// Package catalog keeps OpenRouter's public model list (GET /api/v1/models, no key needed) in the cache
// for about a day, so any model OpenRouter lists can be named, priced, approved and run, including ids
// newer than the signed route table. When the list cannot be fetched the last cached copy is used, and
// with none the route table's own models are all that is known.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
)

// DefaultURL is OpenRouter's public model list; output_modalities=all includes image, audio and video
// models.
const DefaultURL = "https://openrouter.ai/api/v1/models?output_modalities=all"

// MaxAge is how long a fetched list is used before it is fetched again.
const MaxAge = 24 * time.Hour

// MissRetry is how soon a model missing from a cached list makes the list be fetched again.
const MissRetry = time.Hour

// FileName is the cache file inside the cache directory.
const FileName = "openrouter-models.json"

// URL is where the list is fetched from: $P0SSESS_MODELS_URL, else DefaultURL. "off" turns fetching
// off. Tests never fetch unless they set it.
func URL() string {
	v := strings.TrimSpace(os.Getenv("P0SSESS_MODELS_URL"))
	switch {
	case v == "off":
		return ""
	case v != "":
		return v
	case testing.Testing():
		return ""
	}
	return DefaultURL
}

// Model is one entry of OpenRouter's list, reduced to what p0ssess uses.
type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Context int      `json:"context_length,omitempty"`
	Arch    Arch     `json:"architecture"`
	Pricing Prices   `json:"pricing"`
	Top     Top      `json:"top_provider"`
	Params  []string `json:"supported_parameters,omitempty"`
	Routes  []string `json:"endpoints,omitempty"`
	Created int64    `json:"created,omitempty"`
}

type Arch struct {
	Input  []string `json:"input_modalities"`
	Output []string `json:"output_modalities"`
}

// Prices are OpenRouter's per-token (per-request, per-search) prices, as the decimal strings it sends.
type Prices struct {
	Prompt     string     `json:"prompt,omitempty"`
	Completion string     `json:"completion,omitempty"`
	CacheRead  string     `json:"input_cache_read,omitempty"`
	Reasoning  string     `json:"internal_reasoning,omitempty"`
	Audio      string     `json:"audio,omitempty"`
	WebSearch  string     `json:"web_search,omitempty"`
	Image      string     `json:"image,omitempty"`
	Overrides  []Override `json:"overrides,omitempty"`
}

type Override struct {
	MinPromptTokens int    `json:"min_prompt_tokens"`
	Prompt          string `json:"prompt,omitempty"`
	Completion      string `json:"completion,omitempty"`
	CacheRead       string `json:"input_cache_read,omitempty"`
}

type Top struct {
	Context       int `json:"context_length,omitempty"`
	MaxCompletion int `json:"max_completion_tokens,omitempty"`
}

type file struct {
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"`
	Models    []Model   `json:"models"`
}

// Store is the cached list in one cache directory.
type Store struct {
	Dir  string
	URL  string
	HTTP *http.Client
	Now  func() time.Time

	mu      sync.Mutex
	loaded  bool
	cur     file
	stamp   time.Time
	byID    map[string]*Model
	triedAt time.Time
	asTable []table.Model
}

// Open returns the store for dir, fetching from URL().
func Open(dir string) *Store {
	return &Store{Dir: dir, URL: URL(), HTTP: &http.Client{Timeout: 10 * time.Second}, Now: time.Now}
}

func (s *Store) path() string { return filepath.Join(s.Dir, FileName) }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// load reads the cache file when it changed since the last read. Callers hold mu.
func (s *Store) load() {
	st, err := os.Stat(s.path())
	if err != nil {
		if !s.loaded {
			s.loaded, s.byID = true, map[string]*Model{}
		}
		return
	}
	if s.loaded && st.ModTime().Equal(s.stamp) {
		return
	}
	var f file
	b, err := os.ReadFile(s.path())
	if err != nil || json.Unmarshal(b, &f) != nil {
		s.loaded, s.byID = true, map[string]*Model{}
		return
	}
	s.setLocked(f, st.ModTime())
}

func (s *Store) setLocked(f file, stamp time.Time) {
	s.cur, s.stamp, s.loaded, s.asTable = f, stamp, true, nil
	s.byID = make(map[string]*Model, len(f.Models))
	for i := range f.Models {
		s.byID[f.Models[i].ID] = &f.Models[i]
	}
}

// FetchedAt is when the cached list was fetched, or zero.
func (s *Store) FetchedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.cur.FetchedAt
}

// Due reports whether the list should be fetched again.
func (s *Store) Due() bool {
	t := s.FetchedAt()
	return t.IsZero() || s.now().Sub(t) >= MaxAge
}

