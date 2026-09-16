// Package server wires the HTTP API and embedded web UI together.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codebrowse/internal/agent"
	"codebrowse/internal/comments"
	"codebrowse/internal/gitx"
	"codebrowse/internal/piagent"
	"codebrowse/internal/symbols"
	"codebrowse/internal/xref"
)

//go:embed all:static
var staticFS embed.FS

// Version is the build stamp shown in the UI header.
var Version = "dev"

type Server struct {
	Root         string
	Git          *gitx.Git
	Symbols      *symbols.Service
	Xref         *xref.Index
	Agent        *agent.Agent     // fallback: direct OpenAI-compatible client
	Pi           *piagent.Session // preferred: the pi harness itself
	Session      bool             // true when attached to an existing pi session
	SessionFile  string           // path of the attached pi session JSONL (history source)
	OpenFile     string           // repo-relative file to open on load
	Mode         string           // launch mode: "review" or "build" ("" = default)
	Comments     *comments.Store
	chatMu       sync.Mutex // one chat run at a time
	chatCancel   context.CancelFunc
	chatCancelMu sync.Mutex
	evMu         sync.Mutex
	evSubs       map[chan struct{}]struct{}
	curModel     string // pi session's current model key
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/file", s.handleFile)
	mux.HandleFunc("GET /api/outline", s.handleOutline)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/xref", s.handleXref)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/diff", s.handleDiff)
	mux.HandleFunc("GET /api/commits", s.handleCommits)
	mux.HandleFunc("GET /api/branches", s.handleBranches)
	mux.HandleFunc("GET /api/changed-files", s.handleChangedFiles)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat/stop", s.handleChatStop)
	mux.HandleFunc("GET /api/chat/history", s.handleChatHistory)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/comments", s.handleListComments)
	mux.HandleFunc("POST /api/comments", s.handleAddComment)
	mux.HandleFunc("POST /api/revert", s.handleRevert)
	mux.HandleFunc("POST /api/commit", s.handleCommit)
	mux.HandleFunc("POST /api/push", s.handlePush)
	mux.HandleFunc("PATCH /api/comments", s.handlePatchComment)
	mux.HandleFunc("DELETE /api/comments", s.handleDeleteComment)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)

	sub, _ := fs.Sub(staticFS, "static")
	// No-cache: binaries are rebuilt and swapped frequently; stale
	// HTML/JS mixing across versions causes exactly-subtle breakage.
	mux.Handle("GET /", noCache(http.FileServerFS(sub)))
	return logRequests(mux)
}

// resolve turns a client-supplied relative path into an absolute path
// guaranteed to live under Root. Rejects traversal outright.
func (s *Server) resolve(rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\x00") {
		return "", errors.New("bad path")
	}
	abs := filepath.Join(s.Root, filepath.Clean("/"+rel))
	if abs != s.Root && !strings.HasPrefix(abs, s.Root+string(os.PathSeparator)) {
		return "", errors.New("path escapes repository root")
	}
	return abs, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) // generic, no internals
}

// ---- handlers ----

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, "_build": true, "deps": true, "dist": true,
}

type treeEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// walkFiles lists every regular file under Root (skipping hidden and
// vendor-ish dirs). Shared by the tree view and the no-git fallback
// status, so a freshly-created file shows up in Changes even before
// the directory is a git repository.
func (s *Server) walkFiles() []treeEntry {
	var entries []treeEntry
	filepath.WalkDir(s.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			entries = append(entries, treeEntry{Path: filepath.ToSlash(rel)})
		}
		return nil
	})
	return entries
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	entries := s.walkFiles()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) > 20000 {
		entries = entries[:20000]
	}
	writeJSON(w, entries)
}

