// Package registry loads the model registry: prices, capabilities, endpoint
// summaries and data policy, from the shipped snapshot and an optional live
// refresh of OpenRouter's public model list.
package registry

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

type Caps struct {
	Tools             bool     `json:"tools"`
	ToolChoice        bool     `json:"tool_choice"`
	StructuredOutputs bool     `json:"structured_outputs"`
	JSONMode          bool     `json:"json_mode"`
	Reasoning         bool     `json:"reasoning"`
	ReasoningRequired *bool    `json:"reasoning_mandatory"`
	ReasoningEfforts  []string `json:"reasoning_efforts"`
	WebSearch         bool     `json:"web_search_options"`
	Batch             bool     `json:"batch_api"`
}

type Override struct {
	MinPromptTokens int      `json:"min_prompt_tokens"`
	PromptPerM      float64  `json:"prompt_per_m"`
	CompletionPerM  float64  `json:"completion_per_m"`
	CacheReadPerM   *float64 `json:"input_cache_read_per_m"`
}

// Pricing is in US dollars. *_per_m is per million tokens. Pointers are nil
// when the source has no value.
type Pricing struct {
	InputPerM       *float64           `json:"input_per_m"`
	OutputPerM      *float64           `json:"output_per_m"`
	CacheReadPerM   *float64           `json:"cache_read_per_m"`
	ReasoningPerM   *float64           `json:"reasoning_per_m"`
	AudioInPerM     *float64           `json:"audio_in_per_m"`
	WebSearchUSD    *float64           `json:"web_search_usd"`
	ImageOutRaw     *float64           `json:"image_out_raw"`
	PromptRaw       *float64           `json:"prompt_usd_raw"`
	CompletionRaw   *float64           `json:"completion_usd_raw"`
	USDPerSecondMin *float64           `json:"usd_per_second_min"`
	USDPerSecondMax *float64           `json:"usd_per_second_max"`
	VideoSKUs       map[string]float64 `json:"video_pricing_skus_raw"`
	UnitNote        string             `json:"unit_note"`
	LongContext     []Override         `json:"long_context_overrides"`
	// FromLive marks token prices that came from the live list rather than the snapshot.
	FromLive bool `json:"-"`
}

type Endpoint struct {
	Provider    string   `json:"provider"`
	Tag         string   `json:"tag"`
	InputPerM   *float64 `json:"input_per_m"`
	OutputPerM  *float64 `json:"output_per_m"`
	CacheRead   *float64 `json:"cache_read_per_m"`
	Context     int      `json:"context_length"`
	DataPolicy  string   `json:"data_policy"`
	UptimeLast1 *float64 `json:"uptime_last_1d"`
}

type Endpoints struct {
	ProviderCount  int            `json:"provider_count"`
	Providers      []string       `json:"providers"`
	UptimeBest     *float64       `json:"uptime_1d_best"`
	UptimeWorst    *float64       `json:"uptime_1d_worst"`
	PolicyCounts   map[string]int `json:"data_policy_counts"`
	BestDataPolicy string         `json:"best_data_policy"`
	HasZDR         bool           `json:"has_zdr_endpoint"`
	Cheapest       *Endpoint      `json:"cheapest"`
	CheapestZDR    *Endpoint      `json:"cheapest_zdr"`
	AsOf           string         `json:"as_of"`
}

type Model struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Author       string     `json:"author"`
	Kind         string     `json:"kind"`
	InputMods    []string   `json:"input_modalities"`
	OutputMods   []string   `json:"output_modalities"`
	Routes       []string   `json:"api_routes"`
	Context      *int       `json:"context_length"`
	MaxOutput    *int       `json:"max_output_tokens"`
	Caps         Caps       `json:"caps"`
	Pricing      Pricing    `json:"pricing"`
	Endpoints    *Endpoints `json:"endpoints"`
	ReferencedBy []string   `json:"referenced_by"`
	Src          string     `json:"src"`
	AsOf         string     `json:"as_of"`
	// Live is true for models known only from the live refresh.
	Live bool `json:"live,omitempty"`
}

// Data policy tiers, best first.
const (
	PolicyZDR             = "zdr"
	PolicySafetyRetention = "safety_retention"
	PolicyNotListed       = "not_in_zdr_list"
)

type Registry struct {
	AsOf      string
	Models    map[string]*Model
	LiveAsOf  string
	LiveCount int
}

type file struct {
	Meta struct {
		AsOf string `json:"as_of"`
	} `json:"meta"`
	Models []*Model `json:"models"`
}

// Load reads registry/models.json from fsys.
func Load(fsys fs.FS) (*Registry, error) {
	b, err := fs.ReadFile(fsys, "registry/models.json")
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Registry, error) {
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	r := &Registry{AsOf: f.Meta.AsOf, Models: map[string]*Model{}}
	for _, m := range f.Models {
		if m.ID == "" {
			continue
		}
		r.Models[m.ID] = m
	}
	return r, nil
}

func (r *Registry) Get(id string) *Model { return r.Models[id] }

// IDs returns model ids in sorted order.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.Models))
	for id := range r.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *Model) HasInput(mod string) bool  { return has(m.InputMods, mod) }
func (m *Model) HasOutput(mod string) bool { return has(m.OutputMods, mod) }

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (m *Model) IsClaude() bool { return strings.HasPrefix(m.ID, "anthropic/") }

