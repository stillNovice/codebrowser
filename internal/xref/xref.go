// Package xref builds a name-based cross-reference index of a Go
// repository: symbol definitions and call sites, keyed by method or
// function name. It trades exact type resolution for speed and
// resilience — good enough for click-to-callers navigation.
package xref

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Loc is one source location.
type Loc struct {
	File string `json:"file"` // slash-separated, repo-relative
	Line int    `json:"line"`
}

// Def is one symbol definition. Key "Recv.Name" for methods, bare
// name for functions; bareName is the unqualified form for lookups
// from call sites (which only know the name they called).
type Def struct {
	Loc
	Kind     string `json:"kind"` // func | method
	BareName string `json:"bare"`
}

// CallSite is one call occurrence, tagged with its enclosing function.
type CallSite struct {
	Loc
	Caller string `json:"caller"` // enclosing func key, "" if none
}

// Index is a concurrency-safe xref index over one repo root.
type Index struct {
	root string

	mu    sync.RWMutex
	defs  map[string][]Def      // key → definition sites
	calls map[string][]CallSite // callee key → call sites
	files map[string]int64      // file → parsed mtime (unix nanos)
}

// New creates an empty index for root. Call Build (async) to populate.
func New(root string) *Index {
	return &Index{root: root}
}

// mustRead reads a file, returning nil on any error (regex parse
// tolerates missing content; stat-gated anyway).
func mustRead(root, rel string) []byte {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return b
}

// skip lists directory names never indexed (mirrors server.walkFiles).
var skip = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, "_build": true, "deps": true, "dist": true,
}

// Build walks the whole tree and parses every Go file. Each run
// replaces the index wholesale; safe to call repeatedly.
func (x *Index) Build() error {
	fset := token.NewFileSet()
	defs := map[string][]Def{}
	calls := map[string][]CallSite{}
	files := map[string]int64{}
	n := 0

	err := filepath.WalkDir(x.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		rel, rerr := filepath.Rel(x.root, p)
		if ierr != nil || rerr != nil {
			return nil
		}
		slashed := filepath.ToSlash(rel)
		mtime := info.ModTime().UnixNano()
		switch {
		case strings.HasSuffix(p, ".go"):
			if perr := parseFile(fset, x.root, slashed, mtime, defs, calls, files); perr != nil {
				return nil // unparseable file: skip, keep others
			}
			n++
		case strings.HasSuffix(p, ".js") && !strings.Contains(slashed, "/vendor/") && slashed != "internal/server/static/vendor/marked.min.js":
			if jerr := parseJsFile(slashed, mustRead(x.root, slashed), defs, calls, files); jerr == nil {
				n++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	x.mu.Lock()
	x.defs, x.calls, x.files = defs, calls, files
	x.mu.Unlock()
	return nil
}

// parseFile parses one Go file and merges its defs/calls into shared maps.
func parseFile(fset *token.FileSet, root, rel string, mtime int64,
	defs map[string][]Def, calls map[string][]CallSite, files map[string]int64) error {

	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}

	// Outer declaration scan; per-func body walks below.
	var stack []string

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			key := node.Name.Name
			kind := "func"
			if node.Recv != nil && len(node.Recv.List) > 0 {
				if rt := recvType(node.Recv.List[0].Type); rt != "" {
					key = rt + "." + key
					kind = "method"
				}
			}
			line := fset.Position(node.Pos()).Line
			defs[key] = append(defs[key], Def{
				Loc: Loc{File: rel, Line: line}, Kind: kind, BareName: node.Name.Name})

			// Calls AND method-value references in the body attribute to
			// this function. (Handler wiring like mux.HandleFunc("/x",
			// s.handleChat) is a reference, not a call — both matter for
			// navigation.)
			stack = append(stack, key)
			caller := key
			var walk func(n ast.Node) bool
			walk = func(n ast.Node) bool {
				if ce, ok := n.(*ast.CallExpr); ok {
					if callee := callKey(ce); callee != "" {
						cline := fset.Position(ce.Pos()).Line
						calls[callee] = append(calls[callee], CallSite{
							Loc:    Loc{File: rel, Line: cline},
							Caller: caller,
						})
					}
					// Walk args explicitly; walking Fun would double-record
					// the callee as a reference.
					for _, a := range ce.Args {
						ast.Inspect(a, walk)
					}
					return false
				}
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil {
					// Method value: passed as arg, assigned, or deferred.
					calls[sel.Sel.Name] = append(calls[sel.Sel.Name], CallSite{
						Loc:    Loc{File: rel, Line: fset.Position(sel.Pos()).Line},
						Caller: caller,
					})
				}
				return true
			}
			ast.Inspect(node.Body, walk)
			stack = stack[:len(stack)-1]
			return false // body already inspected
		}
		return true
	})

	files[rel] = mtime
	return nil
}