const maxFileBytes = 2 << 20

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	abs, err := s.resolve(q.Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	var data []byte
	if ref := q.Get("ref"); ref != "" && ref != "worktree" {
		rel := filepath.ToSlash(q.Get("path"))
		var out string
		var err error
		if ref == "staged" {
			out, err = s.Git.StagedFile(r.Context(), rel)
		} else {
			if !validRefName(ref) {
				writeErr(w, http.StatusBadRequest, "bad ref")
				return
			}
			out, err = s.Git.ShowFile(r.Context(), ref, rel)
		}
		if err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		data = []byte(out)
	} else {
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() > maxFileBytes {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		data, err = os.ReadFile(abs)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read failed")
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// handleXref answers callers/callees queries for a symbol name.
//
//   - ?name=X            → all call sites of X (click-to-callers)
//   - ?name=X&mode=defs  → definition sites of X
//
// Name-based: works across files/packages, may over-match same-named
// symbols — acceptable for navigation.
func (s *Server) handleXref(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" || len(name) > 200 || !validRefName(name) {
		writeErr(w, http.StatusBadRequest, "bad name")
		return
	}
	if s.Xref == nil {
		writeErr(w, http.StatusServiceUnavailable, "xref not ready")
		return
	}
	out := struct {
		Name    string          `json:"name"`
		Defs    []xref.Def      `json:"defs"`
		Callers []xref.CallSite `json:"callers"`
	}{Name: name, Callers: []xref.CallSite{}}
	if r.URL.Query().Get("mode") == "defs" {
		out.Defs = s.Xref.DefsOf(name)
		if out.Defs == nil {
			out.Defs = []xref.Def{}
		}
	} else {
		out.Callers = s.Xref.Callers(name)
	}
	writeJSON(w, out)
}

func (s *Server) handleOutline(w http.ResponseWriter, r *http.Request) {
	abs, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	syms, err := s.Symbols.Outline(abs, data)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "parse failed")
		return
	}
	writeJSON(w, syms)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Git.Status(r.Context())
	if err != nil {
		// No git (or git failed): every file is effectively "untracked",
		// so first-time writes still surface in the Changes pane.
		st = nil
		for _, e := range s.walkFiles() {
			st = append(st, gitx.StatusEntry{XY: "??", Path: e.Path})
			if len(st) >= 2000 {
				break
			}
		}
	}
	// codebrowse's own metadata file is never a reviewable change.
	filtered := st[:0]
	for _, e := range st {
		if e.Path != comments.CommentsFile {
			filtered = append(filtered, e)
		}
	}
	writeJSON(w, filtered)
}

func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Git.RecentCommits(r.Context(), 15)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git log failed")
		return
	}
	writeJSON(w, cs)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if commit := q.Get("commit"); commit != "" {
		diff, err := s.Git.DiffCommit(r.Context(), commit)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad commit")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(diff))
		return
	}
	path := q.Get("path")
	if path != "" {
		if _, err := s.resolve(path); err != nil {
			writeErr(w, http.StatusBadRequest, "bad path")
			return
		}
	}
	if base := q.Get("base"); base != "" { // review mode: diff merge-base..ref
		ref := q.Get("ref")
		if !validRefName(base) || !validRefName(ref) {
			writeErr(w, http.StatusBadRequest, "bad ref")
			return
		}
		args := []string{"diff", "--no-color", "--find-renames", base + "..." + ref, "--"}
		if path != "" {
			args = append(args, filepath.FromSlash(path))
		}
		diff, err := s.Git.Run(r.Context(), args...)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "git diff failed")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(diff))
		return
	}
	diff, err := s.Git.Diff(r.Context(), q.Get("staged") == "1", filepath.FromSlash(path))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git diff failed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(diff))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]any{
		"version":       Version,
		"terminalModel": terminalModelFor(s.Root), "root": s.Root, "isRepo": s.Git.IsRepo(r.Context()), "agent": false, "backend": "none", "openFile": s.OpenFile, "mode": s.Mode}
	if s.Pi != nil {
		cfg["agent"] = true
		cfg["backend"] = "pi"
		cfg["attachedSession"] = s.Session
		if s.Agent != nil {
			cfg["defaultModel"] = s.Agent.Default()
		}
	} else if s.Agent != nil {
		cfg["agent"] = true
		cfg["backend"] = "direct"
		cfg["defaultModel"] = s.Agent.Default()
	}
	writeJSON(w, cfg)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.Agent.Models())
}