// Free reports a model that costs nothing: a ":free" variant, or a text model whose shipped snapshot
// lists zero token prices. A zero from anywhere else (the live list, an endpoint, or a media model
// billed by the image, second or character) means the price is unknown, not free.
func (m *Model) Free() bool {
	if strings.HasSuffix(m.ID, ":free") {
		return true
	}
	p := m.Pricing
	return m.Kind == "text" && !m.Live && !p.FromLive && p.InputPerM != nil && p.OutputPerM != nil &&
		*p.InputPerM == 0 && *p.OutputPerM == 0
}

// PriceKnown reports whether any price is known for the model: a token price, a media price, or free.
func (m *Model) PriceKnown() bool {
	if m == nil {
		return false
	}
	if m.Free() {
		return true
	}
	p := m.Pricing
	for _, v := range []*float64{p.InputPerM, p.OutputPerM, p.ImageOutRaw, p.PromptRaw, p.CompletionRaw, p.USDPerSecondMin, p.USDPerSecondMax} {
		if v != nil && *v > 0 {
			return true
		}
	}
	for _, v := range p.VideoSKUs {
		if v > 0 {
			return true
		}
	}
	return false
}

// Batch reports a :batch variant (half price, never zero-retention).
func (m *Model) Batch() bool { return strings.HasSuffix(m.ID, ":batch") }

// ContextLen is the model's context window, or 0 when unknown.
func (m *Model) ContextLen() int {
	if m.Context == nil {
		return 0
	}
	return *m.Context
}

// SingleProvider reports whether one provider serves the model.
func (m *Model) SingleProvider() bool {
	return m.Endpoints != nil && m.Endpoints.ProviderCount == 1
}

// Uptime is the best endpoint's one-day uptime in [0,1], or -1 when unknown.
func (m *Model) Uptime() float64 {
	if m.Endpoints == nil || m.Endpoints.UptimeBest == nil {
		return -1
	}
	return *m.Endpoints.UptimeBest / 100
}

// Policy is the best data-policy tier among the model's endpoints.
func (m *Model) Policy() string {
	if m.Endpoints == nil || m.Endpoints.BestDataPolicy == "" {
		return ""
	}
	return m.Endpoints.BestDataPolicy
}

// TokenPrices are per-million-token prices for one way of calling a model.
type TokenPrices struct {
	In, Out, CacheRead float64
	Source             string
}

// Prices returns the token prices to use. With zdr set, only a ZDR endpoint
// counts. cheapest picks the cheapest qualifying endpoint instead of the
// model-level list price. ok is false when no token price is known.
func (m *Model) Prices(zdr, cheapest bool) (TokenPrices, bool) {
	if zdr {
		if m.Endpoints == nil || m.Endpoints.CheapestZDR == nil {
			return TokenPrices{}, false
		}
		return m.endpointPrices(m.Endpoints.CheapestZDR, "cheapest zero-retention endpoint ("+m.Endpoints.CheapestZDR.Provider+")")
	}
	list, okList := m.listPrices()
	if cheapest && m.Endpoints != nil && m.Endpoints.Cheapest != nil {
		if ep, ok := m.endpointPrices(m.Endpoints.Cheapest, "cheapest endpoint ("+m.Endpoints.Cheapest.Provider+")"); ok {
			if !okList || blend(ep) < blend(list) {
				return ep, true
			}
		}
	}
	return list, okList
}

func blend(p TokenPrices) float64 { return (3*p.In + p.Out) / 4 }

func (m *Model) listPrices() (TokenPrices, bool) {
	p := m.Pricing
	if p.InputPerM == nil || p.OutputPerM == nil {
		return TokenPrices{}, false
	}
	if *p.InputPerM == 0 && *p.OutputPerM == 0 && !m.Free() {
		return TokenPrices{}, false
	}
	tp := TokenPrices{In: *p.InputPerM, Out: *p.OutputPerM, CacheRead: *p.InputPerM, Source: "list price"}
	if p.CacheReadPerM != nil {
		tp.CacheRead = *p.CacheReadPerM
	}
	return tp, true
}

func (m *Model) endpointPrices(e *Endpoint, src string) (TokenPrices, bool) {
	if e.InputPerM == nil || e.OutputPerM == nil || (*e.InputPerM == 0 && *e.OutputPerM == 0 && !m.Free()) {
		return TokenPrices{}, false
	}
	tp := TokenPrices{In: *e.InputPerM, Out: *e.OutputPerM, CacheRead: *e.InputPerM, Source: src}
	if e.CacheRead != nil {
		tp.CacheRead = *e.CacheRead
	}
	return tp, true
}

// ForPrompt applies long-context overrides for a prompt of n tokens.
func (m *Model) ForPrompt(tp TokenPrices, n int) TokenPrices {
	best := -1
	for i, o := range m.Pricing.LongContext {
		if n > o.MinPromptTokens && (best < 0 || o.MinPromptTokens > m.Pricing.LongContext[best].MinPromptTokens) {
			best = i
		}
	}
	if best < 0 {
		return tp
	}
	o := m.Pricing.LongContext[best]
	// Overrides are list-price multipliers; scale an endpoint price the same way.
	if m.Pricing.InputPerM != nil && *m.Pricing.InputPerM > 0 {
		f := o.PromptPerM / *m.Pricing.InputPerM
		tp.In *= f
		tp.CacheRead *= f
	}
	if m.Pricing.OutputPerM != nil && *m.Pricing.OutputPerM > 0 {
		tp.Out *= o.CompletionPerM / *m.Pricing.OutputPerM
	}
	tp.Source += fmt.Sprintf(", over-%dk tier", o.MinPromptTokens/1000)
	return tp
}