// callKey extracts a navigable callee key from a call expression.
//   - s.chatWithPi(...) → "chatWithPi"
//   - gitx.New(...)     → "New"
//   - resolve(...)      → "resolve"
//   - newServer(...)    → "newServer"
//   - (anonymous call)  → ""
func callKey(ce *ast.CallExpr) string {
	switch fn := ce.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// recvType renders a receiver type expression as a bare identifier
// ("*Server" → "Server"; generics unwrap to the base type).
func recvType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // G[T]
		return recvType(t.X)
	case *ast.IndexListExpr: // G[T, U]
		return recvType(t.X)
	}
	return ""
}

// Callers returns call sites of the symbol name (bare name; matches
// methods on any receiver type and plain functions).
func (x *Index) Callers(name string) []CallSite {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := x.calls[name]
	if out == nil {
		return []CallSite{}
	}
	return out
}

// DefsOf returns definition sites for a bare name (method or func).
func (x *Index) DefsOf(name string) []Def {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Def
	for key, ds := range x.defs {
		if key == name || strings.HasSuffix(key, "."+name) {
			out = append(out, ds...)
		}
	}
	return out
}

// Reindex reparses changed/new files and drops deleted ones, using
// stored mtimes. Cheap when nothing changed: stat-only.
func (x *Index) Reindex() {
	x.mu.RLock()
	old := make(map[string]int64, len(x.files))
	for k, v := range x.files {
		old[k] = v
	}
	x.mu.RUnlock()

	type job struct {
		rel   string
		mtime int64
	}
	var jobs []job
	seen := map[string]bool{}

	filepath.WalkDir(x.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !(strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".js")) {
			return nil
		}
		info, ierr := d.Info()
		rel, rerr := filepath.Rel(x.root, p)
		if ierr != nil || rerr != nil {
			return nil
		}
		slashed := filepath.ToSlash(rel)
		seen[slashed] = true
		mt := info.ModTime().UnixNano()
		if omt, ok := old[slashed]; !ok || omt != mt {
			jobs = append(jobs, job{slashed, mt})
		}
		return nil
	})

	// Deleted files: present in old, absent from walk.
	var deleted []string
	for k := range old {
		if !seen[k] {
			deleted = append(deleted, k)
		}
	}
	if len(jobs) == 0 && len(deleted) == 0 {
		return
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	// Strip old contributions of touched files, then re-add.
	touched := map[string]bool{}
	for _, j := range jobs {
		touched[j.rel] = true
	}
	for _, d := range deleted {
		touched[d] = true
	}
	defs := map[string][]Def{}
	for k, ds := range x.defs {
		var keep []Def
		for _, d := range ds {
			if !touched[d.File] {
				keep = append(keep, d)
			}
		}
		if len(keep) > 0 {
			defs[k] = keep
		}
	}
	calls := map[string][]CallSite{}
	for k, cs := range x.calls {
		var keep []CallSite
		for _, c := range cs {
			if !touched[c.File] {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			calls[k] = keep
		}
	}
	files := map[string]int64{}
	for k, v := range x.files {
		if !touched[k] {
			files[k] = v
		}
	}

	fset := token.NewFileSet()
	for _, j := range jobs {
		switch {
		case strings.HasSuffix(j.rel, ".go"):
			if perr := parseFile(fset, x.root, j.rel, j.mtime, defs, calls, files); perr != nil {
				files[j.rel] = j.mtime // avoid retry-storm on permanently broken file
			}
		case strings.HasSuffix(j.rel, ".js") && !strings.Contains(j.rel, "/vendor/"):
			if jerr := parseJsFile(j.rel, mustRead(x.root, j.rel), defs, calls, files); jerr == nil {
				files[j.rel] = j.mtime
			}
		default:
			files[j.rel] = j.mtime // unknown ext: mark seen, skip
		}
	}
	x.defs, x.calls, x.files = defs, calls, files
}