// ---- review comments ----

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Comments.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read failed")
		return
	}
	if cs == nil {
		cs = []comments.Comment{}
	}
	writeJSON(w, cs)
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	var c comments.Comment
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	// Only repo-relative file paths from the client; validate like file routes.
	// Ref may be "worktree"/"staged", a commit hash, or (review mode) a
	// branch name — reuse the ref-name validator.
	if c.Ref != "" && c.Ref != "worktree" && c.Ref != "staged" && !validRefName(c.Ref) {
		writeErr(w, http.StatusBadRequest, "bad ref")
		return
	}
	if _, err := s.resolve(c.File); err != nil {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	saved, err := s.Comments.Add(c)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid comment")
		return
	}
	writeJSON(w, saved)
}

func (s *Server) handlePatchComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"` // "done" | "reopen" | "push"
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	var err error
	switch body.Action {
	case "done":
		err = s.Comments.SetDone(body.ID, true)
	case "reopen":
		err = s.Comments.SetDone(body.ID, false)
	case "push":
		err = s.Comments.SetPushed(body.ID)
	default:
		writeErr(w, http.StatusBadRequest, "bad action")
		return
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	if err := s.Comments.Delete(r.URL.Query().Get("id")); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type chatRequest struct {
	Model    string          `json:"model"`
	Messages []agent.Message `json:"messages"`
}

// handleChat streams agent tokens back as server-sent events.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if s.Pi == nil && s.Agent == nil {
		writeErr(w, http.StatusServiceUnavailable, "agent not configured")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(v any) {
		b, _ := json.Marshal(v)
		w.Write([]byte("data: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	// A cancellable context lets a separate /api/chat/stop request abort
	// the in-flight run (via pi's "abort" command or the direct client ctx).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s.chatCancelMu.Lock()
	s.chatCancel = cancel
	s.chatCancelMu.Unlock()
	defer func() {
		s.chatCancelMu.Lock()
		if s.chatCancel != nil {
			s.chatCancel = nil
		}
		s.chatCancelMu.Unlock()
	}()

	var err error
	if s.Pi != nil {
		err = s.chatWithPi(r, req, send, ctx)
	} else {
		err = s.Agent.StreamChat(ctx, req.Model, req.Messages, func(tok string) {
			send(map[string]string{"delta": tok})
		})
	}
	if err != nil {
		send(map[string]string{"error": "stream failed"})
	}
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

// handleChatStop aborts the in-flight chat run if one is active.
func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	s.chatCancelMu.Lock()
	cancel := s.chatCancel
	s.chatCancelMu.Unlock()
	if cancel == nil {
		writeJSON(w, map[string]bool{"ok": false})
		return
	}
	cancel()
	writeJSON(w, map[string]bool{"ok": true})
}

// chatWithPi runs one prompt through the pi harness. pi keeps the
// conversation server-side, so only the final user message is sent;
// tool activity is forwarded so the UI can show what the agent does.
func (s *Server) chatWithPi(r *http.Request, req chatRequest, send func(any), ctx context.Context) error {
	if len(req.Messages) == 0 {
		return errBadMessages
	}
	// Model switch if the dropdown selection changed.
	if req.Model != "" && req.Model != s.curModel && s.Agent != nil {
		for _, m := range s.Agent.Models() {
			if m.Key == req.Model {
				if err := s.Pi.SetModel(m.Provider, m.ID); err == nil {
					s.curModel = m.Key
				}
				break
			}
		}
	}
	last := req.Messages[len(req.Messages)-1]
	return s.Pi.Prompt(ctx, last.Content, piagent.Callbacks{
		OnDelta: func(text string) { send(map[string]string{"delta": text}) },
		OnTool: func(name, phase, summary string) {
			send(map[string]string{"tool": name, "phase": phase, "summary": summary})
		},
	})
}

var errBadMessages = errors.New("no messages")

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately minimal: never log query strings (paths may be sensitive).
		next.ServeHTTP(w, r)
	})
}

