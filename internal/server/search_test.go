package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func searchTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\nfunc Handle() { doWork() }\nfunc other() { doWork(); doWork() }\n")
	write("b.js", "function doWork() {}\ncallSite(doWork);\n")
	write("deep/nested/c.txt", "needle here\nno match\nanother needle\n")
	write("space path.txt", "needle with space\n")
	write("binary.bin", "ok\x00\x01needle\x02")
	write("big_miss.bin", string(make([]byte, 3000))) // > fastReject, no match
	s := &Server{Root: dir}
	return s
}

func doSearch(t *testing.T, s *Server, query string) searchResult {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/search?q="+query, nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, req)
	if w.Code != 200 {
		t.Fatalf("search %q: status %d: %s", query, w.Code, w.Body.String())
	}
	var res searchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSearchLiteral(t *testing.T) {
	s := searchTestServer(t)
	res := doSearch(t, s, "doWork")
	if res.Files != 2 {
		t.Errorf("want 2 files, got %d: %+v", res.Files, res.Matches)
	}
	if len(res.Matches) != 4 { // a.go: lines 3,4 (2 hits); b.js: lines 1,2 (2 hits)
		t.Errorf("want 4 matches, got %d", len(res.Matches))
	}
	if res.Matches[0].Path != "a.go" || res.Matches[0].LineNo != 3 {
		t.Errorf("first match: want a.go:3, got %s:%d", res.Matches[0].Path, res.Matches[0].LineNo)
	}
}

func TestSearchFastRejectPathWithSpace(t *testing.T) {
	s := searchTestServer(t)
	res := doSearch(t, s, "needle")
	// binary.bin must be skipped (NUL heuristic), c.txt + space path hit.
	paths := map[string]bool{}
	for _, m := range res.Matches {
		paths[m.Path] = true
	}
	if !paths["deep/nested/c.txt"] {
		t.Errorf("missing c.txt: %+v", res.Matches)
	}
	if !paths["space path.txt"] {
		t.Errorf("missing space path.txt: %+v", res.Matches)
	}
	if paths["binary.bin"] {
		t.Errorf("binary.bin should be rejected: %+v", res.Matches)
	}
}

func TestSearchRegex(t *testing.T) {
	s := searchTestServer(t)
	res := doSearch(t, s, "func%20%5BA-Z%5D") // "func [A-Z]"
	if !res.Literal {
		// re flag not passed here; use direct call instead
	}
	req := httptest.NewRequest("GET", "/api/search?q=func+%5BA-Z%5D&re=1", nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, req)
	if w.Code != 200 {
		t.Fatalf("regex search: %d %s", w.Code, w.Body.String())
	}
	var res2 searchResult
	if err := json.Unmarshal(w.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if len(res2.Matches) == 0 || res2.Matches[0].Path != "a.go" {
		t.Errorf("regex matches: %+v", res2.Matches)
	}
}

func TestSearchBadRegex(t *testing.T) {
	s := searchTestServer(t)
	req := httptest.NewRequest("GET", "/api/search?q=%5Ba-&re=1", nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, req)
	if w.Code != 400 {
		t.Errorf("bad regex: want 400, got %d", w.Code)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := searchTestServer(t)
	req := httptest.NewRequest("GET", "/api/search?q=%20", nil)
	w := httptest.NewRecorder()
	s.handleSearch(w, req)
	if w.Code != 400 {
		t.Errorf("blank query: want 400, got %d", w.Code)
	}
}

func TestSortSearchMatches(t *testing.T) {
	ms := []searchMatch{
		{Path: "b.js", LineNo: 2},
		{Path: "a.go", LineNo: 5},
		{Path: "a.go", LineNo: 3},
	}
	sortSearchMatches(ms)
	if ms[0].Path != "a.go" || ms[0].LineNo != 3 || ms[2].Path != "b.js" {
		t.Errorf("sort broken: %+v", ms)
	}
}
