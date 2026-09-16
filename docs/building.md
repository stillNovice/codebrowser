# Building & Releasing

## Build

```sh
make build        # stamps VERSION + git sha into the binary
make check        # gofmt + vet + tests + JS syntax (pre-push gate)
make dist         # cross-compile darwin/linux (amd64/arm64) into dist/
```

```sh
go build -o codebrowse .
```

With a version stamp (shown in the UI header):

```sh
go build -buildvcs=false \
  -ldflags "-X main.BuildVersion=$(git rev-parse --short HEAD)" \
  -o codebrowse .
```

Static assets under `internal/server/static/` are embedded via `go:embed` — the binary is fully self-contained. Nothing to install at runtime.

## Verify before committing

```sh
gofmt -w . && go vet ./... && go build -buildvcs=false -o codebrowse . && go test ./...
node --check internal/server/static/app.js   # UI syntax gate
```

## Dev loop

```sh
./serve.sh -r /path/to/repo        # build + (re)start + print URL
./dev.sh                           # tmux watcher: rebuild on save
```

`serve.sh` kills any instance already serving the same repo, so re-running it is always safe.

## Tests

| Package | Covers |
| --- | --- |
| `internal/gitx` | porcelain v2 parsing (rename, space-paths, untracked, conflicts shape), clean tree, non-repo |
| `internal/xref` | call-site indexing, method values, incremental reindex keeping JS |
| `internal/server` | search pipeline (literal/regex/binary-skip/space-paths), sorting |

Tests use `t.TempDir()` and real `git` subprocesses — never the developer's own repos, never destructive git operations outside temp dirs.

## Repo hygiene

- `.gitignore` covers the `codebrowse` binary and the review-queue JSONL.
- Static/vendor files are committed intentionally (zero-dependency goal); they are the only vendored code.
- Keep secrets out: credentials flow via environment variables only.
