// Package comments stores diff review comments as JSONL inside the
// repository, so a terminal-side agent (pi) can read and act on them.
// File-based shared state: no process coupling, survives restarts.
package comments

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Comment struct {
	ID      string `json:"id"`
	File    string `json:"file"`              // repo-relative path or commit ref context
	Line    string `json:"line"`              // the diff line content that was clicked
	LineNo  int    `json:"lineNo"`            // new-side line number when known, else 0
	LineEnd int    `json:"lineEnd"`           // end of selected range; 0 or == LineNo = single line
	Ref     string `json:"ref"`               // "worktree", "staged", or commit hash
	Text    string `json:"text"`              // the reviewer's comment
	Kind    string `json:"kind,omitempty"`    // "" or "ask" (agent acts) | "note" (pure annotation)
	Working bool   `json:"working,omitempty"` // pi has picked it up, fix in flight
	Reply   string `json:"reply,omitempty"`   // pi's one-line summary when marking done
	Done    bool   `json:"done"`              // marked addressed (by pi or the user)
	Pushed  bool   `json:"pushed"`            // pushed to the live terminal pi session
	Created string `json:"created"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func Open(root string) *Store {
	return &Store{path: filepath.Join(root, CommentsFile)}
}

// Path exposes the store location (used in prompts/docs for the agent).
func (s *Store) Path() string { return s.path }

// CommentsFile is the repo-relative name of the shared comment queue.
const CommentsFile = ".codebrowse-review.jsonl"

func (s *Store) List() ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]Comment, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Comment
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c Comment
		if json.Unmarshal([]byte(line), &c) == nil && c.ID != "" {
			out = append(out, c)
		}
	}
	return out, sc.Err()
}

const maxText = 4096

func (s *Store) Add(c Comment) (Comment, error) {
	if c.File == "" || strings.ContainsRune(c.File, 0) || len(c.Text) == 0 || len(c.Text) > maxText {
		return Comment{}, fmt.Errorf("invalid comment")
	}
	if len(c.Line) > 1024 {
		c.Line = c.Line[:1024]
	}
	c.ID = fmt.Sprintf("c%d", time.Now().UnixNano())
	c.Created = time.Now().UTC().Format(time.RFC3339)

	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Comment{}, err
	}
	defer f.Close()
	b, _ := json.Marshal(c)
	_, err = f.Write(append(b, '\n'))
	return c, err
}

// SetDone marks a comment addressed; Delete removes it. Both rewrite
// the file (small by design — review lists are short).
func (s *Store) update(id string, fn func(*Comment) bool) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("bad id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.listLocked()
	if err != nil {
		return err
	}
	found := false
	kept := all[:0]
	for i := range all {
		if all[i].ID == id {
			found = true
			if fn(&all[i]) {
				kept = append(kept, all[i])
			}
			continue
		}
		kept = append(kept, all[i])
	}
	if !found {
		return fmt.Errorf("not found")
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	for _, c := range kept {
		b, _ := json.Marshal(c)
		if _, err := f.Write(append(b, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) SetDone(id string, done bool) error {
	return s.update(id, func(c *Comment) bool { c.Done = done; return true })
}

// SetPushed marks a comment pushed and mirrors it to the global pi
// inbox (~/.pi/agent/codebrowse-inbox.jsonl) so any terminal session
// whose cwd is inside this repo picks it up live — regardless of
// which directory pi was started in.
func (s *Store) SetPushed(id string) error {
	if err := s.update(id, func(c *Comment) bool { c.Pushed = true; return true }); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.listLocked()
	if err != nil {
		return nil // pushed flag saved; inbox mirror is best-effort
	}
	var target *Comment
	for i := range all {
		if all[i].ID == id {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	inbox := filepath.Join(home, ".pi", "agent", "codebrowse-inbox.jsonl")
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		return nil
	}
	type inboxEntry struct {
		Comment
		Repo string `json:"repo"`
	}
	f, err := os.OpenFile(inbox, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()
	repo := filepath.Dir(s.path) // store lives at <repo>/.codebrowse-review.jsonl
	b, _ := json.Marshal(inboxEntry{*target, repo})
	f.Write(append(b, '\n'))
	return nil
}

func (s *Store) Delete(id string) error {
	return s.update(id, func(c *Comment) bool { return false })
}
