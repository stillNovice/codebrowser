// Package piagent embeds pi (the coding-agent harness) as a subprocess
// in RPC mode, giving the chat panel pi's tools, sessions, auth and
// model catalog instead of a hand-rolled LLM client.
//
// Protocol (docs/rpc.md): JSONL over stdin/stdout. Commands carry
// {"type": ...}; agent events stream asynchronously. One pi process
// per codebrowse server; prompts are serialized with a mutex.
package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan json.RawMessage
	stderr *ringBuf
	mu     sync.Mutex // serializes Prompt calls
	wmu    sync.Mutex // serializes stdin writes
	closed atomic.Bool
}

// ringBuf keeps the last N stderr bytes for diagnostics.
type ringBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > 8192 {
		r.buf = r.buf[len(r.buf)-8192:]
	}
	return len(p), nil
}

func (r *ringBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// Start launches `pi --mode rpc` with cwd=dir. The session persists
// (pi's own session files) so conversation continues across prompts.
// If session is non-empty it is passed to --session (path or id) to
// resume an existing session — the terminal-side pi must not be
// running against the same session at the same time.
func Start(dir, model, session string) (*Session, error) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		return nil, errors.New("pi binary not found in PATH")
	}
	args := []string{"--mode", "rpc", "--name", "codebrowse"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.Command(piBin, args...) // fixed argv, no shell
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	rb := &ringBuf{}
	cmd.Stderr = rb
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &Session{cmd: cmd, stdin: stdin, stderr: rb, lines: make(chan json.RawMessage, 256)}
	go s.readLoop(stdout)
	return s, nil
}

// readLoop frames stdout on LF only (per protocol) and feeds lines.
func (s *Session) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	sc.Split(splitLF)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		s.lines <- json.RawMessage(cp)
	}
	s.closed.Store(true)
	close(s.lines)
}

// splitLF is a bufio split function that delimits on '\n' only and
// strips a single trailing '\r'. (bufio.ScanLines also splits on
// U+2028/U+2029 via some readers; RPC forbids that.)
func splitLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := indexByte(data, '\n'); i >= 0 {
		tok := data[:i]
		if n := len(tok); n > 0 && tok[n-1] == '\r' {
			tok = tok[:n-1]
		}
		return i + 1, tok, nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// ---- wire types (only the fields we consume) ----

type event struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Error   string `json:"error"`

	// message_update
	AssistantMessageEvent *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`

	// tool_execution_*
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
	IsError  bool            `json:"isError"`

	// agent_end
	WillRetry bool `json:"willRetry"`
}

type Callbacks struct {
	OnDelta func(text string)
	OnTool  func(name, phase, summary string) // phase: start|end
}

var reqID atomic.Int64

// Prompt sends one user message and streams events until the run
// settles. ctx cancellation aborts the run server-side via "abort".
func (s *Session) Prompt(ctx context.Context, message string, cb Callbacks) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return errors.New("pi process exited: " + tail(s.stderr.String()))
	}
	if len(message) > 1<<20 {
		return errors.New("message too large")
	}

	// Drain events left over from previous turns (e.g. a trailing
	// agent_settled) so they can't be mistaken for this turn's output.
	for {
		select {
		case <-s.lines:
		default:
			goto drained
		}
	}
drained:

	id := fmt.Sprintf("cb-%d", reqID.Add(1))
	if err := s.send(map[string]any{"id": id, "type": "prompt", "message": message}); err != nil {
		return err
	}

	// Abort the run if the HTTP client goes away.
	abortDone := make(chan struct{})
	defer close(abortDone)
	go func() {
		select {
		case <-ctx.Done():
			s.send(map[string]any{"type": "abort"})
		case <-abortDone:
		}
	}()

	accepted := false
	// Safety net: if agent_settled never arrives, unblock eventually.
	timeout := time.After(5 * time.Minute)
	for {
		select {
		case <-timeout:
			s.send(map[string]any{"type": "abort"})
			return errors.New("pi run timed out")
		case raw, ok := <-s.lines:
			if !ok {
				return errors.New("pi stream closed: " + tail(s.stderr.String()))
			}
			var ev event
			if json.Unmarshal(raw, &ev) != nil {
				continue
			}
			switch ev.Type {
			case "response":
				// Command ack for our prompt (first response after send).
				if !accepted {
					if !ev.Success {
						return errors.New("prompt rejected: " + ev.Error)
					}
					accepted = true
				}
			case "message_update":
				if ame := ev.AssistantMessageEvent; ame != nil && ame.Type == "text_delta" && ame.Delta != "" {
					cb.OnDelta(ame.Delta)
				}
			case "tool_execution_start":
				cb.OnTool(ev.ToolName, "start", summarizeArgs(ev.Args))
			case "tool_execution_end":
				status := "ok"
				if ev.IsError {
					status = "error"
				}
				cb.OnTool(ev.ToolName, "end", status)
			case "agent_settled":
				// Sole terminal event. agent_end can precede trailing
				// events; returning there strands output in the channel.
				return nil
			}
		}
	}
}

// send writes one JSONL command; safe for concurrent use.
func (s *Session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// SetModel switches the session model ("provider" + "modelId").
func (s *Session) SetModel(provider, modelID string) error {
	return s.send(map[string]any{"type": "set_model", "provider": provider, "modelId": modelID})
}

// Close terminates the pi process.
func (s *Session) Close() {
	s.stdin.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.cmd.Wait()
}

// summarizeArgs renders a short, single-line preview of tool args.
func summarizeArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "path", "file", "query", "pattern"} {
		if v, ok := m[k].(string); ok && v != "" {
			v = strings.ReplaceAll(v, "\n", " ")
			if len(v) > 80 {
				v = v[:80] + "…"
			}
			return v
		}
	}
	return ""
}

// LatestSession returns the most recently modified pi session file
// for the given repo dir, using pi's session-dir naming convention
// (~/.pi/agent/sessions/--path-with-dashes--/*.jsonl).
func LatestSession(dir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	slug := "--" + strings.Trim(strings.ReplaceAll(dir, "/", "-"), "-") + "--"
	d := filepath.Join(home, ".pi", "agent", "sessions", slug)
	entries, err := os.ReadDir(d)
	if err != nil {
		return "", fmt.Errorf("no sessions for %s", dir)
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().After(bestMod) {
			continue
		}
		bestMod, best = info.ModTime(), filepath.Join(d, e.Name())
	}
	if best == "" {
		return "", fmt.Errorf("no sessions for %s", dir)
	}
	return best, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return "…" + s[len(s)-300:]
	}
	return s
}

// ChatMessage is one user/assistant text turn from a session file.
type ChatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// History reads a pi session JSONL and returns the plain text turns
// (user + assistant text only; thinking/tool calls skipped).
func History(sessionFile string) []ChatMessage {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil
	}
	var out []ChatMessage
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"type":"message"`) {
			continue
		}
		var e struct {
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Message.Role != "user" && e.Message.Role != "assistant" {
			continue
		}
		var text string
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, ChatMessage{Role: e.Message.Role, Text: text})
		}
	}
	return out
}
