// Package server — workspace content search.
//
// Parallel full-text scan with a two-stage pipeline borrowed from px0's
// search engine: cheap literal fast-reject on the raw bytes BEFORE any
// line splitting, then per-line match extraction on the survivors.
// No external processes, no line splitting on files that can't match.
package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	searchMaxResults = 300     // hard cap on returned matches
	searchMaxFileCap = 1 << 20 // skip files > 1MB
	searchMaxLineLen = 400     // truncate long lines in results
	searchFileCap    = 20      // per-file match cap (generated files)
	searchTimeout    = 10 * time.Second
)

type searchMatch struct {
	Path   string `json:"path"`
	LineNo int    `json:"lineNo"`
	Line   string `json:"line"`
	Col    int    `json:"col"` // rune offset of first match in line
}

type searchResult struct {
	Query   string        `json:"query"`
	Literal bool          `json:"literal"`
	Matches []searchMatch `json:"matches"`
	Files   int           `json:"files"` // files with ≥1 match
	Trunc   bool          `json:"truncated"`
	Ms      int64         `json:"ms"`
}

// handleSearch answers GET /api/search?q=...[&re=1].
// Default is literal substring (fast path); ?re=1 enables regex mode.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" || len(q) > 300 {
		writeErr(w, http.StatusBadRequest, "bad query")
		return
	}
	useRE := r.URL.Query().Get("re") == "1"

	var lit []byte
	var re *regexp.Regexp
	if useRE {
		var err error
		re, err = regexp.Compile(q)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad regex")
			return
		}
	} else {
		lit = []byte(q)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	res := searchResult{Query: q, Literal: !useRE}
	var mu sync.Mutex
	var wg sync.WaitGroup

	files := s.walkFiles()
	jobs := make(chan string)
	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case jobs <- f.Path:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Bounded worker pool (px0 pattern): NumCPU workers, results under
	// one mutex, hard deadline + result cap bound total work.
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				mu.Lock()
				full := res.Trunc
				mu.Unlock()
				if full {
					return
				}
				n := s.searchOneFile(path, lit, re, func(m searchMatch) bool {
					mu.Lock()
					defer mu.Unlock()
					if len(res.Matches) >= searchMaxResults {
						res.Trunc = true
						return false // stop signal
					}
					res.Matches = append(res.Matches, m)
					return true
				})
				if n {
					mu.Lock()
					res.Files++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	sortSearchMatches(res.Matches)
	res.Ms = time.Since(start).Milliseconds()
	writeJSON(w, res)
}

// searchOneFile runs the fast-reject pipeline for one file. Reports each
// match via emit (return false from emit to stop). Returns true when the
// file produced ≥1 match.
func (s *Server) searchOneFile(path string, lit []byte, re *regexp.Regexp, emit func(searchMatch) bool) bool {
	info, err := os.Stat(s.Root + "/" + path)
	if err != nil || info.IsDir() || info.Size() > searchMaxFileCap {
		return false
	}
	data, err := os.ReadFile(s.Root + "/" + path)
	if err != nil {
		return false
	}

	// Fast reject: whole-file check BEFORE line splitting. In literal mode
	// this is bytes.Contains (memchr-fast); regex mode pays one full scan
	// here but saves line splitting on misses.
	if re == nil {
		if !bytes.Contains(data, lit) {
			return false
		}
	} else if !re.Match(data) {
		return false
	}

	// Binary heuristics: NUL byte in first 800 bytes ⇒ not source.
	head := data
	if len(head) > 800 {
		head = head[:800]
	}
	if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(head) {
		return false
	}

	found := false
	for li, line := range bytes.Split(data, []byte{'\n'}) {
		var idx = -1
		if re != nil {
			if loc := re.FindIndex(line); loc != nil {
				idx = loc[0]
			}
		} else if i := bytes.Index(line, lit); i >= 0 {
			idx = i
		}
		if idx < 0 {
			continue
		}
		found = true
		text := string(line)
		if len(text) > searchMaxLineLen {
			text = text[:searchMaxLineLen] + "…"
		}
		col := utf8.RuneCountInString(string(line[:idx]))
		if !emit(searchMatch{Path: path, LineNo: li + 1, Line: text, Col: col}) {
			break
		}
		if li+1 >= searchFileCap {
			break
		}
	}
	return found
}

// sortSearchMatches orders by path then line for stable UI output.
func sortSearchMatches(ms []searchMatch) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0; j-- {
			a, b := ms[j-1], ms[j]
			if a.Path < b.Path || (a.Path == b.Path && a.LineNo <= b.LineNo) {
				break
			}
			ms[j-1], ms[j] = ms[j], ms[j-1]
		}
	}
}