// Refresh fetches the list when it is due (or always, with force) and stores it. A failed fetch keeps
// the cached list.
func (s *Store) Refresh(ctx context.Context, force bool) error {
	if s == nil || s.URL == "" {
		return errors.New("fetching the model list is off")
	}
	if !force && !s.Due() {
		return nil
	}
	s.mu.Lock()
	s.triedAt = s.now()
	s.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openrouter models: HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("openrouter models: %w", err)
	}
	var keep []Model
	for _, m := range doc.Data {
		if m.ID != "" && strings.Contains(m.ID, "/") {
			keep = append(keep, m)
		}
	}
	if len(keep) == 0 {
		return errors.New("openrouter models: the list is empty")
	}
	f := file{FetchedAt: s.now().UTC(), Source: s.URL, Models: keep}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := paths.WriteFileAtomic(s.path(), b, 0o600); err != nil {
		return err
	}
	st, _ := os.Stat(s.path())
	s.mu.Lock()
	stamp := s.now()
	if st != nil {
		stamp = st.ModTime()
	}
	s.setLocked(f, stamp)
	s.mu.Unlock()
	return nil
}

// Get returns a listed model from the cache, or nil.
func (s *Store) Get(id string) *Model {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.byID[id]
}

// Find returns a listed model, fetching the list first when it is due or, for an id it lacks, when the
// last try was more than MissRetry ago. It never blocks longer than the store's HTTP timeout.
func (s *Store) Find(ctx context.Context, id string) *Model {
	if s == nil || id == "" {
		return nil
	}
	if m := s.Get(id); m != nil && !s.Due() {
		return m
	}
	s.mu.Lock()
	recent := !s.triedAt.IsZero() && s.now().Sub(s.triedAt) < MissRetry
	s.mu.Unlock()
	if !recent && s.URL != "" {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		s.Refresh(cctx, s.Get(id) == nil)
		cancel()
	}
	return s.Get(id)
}

// Table returns the cached list as route-table models.
func (s *Store) Table() []table.Model {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.asTable == nil {
		s.asTable = make([]table.Model, 0, len(s.cur.Models))
		for i := range s.cur.Models {
			s.asTable = append(s.asTable, s.cur.Models[i].Table())
		}
	}
	return s.asTable
}

// Stamp changes whenever the cached list does.
func (s *Store) Stamp() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.stamp
}

// perM turns a per-token decimal string into dollars per million tokens; nil when absent or negative
// (OpenRouter lists "-1" for a variable price).
func perM(v string) *float64 {
	f, ok := raw(v)
	if !ok {
		return nil
	}
	f = math.Round(f*1e6*1e9) / 1e9
	return &f
}

func raw(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f != f {
		return 0, false
	}
	return f, true
}

// Table is the model as the route table describes one. It is Live: its quality is not calibrated.
func (m Model) Table() table.Model {
	author, _, _ := strings.Cut(m.ID, "/")
	t := table.Model{ID: m.ID, Author: author, Kind: kind(m.Arch), InputMods: m.Arch.Input, OutputMods: m.Arch.Output,
		Routes: m.Routes, Live: true, Name: m.Name}
	if len(t.Routes) == 0 && has(m.Arch.Output, "text") {
		t.Routes = []string{"/api/v1/chat/completions"}
	}
	if c := max(m.Context, m.Top.Context); c > 0 {
		t.Context = &c
	}
	if m.Top.MaxCompletion > 0 {
		n := m.Top.MaxCompletion
		t.MaxOutput = &n
	}
	p := m.Pricing
	t.Pricing.InputPerM, t.Pricing.OutputPerM = perM(p.Prompt), perM(p.Completion)
	t.Pricing.CacheReadPerM, t.Pricing.ReasoningPerM = perM(p.CacheRead), perM(p.Reasoning)
	t.Pricing.AudioInPerM = perM(p.Audio)
	if f, ok := raw(p.WebSearch); ok {
		t.Pricing.WebSearchUSD = &f
	}
	for _, o := range p.Overrides {
		in, out := perM(o.Prompt), perM(o.Completion)
		if in == nil || out == nil {
			continue
		}
		t.Pricing.LongContext = append(t.Pricing.LongContext, table.Override{MinPromptTokens: o.MinPromptTokens, PromptPerM: *in,
			CompletionPerM: *out, CacheReadPerM: perM(o.CacheRead)})
	}
	return t
}

func kind(a Arch) string {
	switch {
	case has(a.Output, "text") && has(a.Output, "image"):
		return "text+image"
	case has(a.Output, "image"):
		return "image"
	case has(a.Output, "video"):
		return "video"
	case has(a.Output, "audio"):
		return "speech"
	case has(a.Output, "embeddings"):
		return "embeddings"
	case has(a.Output, "transcription"):
		return "transcription"
	}
	return "text"
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
