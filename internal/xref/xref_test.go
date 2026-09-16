package xref

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `package demo

type Server struct{}

func (s *Server) handleChat() { s.route() }

func (s *Server) route() {}

func main() {
	srv := &Server{}
	srv.handleChat()
	helper()
}

func helper() { helper() }
`

func writeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "skip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildDefsAndCalls(t *testing.T) {
	x := New(writeTree(t))
	if err := x.Build(); err != nil {
		t.Fatal(err)
	}

	// Method def exists under qualified key with bare name.
	ds := x.DefsOf("handleChat")
	if len(ds) != 1 || ds[0].Kind != "method" || ds[0].BareName != "handleChat" {
		t.Fatalf("DefsOf(handleChat) = %+v", ds)
	}
	if ds[0].File != "main.go" || ds[0].Line != 5 {
		t.Fatalf("def loc = %+v", ds[0].Loc)
	}

	// Callers of handleChat: one site in main, caller "main".
	cs := x.Callers("handleChat")
	if len(cs) != 1 || cs[0].Caller != "main" || cs[0].Line != 11 || cs[0].File != "main.go" {
		t.Fatalf("Callers(handleChat) = %+v", cs)
	}

	// Recursion: helper calls itself.
	cs = x.Callers("helper")
	if len(cs) != 2 {
		t.Fatalf("Callers(helper) = %+v, want 2", cs)
	}
}

func TestReindexPicksUpChanges(t *testing.T) {
	dir := writeTree(t)
	x := New(dir)
	if err := x.Build(); err != nil {
		t.Fatal(err)
	}
	if got := x.Callers("helper"); len(got) != 2 {
		t.Fatalf("before: %+v", got)
	}

	// Remove the recursive call; mtime granularity of Reindex is
	// nanoseconds, but bump the file with a fresh write.
	edited := `package demo

func helper() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	x.Reindex()
	if got := x.Callers("helper"); len(got) != 0 {
		t.Fatalf("after: %+v, want empty", got)
	}
	if got := x.DefsOf("handleChat"); len(got) != 0 {
		t.Fatalf("defs after: %+v, want empty", got)
	}
}

func TestReindexKeepsJs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("function chatSend() {\n  helperX();\n}\n"), 0o644)
	x := New(dir)
	if err := x.Build(); err != nil {
		t.Fatal(err)
	}
	if got := len(x.Callers("helperX")); got != 1 {
		t.Fatalf("before reindex: %d sites, want 1", got)
	}
	x.Reindex() // nothing changed — must be a no-op, not a wipe
	if got := len(x.Callers("helperX")); got != 1 {
		t.Fatalf("after reindex: %d sites, want 1 (Reindex wiped JS!)", got)
	}
	// And a Go file change must not drop the JS sites either.
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	os.Remove(filepath.Join(dir, "app.js"))
	x.Reindex()
	if got := len(x.Callers("helperX")); got != 0 {
		t.Fatalf("after js delete: %d sites, want 0", got)
	}
}