// ---------- live change events (SSE) ----------
// The server watches the working tree + review-comments file itself
// (cheap stat/poll loop) and pushes an event only when something
// actually changed. The browser holds one EventSource connection and
// never polls on a timer.

func (s *Server) treeFingerprint() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := s.Git.Status(ctx)
	if err != nil {
		return "err"
	}
	var b strings.Builder
	for _, c := range st {
		b.WriteString(c.XY)
		b.WriteString(c.Path)
		b.WriteByte('|')
	}
	// Comments file content also matters (pi marks done / adds lines).
	if cs, err := s.Comments.List(); err == nil {
		for _, c := range cs {
			fmt.Fprintf(&b, "%s:%v:%v;", c.ID, c.Done, c.Pushed)
		}
	}
	// Symbol index freshness: pick up editor/agent edits between events.
	if s.Xref != nil {
		go s.Xref.Reindex()
	}
	return b.String()
}

// watchTree polls the fingerprint and broadcasts "change" events.
func (s *Server) WatchTree() {
	last := s.treeFingerprint()
	for range time.Tick(1500 * time.Millisecond) {
		fp := s.treeFingerprint()
		if fp == last {
			continue
		}
		last = fp
		s.evMu.Lock()
		for ch := range s.evSubs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		s.evMu.Unlock()
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	ch := make(chan struct{}, 4)
	s.evMu.Lock()
	if s.evSubs == nil {
		s.evSubs = map[chan struct{}]struct{}{}
	}
	s.evSubs[ch] = struct{}{}
	s.evMu.Unlock()
	defer func() {
		s.evMu.Lock()
		delete(s.evSubs, ch)
		s.evMu.Unlock()
	}()
	// Initial hello so the client knows the stream is live.
	fmt.Fprintf(w, ": hello\n\n")
	fl.Flush()
	ka := time.NewTicker(25 * time.Second) // keep-alive comment
	defer ka.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprintf(w, "data: change\n\n")
			fl.Flush()
		case <-ka.C:
			fmt.Fprintf(w, ": ka\n\n")
			fl.Flush()
		}
	}
}

// terminalModelFor reads the pi extension's session report
// (~/.pi/agent/codebrowse-inbox-sessions.json) and returns the model of
// the terminal session whose cwd contains (or equals) this repo.
func terminalModelFor(root string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "codebrowse-sessions.json"))
	if err != nil {
		return ""
	}
	var db map[string]struct {
		Model string `json:"model"`
		TS    int64  `json:"ts"`
	}
	if json.Unmarshal(data, &db) != nil {
		return ""
	}
	best, bestLen := "", 0
	for cwd, e := range db {
		if e.Model == "" {
			continue
		}
		if strings.HasPrefix(cwd, root) || strings.HasPrefix(root, cwd) {
			if len(cwd) > bestLen {
				best, bestLen = e.Model, len(cwd)
			}
		}
	}
	return best
}

// handleRevert undoes the last change to a file: if it has uncommitted
// modifications, discard them; otherwise revert the most recent commit
// that touched it (creates an inverse commit — no history rewriting).
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.File == "" ||
		strings.Contains(req.File, "..") || strings.HasPrefix(req.File, "/") {
		http.Error(w, "bad file", 400)
		return
	}
	ctx := r.Context()

	// Case 1: uncommitted changes to this file → discard them.
	st, err := s.Git.Status(ctx)
	if err == nil {
		for _, e := range st {
			if e.Path == req.File {
				if _, err := s.Git.Run(ctx, "checkout", "--", req.File); err != nil {
					http.Error(w, "checkout failed: "+err.Error(), 500)
					return
				}
				writeJSON(w, map[string]string{"result": "discarded uncommitted changes"})
				return
			}
		}
	}

	// Case 2: clean worktree → revert the newest commit touching the file.
	sha, err := s.Git.Run(ctx, "log", "-1", "--format=%H", "--", req.File)
	if err != nil || strings.TrimSpace(sha) == "" {
		http.Error(w, "nothing to revert", 404)
		return
	}
	sha = strings.TrimSpace(sha)
	if _, err := s.Git.Run(ctx, "revert", "--no-edit", sha); err != nil {
		http.Error(w, "revert failed (conflict?): "+err.Error(), 409)
		return
	}
	writeJSON(w, map[string]string{"result": "reverted commit " + sha[:12]})
}

