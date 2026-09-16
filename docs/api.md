# API Reference

All endpoints are under `/api/` on the listen port (default `127.0.0.1:4004`). GET unless noted. Paths are repo-relative and confined to the root; refs are validated.

## Browsing

### `GET /api/tree`
Full file list (hidden/vendor dirs skipped, 20k cap).
```json
[{"path": "internal/server/server.go", "dir": false}]
```

### `GET /api/file?path=P[&ref=R]`
File content as `text/plain`. `ref` optional: `staged` (index content) or any validated revision. 2MB cap on worktree reads.

### `GET /api/outline?path=P`
Structural outline of one file.
```json
[{"name": "handleSearch", "kind": "func", "line": 46, "sig": "func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request)", "depth": 1}]
```

### `GET /api/search?q=Q[&re=1]`
Parallel content search. Literal substring by default; `re=1` treats `q` as regex (400 on bad pattern).
```json
{
  "query": "handleDiff", "literal": true,
  "matches": [{"path": "internal/server/server.go", "lineNo": 61,
               "line": "\tmux.HandleFunc(\"GET /api/diff\", s.handleDiff)", "col": 35}],
  "files": 1, "truncated": false, "ms": 2
}
```
Caps: 300 matches, 20 per file, 1MB per file, 10s deadline. Binary files (NUL byte in first 800B) skipped. Fast-reject: whole-file literal check before line splitting.

## Cross-reference

### `GET /api/xref?name=X[&mode=defs]`
Call sites of `X` (default) or its definition sites (`mode=defs`). Name-based: `Recv.Name` or bare function name; may over-match same-named symbols.
```json
[{"file": "internal/server/server.go", "line": 61}]
```

## Git

### `GET /api/status`
Working-tree status — porcelain v2 semantics in v1-style fields. The review-queue file (`.codebrowse-review.jsonl`) is always filtered out.
```json
[{"xy": ".M", "path": "main.go"}, {"xy": "??", "path": "new.go"}]
```
Non-git dirs: every file listed as `??` (2000 cap), so writes still surface.

### `GET /api/diff[?path=P][&staged=1][&base=B&ref=R][&commit=H]`
Unified diff. Precedence: `commit` → `base`+`ref` (review range) → per-path worktree/staged. Unstaged default.

### `GET /api/commits` · `GET /api/branches`
Recent commits (`{hash, subject}`) and branch names.

### `GET /api/changed-files?base=B&ref=R`
Files changed in `B...R` with xy codes — review mode's list.

## Chat (agent)

### `GET /api/config`
Capability discovery — drives what the UI renders.
```json
{"backend": "pi", "agent": "…model…", "attachedSession": "…id…", "root": "/abs/repo", "mode": "review"}
```
`backend`: `"pi"` (rpc session), `"direct"` (OpenAI-compatible env fallback), `"none"`.

### `POST /api/chat` · `POST /api/chat/stop`
Send a user message (streams response over the SSE connection) / interrupt the current run.

### `GET /api/chat/history`
Replayed conversation for the attached session (chat panel hydration on load).

### `GET /api/models`
Available models from pi config (or the fallback agent's env).

## Comments

### `GET /api/comments`
Comment queue (JSONL-backed, per-repo file).
```json
[{"id": "…", "file": "main.go", "lineNo": 42, "lineEnd": 0, "ref": "worktree",
  "text": "why is this hardcoded?", "done": false, "pushed": false}]
```

### `POST /api/comments`
Add one. JSON body: `file`, `lineNo`, `lineEnd`, `ref`, `text`. Path guarded (inside root, no traversal).

### `PATCH /api/comments`
Update: `id` + any of `text`, `done`, `pushed`.

### `DELETE /api/comments?id=…`
Remove one.

## Mutations

### `POST /api/revert`
`{"file": "P"}` — discard uncommitted changes to P (`git checkout -- P`); if worktree is clean, revert the newest commit touching P.

### `POST /api/commit` · `POST /api/push`
Stage-all + commit (message built from comment queue) / push committed state.

### `POST /api/shutdown`
Graceful local shutdown of the codebrowse server.

## Live events

### `GET /api/events` (SSE)
One long-lived stream. Server pushes a bare event when the working-tree fingerprint, comment state, or xref index changes; the client debounces (300ms) and refreshes what it shows. Also the channel for streaming chat deltas.
