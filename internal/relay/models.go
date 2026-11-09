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
	q := strings.ToLower(strings.TrimSpace(req.Query))
	var out []ModelInfo
	for _, m := range list {
		if q == "" || strings.Contains(strings.ToLower(m.ID+" "+m.Name+" "+strings.Join(m.Input, " ")+" "+strings.Join(m.Output, " ")), q) {
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
