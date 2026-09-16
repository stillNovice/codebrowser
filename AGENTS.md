# AGENTS.md — codebrowse

Guidance for AI agents (and humans) working in this repository.

## What this is

A localhost Go code browser with an embedded agent chat panel. Single static binary, vanilla JS frontend, zero external dependencies (Go stdlib + vendored `highlight.js`/`marked`). Read `docs/architecture.md` before non-trivial changes.

## Ground rules

- **Go stdlib only.** No new Go dependencies. The vendored JS under `internal/server/static/vendor/` is the only allowed third-party code.
- **Fixed argv subprocess calls only.** Never shell-interpolate anything into `git`/`pi` invocations. Client-supplied paths/refs must pass validation (`resolve()`, `validRefName`) before use.
- **Path confinement.** Anything the browser sends (paths, refs, search queries) is untrusted input. Validate type, length, and containment before filesystem or subprocess use.
- **No secrets in code.** Credentials come from the environment (`OPENAI_API_KEY`, pi's config with `$VAR` refs). Never log PII, tokens, or full user paths — repo basename only in logs and UI.
- **Tests use `t.TempDir()` and throwaway git repos.** Never run git mutations against a real user repository from a test or script. (This bit us once: a test script swept uncommitted work into a throwaway branch. Don't repeat it.)
- **Keep changes minimal and aligned** with existing patterns in the package you touch.

## Verify before you push

```sh
make check    # gofmt + go vet + full test suite + JS syntax gate
```

`make build` produces the version-stamped binary (`VERSION` file + git sha).

## Where things live

| Path | Purpose |
| --- | --- |
| `main.go` | flags, bootstrap, xref warm-up |
| `internal/server/` | HTTP API, SSE watcher, search, embedded static UI |
| `internal/server/static/` | vanilla JS UI (`app.js` is the monolith), CSS, vendored libs |
| `internal/gitx/` | git shell-out (porcelain v2) |
| `internal/xref/` | name-based call-site index (Go AST + JS scanner) |
| `internal/symbols/` | per-file outline |
| `internal/piagent/` | `pi --mode rpc` session lifecycle |
| `internal/agent/` | direct OpenAI-compatible fallback client |
| `internal/piconfig/` | reads pi's config for models/endpoints |
| `internal/comments/` | comment queue (JSONL per repo) |

## Conventions

- Commits: imperative, ≤50 chars, no AI attribution.
- UI text and comments: lowercase, terse.
- SSE refresh: server pushes bare events; the client debounces (300ms) and reloads with scroll preservation. Don't add polling.
- API latency: hot paths should stay well under 100ms p99 — the whole point of this tool is instant reads.