// handleCommit stages everything and commits with the given message.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
		Push    bool   `json:"push"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message required", 400)
		return
	}
	if len(req.Message) > 500 {
		http.Error(w, "message too long", 400)
		return
	}
	ctx := r.Context()
	if _, err := s.Git.Run(ctx, "add", "-A"); err != nil {
		http.Error(w, "add failed: "+err.Error(), 500)
		return
	}
	if _, err := s.Git.Run(ctx, "commit", "-m", req.Message); err != nil {
		http.Error(w, "commit failed: "+err.Error(), 500)
		return
	}
	result := "committed"
	if req.Push {
		if _, err := s.Git.Run(ctx, "push"); err != nil {
			http.Error(w, "committed locally, but push failed: "+err.Error(), 502)
			return
		}
		result = "committed and pushed"
	}
	writeJSON(w, map[string]string{"result": result})
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Git.Run(r.Context(), "push"); err != nil {
		http.Error(w, "push failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"result": "pushed"})
}

// validRefName allows branch/tag-ish names, rejects flags and traversal.
func validRefName(r string) bool {
	if r == "" || len(r) > 120 || strings.HasPrefix(r, "-") || strings.Contains(r, "..") {
		return false
	}
	for _, c := range r {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c == '-' || c == '_' || c == '/' || c == '.') {
			return false
		}
	}
	return true
}

// handleBranches returns current branch, all local+remote branches, and a
// best-guess base branch (master > main).
func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := s.Git.Run(ctx, "branch", "--show-current")
	out, err := s.Git.Run(ctx, "branch", "-a", "--format=%(refname:short)")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git branch failed")
		return
	}
	var branches []string
	base := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasSuffix(l, "/HEAD") {
			continue
		}
		branches = append(branches, l)
		short := l
		if i := strings.LastIndex(short, "/"); i >= 0 {
			short = short[i+1:]
		}
		if short == "master" {
			base = l
		} else if short == "main" && base == "" {
			base = l
		}
	}
	writeJSON(w, map[string]any{
		"current":  strings.TrimSpace(cur),
		"branches": branches,
		"base":     base,
	})
}

// handleChangedFiles lists files changed in base...ref (review mode).
func (s *Server) handleChangedFiles(w http.ResponseWriter, r *http.Request) {
	base, ref := r.URL.Query().Get("base"), r.URL.Query().Get("ref")
	if !validRefName(base) || !validRefName(ref) {
		writeErr(w, http.StatusBadRequest, "bad ref")
		return
	}
	out, err := s.Git.Run(r.Context(), "diff", "--name-status", "--find-renames", base+"..."+ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git diff failed")
		return
	}
	type entry struct {
		Path string `json:"path"`
		XY   string `json:"xy"`
	}
	var files []entry
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			xy := f[0][0:1] + " "
			files = append(files, entry{Path: f[len(f)-1], XY: xy})
		}
	}
	if files == nil {
		files = []entry{}
	}
	writeJSON(w, files)
}

// handleShutdown stops this codebrowse process without touching the repository
// or the attached pi session. The delayed exit lets the HTTP response flush.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.Exit(0)
	}()
}

// handleChatHistory replays the attached pi session's conversation.
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	if s.Pi == nil || s.SessionFile == "" {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, piagent.History(s.SessionFile))
}
