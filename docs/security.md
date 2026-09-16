# Security Notes

Threat model: codebrowse runs on localhost against a repo you control, but renders agent-controlled content, executes git operations, and sometimes listens while other users exist on the machine. Rules the codebase follows:

## Process boundaries

- **Fixed argv only.** Every subprocess (`git`, `pi`) goes through `exec.CommandContext` with a literal argument list. No shell, ever — no interpolation into `sh -c` anywhere in the codebase.
- **Client strings never reach argv raw.** Paths pass through `resolve()` (must stay inside root); refs are charset-validated before use; diff paths are passed after `--` so they parse as pathspecs, not options.

## Path confinement

- `resolve()` rejects absolute paths and `..` traversal; file reads are capped (2MB) and confined to the repo root.
- Xref/search operate on the walked tree only (hidden + vendor dirs skipped).
- The session-attach log records the session **id**, never the full session file path.

## Secrets & credentials

- Credentials come exclusively from the environment (`OPENAI_API_KEY`, pi's own auth, `$VAR` references in pi config resolved at read time). No secrets in code, config files, or the repo.
- Nothing sensitive is logged: no PII, no full user paths (repo shown as basename in logs/UI), no tokens.

## Network posture

- Binds `127.0.0.1` by default. Passing `-host` exposes it deliberately — do so only on trusted networks.
- No outbound calls except to the configured LLM endpoint (from pi config / env). No telemetry, no phone-home.

## Content the browser renders

- Diffs, file contents and chat output are inserted as text nodes / highlighted code — not raw HTML — except the explicitly-marked Markdown/HTML rich views, which render user-opened repo files, never chat output.
- Comment text and model output are escaped at insertion points.

## Review checklist for changes

1. New endpoints: path/ref validation before any git or filesystem use?
2. New subprocess calls: fixed argv, timeout, stderr captured?
3. New UI injection points: text node or context-appropriate escaping?
4. New config: env-var sourced, documented, nothing hardcoded?
