// Package agent is a thin streaming client for OpenAI-compatible
// chat-completions endpoints. Endpoints come from pi's models.json
// or from OPENAI_* environment variables; credentials never leave
// the process.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"codebrowse/internal/piconfig"
)

type Agent struct {
	endpoints map[string]piconfig.Endpoint // key: "provider/id"
	order     []string                     // stable order for listing
	deflt     string
	http      *http.Client
}

// Load builds the registry. pi's models.json is the primary source;
// explicit OPENAI_* env config is a fallback for machines without pi.
func Load() (*Agent, error) {
	a := &Agent{endpoints: map[string]piconfig.Endpoint{}, http: &http.Client{Timeout: 0}}

	if eps, err := piconfig.Load(piconfig.DefaultPath()); err == nil && len(eps) > 0 {
		for _, ep := range eps {
			a.add(ep)
		}
		a.deflt = a.order[0]
		return a, nil
	}

	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		base := os.Getenv("OPENAI_BASE_URL")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model := os.Getenv("CODEBROWSE_MODEL")
		if model == "" {
			model = "gpt-5-mini"
		}
		ep, err := piconfig.Manual("env", model, base, key)
		if err != nil {
			return nil, err
		}
		a.add(ep)
		a.deflt = ep.Key()
		return a, nil
	}

	return nil, errors.New("no usable OpenAI-compatible providers in pi config and OPENAI_API_KEY not set")
}

func (a *Agent) add(ep piconfig.Endpoint) {
	k := ep.Key()
	if _, dup := a.endpoints[k]; dup {
		return
	}
	a.endpoints[k] = ep
	a.order = append(a.order, k)
}

// ModelInfo is what the browser is allowed to see — no baseURL, no key.
type ModelInfo struct {
	Key      string `json:"key"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Ctx      int    `json:"contextWindow"`
}

func (a *Agent) Models() []ModelInfo {
	out := make([]ModelInfo, 0, len(a.order))
	for _, k := range a.order {
		e := a.endpoints[k]
		out = append(out, ModelInfo{Key: k, Provider: e.Provider, ID: e.ID, Name: e.Name, Ctx: e.Ctx})
	}
	return out
}

func (a *Agent) Default() string { return a.deflt }

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// StreamChat POSTs the conversation to the endpoint named by key
// (empty = default) and invokes onDelta for each token.
func (a *Agent) StreamChat(ctx context.Context, key string, msgs []Message, onDelta func(string)) error {
	if key == "" {
		key = a.deflt
	}
	ep, ok := a.endpoints[key]
	if !ok {
		return errors.New("unknown model")
	}
	if len(msgs) == 0 || len(msgs) > 200 {
		return errors.New("invalid message count")
	}
	for _, m := range msgs {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("invalid role %q", m.Role)
		}
		if len(m.Content) > 1<<20 {
			return errors.New("message too large")
		}
	}

	body, _ := json.Marshal(chatRequest{Model: ep.ID, Messages: msgs, Stream: true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ep.BaseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ep.APIKey())
	req.Header.Set("Accept", "text/event-stream")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, string(b))
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	go func() { <-ctx.Done(); resp.Body.Close() }()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				onDelta(c.Delta.Content)
			}
		}
	}
	return sc.Err()
}
