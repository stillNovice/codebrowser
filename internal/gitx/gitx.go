// Package gitx shells out to git with fixed argument lists.
// No user input is ever interpolated into a shell command.
package gitx

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

type Git struct{ root string }

func New(root string) *Git { return &Git{root: root} }

// Run executes an arbitrary fixed-argv git command (no shell).
func (g *Git) Run(ctx context.Context, args ...string) (string, error) {
	return g.run(ctx, args...)
}

func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...) // fixed argv, no shell
	cmd.Dir = g.root
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.String(), nil
}

type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string { return "git " + e.Args[0] + ": " + e.Stderr }

// Status returns `git status --porcelain=v2 -z`-derived entries.
//
// v2 (unlike v1) is a stable machine format immune to user git config
// (aliases, status.showUntrackedFiles, etc.) and supports renamed/conflicted
// entries cleanly. Records are NUL-delimited:
//
//	unchanged/modified/added/deleted: "1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>"
//	renamed/copied:                   "2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score>\t<path>\t<origPath>"
//	untracked:                        "? <path>"
//	conflicted:                       "u <XY> <...> <path>" (XY from index stages)
func (g *Git) Status(ctx context.Context) ([]StatusEntry, error) {
	out, err := g.run(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var entries []StatusEntry
	for _, rec := range bytes.Split([]byte(out), []byte{0}) {
		if len(rec) < 2 {
			continue
		}
		switch rec[0] {
		case '?': // "? <path>" — surface as v1-style "??" for clients
			path := string(rec[2:])
			if path != "" {
				entries = append(entries, StatusEntry{XY: "??", Path: path})
			}
		case '1', '2', 'u':
			// Full record: "<kind> <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>"
			// ("2" records append "<X><score>\t<origPath>"). The fixed prefix
			// "<kind> <XY> " is 5 bytes; then skip 6 space-free fields (sub,
			// 3 modes, 2 hashes); the remainder is the path (spaces allowed).
			rest := rec
			if len(rest) > 5 {
				rest = rest[5:]
			}
			for i := 0; i < 6; i++ {
				s, r, ok := bytes.Cut(rest, []byte{' '})
				if !ok {
					rest = nil
					break
				}
				_ = s
				rest = r
			}
			path := rest
			if len(path) == 0 {
				continue
			}
			// For "2" records the score "<X><score> " precedes the new path —
			// strip it, then cut the origPath after the tab. "1"/"u" records
			// have no score/tab; their paths may legally contain spaces.
			if rec[0] == '2' {
				if i := bytes.IndexByte(path, '\t'); i >= 0 {
					path = path[:i]
				}
				if sp := bytes.IndexByte(path, ' '); sp >= 0 {
					path = path[sp+1:]
				}
			}
			entries = append(entries, StatusEntry{
				XY:   xyV2(rec[0], rec),
				Path: string(path),
			})
		}
	}
	return entries, nil
}

// xyV2 extracts the two-letter XY code clients already understand
// (porcelain v1 semantics) from a v2 record. rec[0] is the record kind.
func xyV2(kind byte, rec []byte) string {
	// XY sits at a fixed offset right after the kind byte in every
	// v2 record: "1 XY ...", "2 XY ...", "u XY ...".
	if kind == 'u' {
		// Conflicts: XY is the index stage pair (e.g. "AA", "DU").
		off := 2
		if len(rec) >= off+2 {
			return string(rec[off : off+2])
		}
		return "UU"
	}
	if len(rec) >= 4 {
		return string(rec[2:4])
	}
	return "M "
}

type StatusEntry struct {
	XY   string `json:"xy"`
	Path string `json:"path"`
}

// Diff returns the unified diff for unstaged (or staged) changes,
// optionally limited to one path. Path is passed after "--" so it is
// always treated as a pathspec, never an option.
func (g *Git) Diff(ctx context.Context, staged bool, path string) (string, error) {
	args := []string{"diff", "--no-color", "--find-renames"}
	if staged {
		args = append(args, "--staged")
	}
	args = append(args, "--")
	if path != "" {
		args = append(args, path)
	}
	return g.run(ctx, args...)
}

// DiffCommit returns the full diff introduced by one commit.
// The ref is validated to look like a revision before use.
func (g *Git) DiffCommit(ctx context.Context, ref string) (string, error) {
	if len(ref) == 0 || len(ref) > 40 {
		return "", &Error{Args: []string{"show", ref}, Stderr: "invalid ref"}
	}
	for _, r := range ref {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", &Error{Args: []string{"show", ref}, Stderr: "invalid ref"}
		}
	}
	return g.run(ctx, "show", "--no-color", "--find-renames", "--format=commit %h %s", ref, "--")
}

// ShowFile returns the content of path at a revision (branch/tag/commit hash),
// as produced by `git show ref:path`. The ref is a fixed argv element, never a shell.
func (g *Git) ShowFile(ctx context.Context, ref, path string) (string, error) {
	return g.run(ctx, "show", ref+":"+path)
}

// StagedFile returns the index (staged) content of path, as `git show :path`.
func (g *Git) StagedFile(ctx context.Context, path string) (string, error) {
	return g.run(ctx, "show", ":"+path)
}

// RecentCommits returns the last n commits as "<short> <subject>" lines.
func (g *Git) RecentCommits(ctx context.Context, n int) ([]Commit, error) {
	out, err := g.run(ctx, "log", "--format=%h%x00%s", "-n", itoa(n))
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		h, s, _ := strings.Cut(line, "\x00")
		commits = append(commits, Commit{Hash: h, Subject: s})
	}
	return commits, nil
}

type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

func itoa(n int) string {
	if n == 0 {
		return "10"
	}
	b := [8]byte{}
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// IsRepo reports whether root is inside a git work tree.
func (g *Git) IsRepo(ctx context.Context) bool {
	_, err := g.run(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil
}
