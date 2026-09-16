// Package symbols provides structural outlines of source files.
// Go files get a real AST parse via go/ast; other languages fall back
// to a heuristic scanner. The Parser interface is the seam where a
// tree-sitter binding can be dropped in later.
package symbols

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // func, method, type, struct, interface, heading...
	Line  int    `json:"line"`
	Sig   string `json:"sig,omitempty"`
	Depth int    `json:"depth"`
}

type Parser interface {
	Extensions() []string
	Parse(src []byte) ([]Symbol, error)
}

type Service struct{ parsers []Parser }

func New() *Service { return &Service{parsers: []Parser{goParser{}}} }

func (s *Service) Outline(path string, src []byte) ([]Symbol, error) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, p := range s.parsers {
		for _, e := range p.Extensions() {
			if e == ext {
				return p.Parse(src)
			}
		}
	}
	return heuristic(src), nil
}

// ---- go/ast parser ----

type goParser struct{}

func (goParser) Extensions() []string { return []string{".go"} }

func (goParser) Parse(src []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var syms []Symbol
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			pos := fset.Position(d.Pos())
			kind := "func"
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				name = recvType(d.Recv.List[0].Type) + "." + name
			}
			syms = append(syms, Symbol{Name: name, Kind: kind, Line: pos.Line})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				kind := "type"
				switch ts.Type.(type) {
				case *ast.StructType:
					kind = "struct"
				case *ast.InterfaceType:
					kind = "interface"
				}
				syms = append(syms, Symbol{
					Name: ts.Name.Name, Kind: kind,
					Line: fset.Position(ts.Pos()).Line,
				})
			}
		}
	}
	return syms, nil
}

func recvType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvType(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// ---- heuristic fallback (tree-sitter replaces this) ----

var reDecl = regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?` +
	`(func|fn|def|class|interface|struct|enum|impl|module|defmodule)` +
	`[\s:]+([A-Za-z_][\w!?]*)?`)

func heuristic(src []byte) []Symbol {
	var syms []Symbol
	for i, line := range strings.Split(string(src), "\n") {
		if m := reDecl.FindStringSubmatch(line); m != nil {
			syms = append(syms, Symbol{
				Kind: m[1], Name: m[2], Line: i + 1,
			})
		}
	}
	return syms
}
