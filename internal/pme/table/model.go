package table

import (
	"fmt"
	"strings"
)

type Override struct {
	MinPromptTokens int      `json:"min_prompt_tokens"`
	PromptPerM      float64  `json:"prompt_per_m"`
	CompletionPerM  float64  `json:"completion_per_m"`
	CacheReadPerM   *float64 `json:"input_cache_read_per_m,omitempty"`
}

// Pricing is in US dollars; *_per_m is per million tokens. Pointers are nil when no value is known.
type Pricing struct {
	InputPerM       *float64           `json:"input_per_m,omitempty"`
	OutputPerM      *float64           `json:"output_per_m,omitempty"`
	CacheReadPerM   *float64           `json:"cache_read_per_m,omitempty"`
	ReasoningPerM   *float64           `json:"reasoning_per_m,omitempty"`
	AudioInPerM     *float64           `json:"audio_in_per_m,omitempty"`
	WebSearchUSD    *float64           `json:"web_search_usd,omitempty"`
	ImageOutRaw     *float64           `json:"image_out_raw,omitempty"`
	PromptRaw       *float64           `json:"prompt_usd_raw,omitempty"`
	CompletionRaw   *float64           `json:"completion_usd_raw,omitempty"`
	USDPerSecondMin *float64           `json:"usd_per_second_min,omitempty"`
	USDPerSecondMax *float64           `json:"usd_per_second_max,omitempty"`
	VideoSKUs       map[string]float64 `json:"video_pricing_skus_raw,omitempty"`
	UnitNote        string             `json:"unit_note,omitempty"`
	LongContext     []Override         `json:"long_context_overrides,omitempty"`
}

type Endpoint struct {
	Provider    string   `json:"provider"`
	Tag         string   `json:"tag,omitempty"`
	InputPerM   *float64 `json:"input_per_m,omitempty"`
	OutputPerM  *float64 `json:"output_per_m,omitempty"`
	CacheRead   *float64 `json:"cache_read_per_m,omitempty"`
	Context     int      `json:"context_length,omitempty"`
	DataPolicy  string   `json:"data_policy,omitempty"`
	UptimeLast1 *float64 `json:"uptime_last_1d,omitempty"`
}

type Endpoints struct {
	ProviderCount  int       `json:"provider_count"`
	Providers      []string  `json:"providers"`
	UptimeBest     *float64  `json:"uptime_1d_best,omitempty"`
	BestDataPolicy string    `json:"best_data_policy,omitempty"`
	HasZDR         bool      `json:"has_zdr_endpoint"`
	Cheapest       *Endpoint `json:"cheapest,omitempty"`
	CheapestZDR    *Endpoint `json:"cheapest_zdr,omitempty"`
}

// Model is what OpenRouter's public listing says about one model.
type Model struct {
	ID         string     `json:"id"`
	Author     string     `json:"author"`
	Kind       string     `json:"kind"`
	InputMods  []string   `json:"input_modalities"`
	OutputMods []string   `json:"output_modalities"`
	Routes     []string   `json:"api_routes"`
	Context    *int       `json:"context_length,omitempty"`
	MaxOutput  *int       `json:"max_output_tokens,omitempty"`
	Pricing    Pricing    `json:"pricing"`
	Endpoints  *Endpoints `json:"endpoints,omitempty"`
}

// Data policy tiers, best first.
const (
	PolicyZDR             = "zdr"
	PolicySafetyRetention = "safety_retention"
)

func (m *Model) HasInput(mod string) bool  { return m != nil && has(m.InputMods, mod) }
func (m *Model) HasOutput(mod string) bool { return m != nil && has(m.OutputMods, mod) }

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (m *Model) IsClaude() bool { return m != nil && strings.HasPrefix(m.ID, "anthropic/") }

// Providers lists the providers that serve the model, when known.
func (m *Model) Providers() []string {
	if m == nil || m.Endpoints == nil {
		return nil
	}
	return m.Endpoints.Providers
}

// Free reports a model that costs nothing: a ":free" variant, or a text model listed at zero token
// prices. A zero for a media model billed by the image, second or character means unknown, not free.
func (m *Model) Free() bool {
	if m == nil {
		return false
	}
	if strings.HasSuffix(m.ID, ":free") {
		return true
	}
	p := m.Pricing
	return m.Kind == "text" && p.InputPerM != nil && p.OutputPerM != nil && *p.InputPerM == 0 && *p.OutputPerM == 0
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
func (m *Model) Batch() bool { return m != nil && strings.HasSuffix(m.ID, ":batch") }

// SingleProvider reports whether one provider serves the model.
func (m *Model) SingleProvider() bool {
	return m != nil && m.Endpoints != nil && m.Endpoints.ProviderCount == 1
}

// Policy is the best data-policy tier among the model's endpoints.
func (m *Model) Policy() string {
	if m == nil || m.Endpoints == nil {
		return ""
	}
	return m.Endpoints.BestDataPolicy
}

// TokenPrices are per-million-token prices for one way of calling a model.
type TokenPrices struct {
	In, Out, CacheRead float64
	Source             string
}

// Prices returns the token prices to use. With zdr set, only a zero-retention endpoint counts.
// cheapest picks the cheapest qualifying endpoint instead of the list price. ok is false when no
// token price is known.
func (m *Model) Prices(zdr, cheapest bool) (TokenPrices, bool) {
	if m == nil {
		return TokenPrices{}, false
	}
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

// ForPrompt applies long-context price tiers for a prompt of n tokens.
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
