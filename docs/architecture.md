# Architecture

How codebrowse fits together. Read top to bottom for a full picture; each section stands alone.

```
┌─────────────────────────── browser ───────────────────────────┐
│  app.js (vanilla)                                             │
│  file tree · editor · diff view · xref popover · chat panel   │
└──────────────┬────────────────────────────────────────────────┘
               │ /api/* (JSON, SSE for live events)
┌──────────────▼──────────────── Go binary ─────────────────────┐
│  server (mux, handlers, SSE watcher, tree walk)               │
│    ├── gitx        git shell-out, fixed argv                  │
│    ├── symbols     outline (Go AST + regex)                   │
│    ├── xref        name-based call-site index                 │
│    ├── comments    comment queue (JSONL)                      │
│    └── piagent     pi --mode rpc (primary chat)               │
│          └── piconfig   reads pi config for models/endpoints  │
│    └── agent        direct OpenAI-compatible fallback         │
└───────────────────────────────────────────────────────────────┘
```

## Server (internal/server)

One `http.ServeMux` under `/api/`, static assets embedded via `go:embed`. Three things worth knowing:

**Live change detection.** A stat/poll loop on the server watches the working tree and the comments file. When a fingerprint (git status + comments state + xref index stamp) changes, it pushes a bare SSE event — the browser decides what to reload. No websockets, no polling in the client, and events coalesce (300ms debounce) so bursts of agent edits produce one refresh.

**Path confinement.** Client-supplied paths go through `resolve()`: must be inside root, no `..`, no absolute. Refs are validated against a charset. Git is never given a client string without these guards.

**Graceful no-git mode.** Not a repo? `handleStatus` falls back to listing all files as untracked (capped at 2000), so the Changes pane still works for first-time writes.

## Git integration (internal/gitx)

Pure shell-out to the host `git` binary — no go-git (memory), no libgit2 (CGO).

- `status --porcelain=v2 -z --untracked-files=all` — v2 is immune to user git config; `-z` NUL-delimits so paths with spaces/quotes/unicode survive intact. The parser skips the fixed prefix (`kind XY ` + 6 space-free fields: sub, 3 modes, 2 hashes), then treats the remainder as the path; rename records (`2 R. …`) additionally split on tab to drop the origPath.
- Diffs use `--no-color --find-renames`, path after `--` so it is always a pathspec.
- Every call: `exec.CommandContext` with fixed argv, 15s timeout, stderr captured into a typed `*Error`.

## Symbol navigation (internal/symbols, internal/xref)

Two layers, both zero-dependency:

**Outline** (`symbols`): per-file symbol list. Go via AST walk; other languages via family-tagged regex templates (the same line-matcher covers js/ts, python, c, rust, jvm…).

**Xref** (`xref`): repo-wide index of *call sites*, keyed by name.

- **Build**: one pass — parse every `.go` file, record definitions as `Recv.Name` (or bare `Name`), attribute `CallExpr` sites to the enclosing function. Method-value references (`mux.HandleFunc("...", s.handleChat)`) are caught by walking the call's Args explicitly while skipping the Fun subtree.
- **Query**: `Callers(name)` matches sites by name — same-named methods may over-match; interface dispatch is not resolved. Accepted trade-off for a nav tool: instant, offline, no `go/types` cost.
- **Incremental**: `Reindex()` diffs mtimes and re-parses only changed files; a walk-filter bug once wiped JS entries every 1.5s — regression-tested now (`TestReindexKeepsJs`).
- **JS**: line-scanner regex (function decls, arrow funcs, `Class.method`); vendored/minified paths excluded.

The UI: click an identifier → popover of call sites → click → `openFile` + `gotoLine`.

## Search (internal/server/search.go)

Pipeline shaped after px0's engine:

1. `walkFiles()` (already skips hidden/vendor dirs) feeds a bounded worker pool (`runtime.NumCPU()`).
2. **Fast reject** — `bytes.Contains(data, lit)` on the whole file *before* any line splitting (regex mode: one `re.Match(data)` pass). Misses cost one read, nothing else.
3. Binary skip: NUL byte or invalid UTF-8 in the first 800 bytes ⇒ not source.
4. Per-line match extraction, rune-safe column, capped: 20 matches/file, 300 total, 1MB/file, 10s deadline.

Results sorted by path/line; `truncated` flag tells the UI when caps bit.

## Agent chat (internal/piagent, internal/agent, internal/piconfig)

**Chat backends** — two paths, same UI:

- **pi (preferred)** — spawn `pi --mode rpc` with `cwd` = repo root, talk JSON-RPC over stdio. The pi session *is* the terminal's session: same context, same tools, same history. `-attach` resumes the most recent session file for the repo slug (newest by mtime under `~/.pi/agent/sessions/<slug>/`); `-no-attach` starts fresh. Attachment is logged as `agent session attached: <session-id>` — never a full path.
- **direct (fallback)** — when pi isn't installed, `internal/agent` streams from any OpenAI-compatible endpoint configured via `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `CODEBROWSE_MODEL`. Stateless: no session resume, no tools.

`/api/config` reports `backend: pi | direct | none`. Today the **UI shows the chat panel only when `backend == "pi"`** — the direct path works at the API level (same `/api/chat` SSE contract) but has no panel yet; wire it up by relaxing the `backend === "pi"` gates in `app.js` if you want UI parity.

**Config** — models/endpoints come from pi's own config via `piconfig`; env-var references (`$NAME`) are resolved at read time. **Fallback path** — a direct OpenAI-compatible client (`agent`) when pi isn't installed; `/api/config` reports `backend: pi | direct | none` and the UI renders the chat panel only when a backend exists.

## Comments (internal/comments)

One JSONL file per repo (`.codebrowse-review.jsonl`): the queue shared between UI, chat panel, and the terminal pi. Filtered out of the Changes pane server- and client-side so it never shows up as a diff.

## UI (internal/server/static)

Vanilla JS, no framework, no build step. Live refresh preserves scroll — content swaps only when text actually changed (diff views keep the viewport anchored by scroll *ratio*, since diff height changes between renders). Multi-file diffs render stacked (GitHub PR style) with sticky per-file headers and rAF-coalesced scrollspy.
