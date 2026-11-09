package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"p0ssess/internal/pme/paths"
)

// LiveURL is OpenRouter's public model list. It needs no key.
const LiveURL = "https://openrouter.ai/api/v1/models?output_modalities=all"

// LiveMaxAge is how long a cached live list is used before refetching.
const LiveMaxAge = 24 * time.Hour

type liveModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength *int   `json:"context_length"`
	Architecture  struct {
		Input  []string `json:"input_modalities"`
		Output []string `json:"output_modalities"`
	} `json:"architecture"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
		CacheRead  string `json:"input_cache_read"`
		Overrides  []struct {
			MinPromptTokens int    `json:"min_prompt_tokens"`
			Prompt          string `json:"prompt"`
			Completion      string `json:"completion"`
			CacheRead       string `json:"input_cache_read"`
		} `json:"overrides"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletion *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Supported []string `json:"supported_parameters"`
	Endpoints []string `json:"endpoints"`
}

type liveFile struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Source    string      `json:"source"`
	Models    []liveModel `json:"models"`
}

// Refresh fetches the live list unless the cache is younger than maxAge, and
// writes the cache. It strips everything except the fields the engine uses;
// in particular the third-party benchmarks object is never stored.
func Refresh(ctx context.Context, client *http.Client, cachePath string, maxAge time.Duration, url string) (fetched bool, err error) {
	if st, err := os.Stat(cachePath); err == nil && time.Since(st.ModTime()) < maxAge {
		return false, nil
	}
	if url == "" {
		url = LiveURL
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "p0ssess")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("openrouter models: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return false, err
	}
	var raw struct {
		Data []liveModel `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, fmt.Errorf("openrouter models: %w", err)
	}
	if len(raw.Data) == 0 {
		return false, errors.New("openrouter models: empty list")
	}
	out, err := json.Marshal(liveFile{FetchedAt: time.Now().UTC(), Source: url, Models: raw.Data})
	if err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(cachePath, out, 0o600)
}

// MergeLive overlays a cached live list onto the snapshot: prices, context,
// modalities and capability flags are updated for known models, and models
// the snapshot lacks are added with Live set (no endpoint, uptime or
// data-policy facts). It is a no-op when the cache is missing.
func (r *Registry) MergeLive(cachePath string) error {
	b, err := os.ReadFile(cachePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var lf liveFile
	if err := json.Unmarshal(b, &lf); err != nil {
		return fmt.Errorf("live cache: %w", err)
	}
	r.LiveAsOf = lf.FetchedAt.Format(time.RFC3339)
	r.LiveCount = len(lf.Models)
	for _, lm := range lf.Models {
		if lm.ID == "" {
			continue
		}
		m := r.Models[lm.ID]
		if m == nil {
			m = &Model{ID: lm.ID, Name: lm.Name, Live: true, Src: "openrouter live list", Kind: kindOf(lm.Architecture.Output)}
			r.Models[lm.ID] = m
		}
		m.AsOf = r.LiveAsOf
		if len(lm.Architecture.Input) > 0 {
			m.InputMods = lm.Architecture.Input
		}
		if len(lm.Architecture.Output) > 0 {
			m.OutputMods = lm.Architecture.Output
		}
		if len(lm.Endpoints) > 0 {
			m.Routes = lm.Endpoints
		}
		if lm.ContextLength != nil && *lm.ContextLength > 0 {
			m.Context = lm.ContextLength
		}
		if lm.TopProvider.MaxCompletion != nil {
			m.MaxOutput = lm.TopProvider.MaxCompletion
		}
		if in, ok := perM(lm.Pricing.Prompt); ok {
			out, ok2 := perM(lm.Pricing.Completion)
			// A zero live price never replaces a snapshot price: zero there means "not listed" as often as free.
			zero := in == 0 && out == 0
			had := m.Pricing.InputPerM != nil && m.Pricing.OutputPerM != nil
			if ok2 && (!zero || (!had && m.Kind == "text")) {
				m.Pricing.InputPerM, m.Pricing.OutputPerM, m.Pricing.FromLive = &in, &out, true
				if cr, ok := perM(lm.Pricing.CacheRead); ok {
					m.Pricing.CacheReadPerM = &cr
				}
			}
		}
		if len(lm.Pricing.Overrides) > 0 {
			var ovs []Override
			for _, o := range lm.Pricing.Overrides {
				p, ok1 := perM(o.Prompt)
				c, ok2 := perM(o.Completion)
				if !ok1 || !ok2 {
					continue
				}
				ov := Override{MinPromptTokens: o.MinPromptTokens, PromptPerM: p, CompletionPerM: c}
				if cr, ok := perM(o.CacheRead); ok {
					ov.CacheReadPerM = &cr
				}
				ovs = append(ovs, ov)
			}
			m.Pricing.LongContext = ovs
		}
		if len(lm.Supported) > 0 {
			m.Caps.Tools = has(lm.Supported, "tools")
			m.Caps.ToolChoice = has(lm.Supported, "tool_choice")
			m.Caps.StructuredOutputs = has(lm.Supported, "structured_outputs")
			m.Caps.JSONMode = has(lm.Supported, "response_format")
			m.Caps.Reasoning = has(lm.Supported, "reasoning")
		}
	}
	return nil
}

func perM(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * 1e6, true
}

func kindOf(out []string) string {
	switch {
	case has(out, "video"):
		return "video"
	case has(out, "image") && has(out, "text"):
		return "text+image"
	case has(out, "image"):
		return "image"
	case has(out, "audio"):
		return "audio"
	case has(out, "speech"):
		return "speech"
	case has(out, "transcription"):
		return "transcription"
	case has(out, "embeddings"):
		return "embeddings"
	case has(out, "rerank"):
		return "rerank"
	}
	return "text"
}
