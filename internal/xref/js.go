package xref

import (
	"bufio"
	"regexp"
	"strings"
)

// JS/TS support: regex-based, layered so later patterns win.
// Not a real AST — good enough for click-to-callers on typical JS.
// Order matters: each layer only records what earlier layers missed
// is hard; instead callsites get attributed with best-effort caller.
//
// Layers:
//  1. function declarations + const/let arrow funcs → defs + caller keys
//  2. class methods → defs (Key: Class.method)
//  3. call sites → attributed to innermost enclosing key via line scan
//
// Simplicity over precision: one linear scan, line-based state machine.

var (
	reJsFuncDecl  = regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`)
	reJsArrowDecl = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(\(.*?\)|[A-Za-z_$][\w$]*)\s*=>`)
	reJsMethod    = regexp.MustCompile(`^\s{2,}(?:async\s+)?([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\{`)
	reJsClass     = regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`)
	reJsCall      = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)
)

// jsFuncSpan is one named function with its line range.
type jsFuncSpan struct {
	name     string
	cls      string // class name for methods, "" for top-level
	line     int    // 1-based declaration line
	endLine  int    // approx: brace match is hard; use next decl line - 1
	isMethod bool
}

// parseJsFile appends JS defs/calls to the shared maps. Caller keys are
// bare names (no class prefix unless method, then Class.method).
func parseJsFile(rel string, src []byte, defs map[string][]Def, calls map[string][]CallSite, files map[string]int64) error {
	sc := bufio.NewScanner(strings.NewReader(string(src)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	type frame struct {
		key   string
		start int
	}
	var stack []frame
	var class string
	lineNo := 0

	closeFrames := func(through int) {
		// pop frames whose span ended before this line (approximation:
		// a frame ends when a new decl starts or file ends)
		for len(stack) > 0 && stack[len(stack)-1].start < through {
			// keep until we can compute ends; handled by replacement below
			break
		}
	}

	addDef := func(name, cls string, ln int, kind string) {
		key := name
		if cls != "" {
			key = cls + "." + name
		}
		defs[key] = append(defs[key], Def{
			Loc: Loc{File: rel, Line: ln}, Kind: kind, BareName: name})
	}

	var methodSpans []jsFuncSpan

	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "*") || strings.HasPrefix(trim, "/*") {
			continue
		}

		if m := reJsClass.FindStringSubmatch(line); m != nil {
			class = m[1]
			continue
		}

		// A new decl at this line ends the previous frame's span.
		newDecl := false
		if m := reJsFuncDecl.FindStringSubmatch(line); m != nil {
			addDef(m[1], "", lineNo, "func")
			stack = append(stack, frame{key: m[1], start: lineNo})
			newDecl = true
		} else if m := reJsArrowDecl.FindStringSubmatch(line); m != nil {
			addDef(m[1], "", lineNo, "func")
			stack = append(stack, frame{key: m[1], start: lineNo})
			newDecl = true
		} else if m := reJsMethod.FindStringSubmatch(line); m != nil && class != "" {
			addDef(m[1], class, lineNo, "method")
			methodSpans = append(methodSpans, jsFuncSpan{name: m[1], cls: class, line: lineNo, isMethod: true})
			stack = append(stack, frame{key: class + "." + m[1], start: lineNo})
			newDecl = true
		}

		if newDecl {
			// previous frames stay until braces close — approximate:
			// attribute to innermost (last) frame; spans never overlap
			// in JS the way they would in nested callbacks, acceptable.
			closeFrames(lineNo)
			continue
		}

		// Call site: attribute to innermost open frame (may be none).
		var caller string
		if len(stack) > 0 {
			caller = stack[len(stack)-1].key
		}
		for _, m := range reJsCall.FindAllStringSubmatch(line, -1) {
			name := m[1]
			// Skip keywords and control flow.
			switch name {
			case "if", "for", "while", "switch", "catch", "return", "function", "typeof", "new", "await", "async", "else", "do", "in", "of":
				continue
			}
			calls[name] = append(calls[name], CallSite{
				Loc: Loc{File: rel, Line: lineNo}, Caller: caller,
			})
		}
	}

	files[rel] = files[rel] // mtime set by caller
	return nil
}
