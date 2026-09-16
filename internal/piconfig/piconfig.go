// Package piconfig reads pi's ~/.pi/agent/models.json and exposes
// OpenAI-compatible providers. API keys are referenced indirectly
// ("$ENV_VAR") in the file and resolved from the environment here;
// resolved keys never leave the process.
package piconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type file struct {
	Providers map[string]provider `json:"providers"`
}

type provider struct {
	BaseURL string  `json:"baseUrl"`
	API     string  `json:"api"`
	APIKey  string  `json:"apiKey"`
	Models  []model `json:"models"`
}

type model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
}

// Endpoint is one usable model on one provider.
type Endpoint struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`   // sent to the API as "model"
	Name     string `json:"name"` // display name
	Ctx      int    `json:"contextWindow"`

	baseURL string
	apiKey  string
}

func (e Endpoint) BaseURL() string { return e.baseURL }
func (e Endpoint) APIKey() string  { return e.apiKey }

// Key returns the stable registry key, "provider/id".
func (e Endpoint) Key() string { return e.Provider + "/" + e.ID }

// openaiCompat lists pi api types our chat client can speak.
var openaiCompat = map[string]bool{
	"":                   true, // pi default is openai-completions
	"openai-completions": true,
	"openai-responses":   false, // different wire format; skip
}

// DefaultPath is pi's well-known config location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "models.json")
}

// Load parses models.json at path and returns all usable endpoints.
// Missing env vars drop only the affected provider, not the whole file.
func Load(path string) ([]Endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("models.json suspiciously large")
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse models.json: %w", err)
	}

	var out []Endpoint
	for pname, p := range f.Providers {
		if !openaiCompat[p.API] || p.BaseURL == "" {
			continue
		}
		key, err := resolveKey(p.APIKey)
		if err != nil || len(p.Models) == 0 {
			continue
		}
		for _, m := range p.Models {
			if m.ID == "" {
				continue
			}
			name := m.Name
			if name == "" {
				name = m.ID
			}
			out = append(out, Endpoint{
				Provider: pname, ID: m.ID, Name: name, Ctx: m.ContextWindow,
				baseURL: strings.TrimRight(p.BaseURL, "/"), apiKey: key,
			})
		}
	}
	return out, nil
}

// Manual builds a single endpoint from explicit values (env config path).
func Manual(provider, model, baseURL, apiKey string) (Endpoint, error) {
	if provider == "" || model == "" || baseURL == "" || apiKey == "" {
		return Endpoint{}, fmt.Errorf("incomplete manual endpoint")
	}
	return Endpoint{
		Provider: provider, ID: model, Name: model,
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
	}, nil
}

// resolveKey accepts "$VAR" indirection or (legacy) a literal value.
func resolveKey(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("no apiKey")
	}
	if strings.HasPrefix(ref, "$") {
		v := os.Getenv(strings.TrimPrefix(ref, "$"))
		if v == "" {
			return "", fmt.Errorf("env var %s not set", ref)
		}
		return v, nil
	}
	return ref, nil
}
