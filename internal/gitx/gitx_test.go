package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitIn runs a git command in dir; skips test if git is unavailable.
func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "warning:") {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestStatusPorcelainV2(t *testing.T) {
	dir := t.TempDir()
	runGitIn(t, dir, "init", "-q", "--initial-branch=main")
	runGitIn(t, dir, "config", "user.email", "t@t")
	runGitIn(t, dir, "config", "user.name", "t")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("mod.txt", "a\n")
	write("del.txt", "b\n")
	write("renamed-from.txt", "c\n")
	write("space name.txt", "d\n")
	runGitIn(t, dir, "add", "-A")
	runGitIn(t, dir, "commit", "-qm", "init")

	// working tree changes: modify, delete, rename (staged), untracked.
	write("mod.txt", "a2\n")
	if err := os.Remove(filepath.Join(dir, "del.txt")); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "mv", "renamed-from.txt", "renamed-to.txt")
	write("space name.txt", "d2\n") // also modified
	write("unt.txt", "u\n")

	g := New(dir)
	entries, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]StatusEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}

	if e, ok := byPath["mod.txt"]; !ok || e.XY != ".M" {
		t.Errorf("mod.txt: want XY=.M, got %+v", e)
	}
	if e, ok := byPath["del.txt"]; !ok || e.XY != ".D" {
		t.Errorf("del.txt: want XY=.D, got %+v", e)
	}
	// Staged rename surfaces as staged change on the new path.
	if e, ok := byPath["renamed-to.txt"]; !ok || e.XY[0] != 'R' && e.XY[0] != 'A' {
		t.Errorf("renamed-to.txt: want staged R/A, got %+v", e)
	}
	if _, ok := byPath["renamed-from.txt"]; ok {
		t.Errorf("renamed-from.txt should not appear alongside rename record")
	}
	// Path with a space must survive intact (the whole point of -z).
	if e, ok := byPath["space name.txt"]; !ok || e.XY != ".M" {
		t.Errorf("space name.txt: want .M, got %+v", e)
	}
	if e, ok := byPath["unt.txt"]; !ok || e.XY != "??" {
		t.Errorf("unt.txt: want ??, got %+v", e)
	}
}

func TestStatusCleanTree(t *testing.T) {
	dir := t.TempDir()
	runGitIn(t, dir, "init", "-q", "--initial-branch=main")
	runGitIn(t, dir, "config", "user.email", "t@t")
	runGitIn(t, dir, "config", "user.name", "t")
	runGitIn(t, dir, "commit", "-qm", "empty", "--allow-empty")

	g := New(dir)
	entries, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("clean tree: want 0 entries, got %+v", entries)
	}
}

func TestStatusNonRepo(t *testing.T) {
	g := New(t.TempDir())
	if _, err := g.Status(context.Background()); err == nil {
		t.Error("non-repo: want error, got nil")
	}
}
