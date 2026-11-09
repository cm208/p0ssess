package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ModelInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	Context    int      `json:"context,omitempty"`
	InPerM     *float64 `json:"usd_in_per_m,omitempty"`
	OutPerM    *float64 `json:"usd_out_per_m,omitempty"`
	Input      []string `json:"input,omitempty"`
	Output     []string `json:"output,omitempty"`
	Tools      bool     `json:"tools"`
	SourceDate string   `json:"as_of,omitempty"`
}

type ModelsRequest struct {
	Query string `json:"query"`
	Live  bool   `json:"live"`
	Limit int    `json:"limit"`
}

func (e *Engine) registryFile() string {
	if p := os.Getenv("P0SSESS_REGISTRY"); p != "" {
		return p
	}
	if e.cfg.RegistryFile != "" {
		return e.cfg.RegistryFile
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			p := filepath.Join(filepath.Dir(real), "..", "registry", "models.json")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// ResolveModelID turns the caller's model string into an exact OpenRouter id. An exact id (anything with a
// "/", optionally with an "openrouter/" prefix or a "#variant") is accepted as given, since the registry
// snapshot may lag OpenRouter. A bare name is matched against the registry: it resolves only when exactly
// one model matches, and otherwise returns the candidates rather than guessing.
func (e *Engine) ResolveModelID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, nil
	}
	if strings.Contains(strings.TrimPrefix(s, "openrouter/"), "/") {
		return s, nil // already an exact id
	}
	ids, err := e.registryIDs()
	if err != nil {
		return "", fmt.Errorf("%q is not an exact OpenRouter id (vendor/model) and the registry is unavailable to resolve it: %w", s, err)
	}
	term := strings.ToLower(s)
	var exact, contains []string
	for _, id := range ids {
		low := strings.ToLower(id)
		_, model, _ := strings.Cut(low, "/")
		switch {
		case low == term || model == term:
			exact = append(exact, id)
		case strings.Contains(low, term):
			contains = append(contains, id)
		}
	}
	cand := exact
	if len(cand) == 0 {
		cand = contains
	}
	switch len(cand) {
	case 1:
		return cand[0], nil
	case 0:
		return "", fmt.Errorf("no model matches %q; pass an exact OpenRouter id like deepseek/deepseek-v4.1-flash", s)
	default:
		sort.Strings(cand)
		if len(cand) > 12 {
			cand = cand[:12]
		}
		return "", fmt.Errorf("%q matches %d models; pass one exactly: %s", s, len(cand), strings.Join(cand, ", "))
	}
}

func (e *Engine) registryIDs() ([]string, error) {
	p := e.registryFile()
	if p == "" {
		return nil, fmt.Errorf("no registry snapshot found")
	}
	list, _, err := snapshotModels(p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func (e *Engine) Models(ctx context.Context, req ModelsRequest) ([]ModelInfo, string, error) {
	var list []ModelInfo
	var source string
	var err error
	if req.Live {
		list, err = e.liveModels(ctx)
		source = "live https://openrouter.ai/api/v1/models, " + time.Now().UTC().Format("2006-01-02")
	} else {
		p := e.registryFile()
		if p == "" {
			return nil, "", fmt.Errorf("no registry snapshot found; pass live: true")
		}
		list, source, err = snapshotModels(p)
	}
	if err != nil {
		return nil, "", err
	}
	terms := strings.Fields(strings.ToLower(req.Query))
	out := []ModelInfo{}
	for _, m := range list {
		hay := strings.ToLower(m.ID + " " + m.Name + " " + strings.Join(m.Input, " ") + " " + strings.Join(m.Output, " "))
		match := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				match = false
				break
			}
		}
		if match {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	limit := req.Limit
	if limit <= 0 {
		limit = 40
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, source, nil
}

func snapshotModels(path string) ([]ModelInfo, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var doc struct {
		Meta struct {
			AsOf string `json:"as_of"`
		} `json:"meta"`
		Models []struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Context int      `json:"context_length"`
			Input   []string `json:"input_modalities"`
			Output  []string `json:"output_modalities"`
			Pricing struct {
				In  *float64 `json:"input_per_m"`
				Out *float64 `json:"output_per_m"`
			} `json:"pricing"`
			Caps struct {
				Tools bool `json:"tools"`
			} `json:"caps"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, "", fmt.Errorf("registry %s: %w", path, err)
	}
	var out []ModelInfo
	for _, m := range doc.Models {
		out = append(out, ModelInfo{ID: m.ID, Name: m.Name, Context: m.Context, InPerM: m.Pricing.In, OutPerM: m.Pricing.Out,
			Input: m.Input, Output: m.Output, Tools: m.Caps.Tools, SourceDate: doc.Meta.AsOf})
	}
	return out, "registry snapshot " + path + " (as of " + doc.Meta.AsOf + ")", nil
}

func (e *Engine) liveModels(ctx context.Context) ([]ModelInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://openrouter.ai/api/v1/models", nil)
	if key, err := e.cfg.OpenRouterKey(); err == nil {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openrouter models: HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Context int    `json:"context_length"`
			Arch    struct {
				In  []string `json:"input_modalities"`
				Out []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			Params []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	perM := func(s string) *float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < 0 {
			return nil
		}
		v *= 1e6
		return &v
	}
	today := time.Now().UTC().Format("2006-01-02")
	var out []ModelInfo
	for _, m := range doc.Data {
		tools := false
		for _, p := range m.Params {
			if p == "tools" {
				tools = true
			}
		}
		out = append(out, ModelInfo{ID: m.ID, Name: m.Name, Context: m.Context, InPerM: perM(m.Pricing.Prompt),
			OutPerM: perM(m.Pricing.Completion), Input: m.Arch.In, Output: m.Arch.Out, Tools: tools, SourceDate: today})
	}
	return out, nil
}
