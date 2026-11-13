package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ModelInfo is what the cost guard and the router need to know about a model. Prices are USD.
type ModelInfo struct {
	ID          string             `json:"id"`
	Input       []string           `json:"input_modalities"`
	Output      []string           `json:"output_modalities"`
	Routes      []string           `json:"api_routes,omitempty"`
	HasZDR      bool               `json:"has_zdr_endpoint"`
	ZDRKnown    bool               `json:"-"`      // HasZDR comes from the registry snapshot
	Source      string             `json:"source"` // "registry" or "live"
	InPerM      float64            `json:"input_per_m"`
	OutPerM     float64            `json:"output_per_m"`
	AudioPerM   float64            `json:"audio_in_per_m,omitempty"`
	ReasonPerM  float64            `json:"reasoning_per_m,omitempty"`
	LongContext []LongContextPrice `json:"long_context,omitempty"`

	// Image output, from the image endpoints listing: the highest price across endpoints.
	ImageUnit string  `json:"image_unit,omitempty"` // "image", "megapixel" or "token"
	ImageCost float64 `json:"image_cost,omitempty"`
	// Speech-to-text price in USD per second of audio, the highest across endpoints when known live.
	STTRaw float64 `json:"stt_usd_per_second,omitempty"`
	// Free is a ":free" variant, or a text model the registry snapshot lists at zero token prices.
	// Any other zero price is unknown.
	Free bool `json:"free,omitempty"`
}

type LongContextPrice struct {
	MinPromptTokens int     `json:"min_prompt_tokens"`
	InPerM          float64 `json:"prompt_per_m"`
	OutPerM         float64 `json:"completion_per_m"`
}

func (m ModelInfo) Takes(modality string) bool { return has(m.Input, modality) }
func (m ModelInfo) Makes(modality string) bool { return has(m.Output, modality) }
func (m ModelInfo) ImagesRoute() bool          { return has(m.Routes, "/api/v1/images") }

// priceGap says which price a call on route would need but is not known, or "" when the estimate rests
// on real prices (or the model is free).
func (m ModelInfo) priceGap(route string, out Output) string {
	if m.Free || strings.HasSuffix(m.ID, ":free") {
		return ""
	}
	switch route {
	case "stt":
		if m.STTRaw <= 0 {
			return "no speech-to-text price is known for " + m.ID
		}
	case "images":
		if m.ImageCost <= 0 {
			return "no image price is known for " + m.ID
		}
	default:
		if m.InPerM <= 0 && m.OutPerM <= 0 && m.ReasonPerM <= 0 {
			return "no token price is known for " + m.ID
		}
		if out == OutImage && m.ImageCost <= 0 {
			return "no image output price is known for " + m.ID
		}
	}
	return ""
}

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Catalog looks up model facts: the registry snapshot first, then OpenRouter's public endpoints listing
// (no key needed). Live answers are cached for the life of the Catalog.
type Catalog struct {
	BaseURL string
	HTTP    *http.Client
	NoLive  bool

	mu   sync.Mutex
	reg  map[string]ModelInfo
	live map[string]ModelInfo
}

func NewCatalog(baseURL string, hc *http.Client) *Catalog {
	return &Catalog{BaseURL: baseURL, HTTP: hc, reg: map[string]ModelInfo{}, live: map[string]ModelInfo{}}
}

// LoadRegistry reads registry/models.json.
func (c *Catalog) LoadRegistry(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Models []struct {
			ID        string                     `json:"id"`
			Kind      string                     `json:"kind"`
			Input     []string                   `json:"input_modalities"`
			Output    []string                   `json:"output_modalities"`
			Routes    []string                   `json:"api_routes"`
			Pricing   map[string]json.RawMessage `json:"pricing"`
			Endpoints struct {
				HasZDR bool `json:"has_zdr_endpoint"`
			} `json:"endpoints"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("registry %s: %w", path, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range doc.Models {
		mi := ModelInfo{ID: m.ID, Input: m.Input, Output: m.Output, Routes: m.Routes, HasZDR: m.Endpoints.HasZDR, ZDRKnown: true, Source: "registry"}
		num := func(k string) float64 {
			var f float64
			_ = json.Unmarshal(m.Pricing[k], &f)
			return f
		}
		mi.InPerM, mi.OutPerM = num("input_per_m"), num("output_per_m")
		mi.AudioPerM, mi.ReasonPerM = num("audio_in_per_m"), num("reasoning_per_m")
		mi.STTRaw = num("prompt_usd_raw")
		if raw, ok := m.Pricing["long_context_overrides"]; ok {
			_ = json.Unmarshal(raw, &mi.LongContext)
		}
		if f := num("image_out_raw"); f > 0 {
			mi.ImageUnit, mi.ImageCost = "token", f
		}
		_, hasIn := m.Pricing["input_per_m"]
		_, hasOut := m.Pricing["output_per_m"]
		mi.Free = strings.HasSuffix(m.ID, ":free") ||
			(m.Kind == "text" && hasIn && hasOut && mi.InPerM == 0 && mi.OutPerM == 0)
		c.reg[m.ID] = mi
	}
	return nil
}

func (c *Catalog) Add(m ModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reg[m.ID] = m
}

// Lookup returns what is known about a model. Unless live lookups are off, prices come from OpenRouter's
// public endpoint listings and are the highest across endpoints, so the estimate (and the max_price cap
// sent with chat calls) holds whichever endpoint serves the call. The registry snapshot fills in what the
// listings lack (ZDR, long-context prices) and is the offline fallback.
func (c *Catalog) Lookup(ctx context.Context, id string) (ModelInfo, error) {
	c.mu.Lock()
	if mi, ok := c.live[id]; ok {
		c.mu.Unlock()
		return mi, nil
	}
	reg, inReg := c.reg[id]
	c.mu.Unlock()
	if c.NoLive {
		if inReg {
			return reg, nil
		}
		return ModelInfo{}, fmt.Errorf("model %s is not in the registry (live lookup disabled)", id)
	}
	live, err := c.fetch(ctx, id, reg)
	if err != nil {
		if inReg {
			return reg, nil
		}
		return ModelInfo{}, err
	}
	c.mu.Lock()
	c.live[id] = live
	c.mu.Unlock()
	return live, nil
}

// Resolve turns a bare model name such as "recraft-v4.1-flash" into its full OpenRouter id
// ("recraft/recraft-v4.1-flash"). An id that already names its provider is returned as it is. A bare name
// must match exactly one model, from the registry snapshot and, unless live lookups are off, OpenRouter's
// public chat and image model lists.
func (c *Catalog) Resolve(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") {
		return id, nil
	}
	seen := map[string]bool{}
	var found []string
	match := func(full string) {
		if seen[full] {
			return
		}
		if _, name, ok := strings.Cut(full, "/"); ok && strings.EqualFold(name, id) {
			seen[full] = true
			found = append(found, full)
		}
	}
	c.mu.Lock()
	for full := range c.reg {
		match(full)
	}
	c.mu.Unlock()
	if len(found) == 0 && !c.NoLive {
		for _, path := range []string{"/models", "/images/models"} {
			var list struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if c.get(ctx, path, &list) == nil {
				for _, m := range list.Data {
					match(m.ID)
				}
			}
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("model %s: no OpenRouter model has that name; give the full id, provider/name", id)
	}
	return "", fmt.Errorf("model %s is ambiguous: %s; give the full id", id, strings.Join(found, ", "))
}

func (c *Catalog) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Catalog) fetch(ctx context.Context, id string, base ModelInfo) (ModelInfo, error) {
	mi := base
	mi.ID, mi.Source = id, "live"
	var ep struct {
		Data struct {
			Architecture struct {
				Input  []string `json:"input_modalities"`
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
			Endpoints []struct {
				Pricing map[string]any `json:"pricing"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	chatErr := c.get(ctx, "/models/"+id+"/endpoints", &ep)
	if chatErr == nil {
		mi.Input, mi.Output = ep.Data.Architecture.Input, ep.Data.Architecture.Output
		if len(mi.Routes) == 0 && mi.Makes("text") {
			mi.Routes = []string{"/api/v1/chat/completions"}
		}
		for _, e := range ep.Data.Endpoints {
			p := func(k string) float64 { return perToken(e.Pricing[k]) }
			mi.InPerM = math.Max(mi.InPerM, p("prompt")*1e6)
			mi.OutPerM = math.Max(mi.OutPerM, p("completion")*1e6)
			mi.AudioPerM = math.Max(mi.AudioPerM, p("audio")*1e6)
			mi.ReasonPerM = math.Max(mi.ReasonPerM, p("internal_reasoning")*1e6)
			if mi.Makes("transcription") {
				mi.STTRaw = math.Max(mi.STTRaw, p("prompt"))
			}
		}
	}
	var img struct {
		Endpoints []struct {
			Pricing []struct {
				Billable string  `json:"billable"`
				Unit     string  `json:"unit"`
				Cost     float64 `json:"cost_usd"`
			} `json:"pricing"`
		} `json:"endpoints"`
	}
	if imgErr := c.get(ctx, "/images/models/"+id+"/endpoints", &img); imgErr == nil && len(img.Endpoints) > 0 {
		if !mi.Makes("image") {
			mi.Output = append(mi.Output, "image")
		}
		if !mi.ImagesRoute() {
			mi.Routes = append(mi.Routes, "/api/v1/images")
		}
		if chatErr != nil && !has(mi.Input, "text") {
			mi.Input = append(mi.Input, "text")
		}
		// The live listing replaces the snapshot's image price only when it has one: a listing with no
		// output_image price must not turn a known price into $0.
		unit, cost := "", 0.0
		rank := map[string]int{"image": 3, "megapixel": 2, "token": 1}
		for _, e := range img.Endpoints {
			for _, p := range e.Pricing {
				if p.Billable != "output_image" || !(p.Cost > 0) {
					continue
				}
				if rank[p.Unit] > rank[unit] || (p.Unit == unit && p.Cost > cost) {
					unit, cost = p.Unit, p.Cost
				}
			}
		}
		if cost > 0 {
			mi.ImageUnit, mi.ImageCost = unit, cost
		}
		return mi, nil
	} else if chatErr != nil {
		return ModelInfo{}, fmt.Errorf("model %s: not in the registry and not found on OpenRouter (%v)", id, chatErr)
	}
	return mi, nil
}

func perToken(v any) float64 {
	switch x := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case float64:
		return x
	}
	return 0
}

// Estimate is the pre-send cost bound. It is deliberately on the high side: duration is measured with
// ffprobe, token rates are upper ends of the published ranges, and the full completion cap is priced.
type Estimate struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	FeeUSD       float64 `json:"fee_usd,omitempty"` // OCR and per-image charges
	CostUSD      float64 `json:"cost_usd"`
	Calls        int     `json:"calls"`
	// CostUnknown means a price the call needs is not known, so CostUSD is not a bound; only an explicit
	// max_cost_usd limits the call.
	CostUnknown bool     `json:"cost_unknown,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// ErrOverBudget is returned when the estimate is above Request.MaxCostUSD.
var ErrOverBudget = errors.New("estimated cost is above the limit")

// ErrPriceUnknown is returned when a price the call needs is unknown and no max_cost_usd above 0 was given.
var ErrPriceUnknown = errors.New("price unknown")

// Token rates used by the estimate. Gemini rates are Google's published figures (audio 32 tokens per
// second; video about 100–300 per second, upper end used). Other families publish nothing comparable,
// so they get a wider margin until measured; docs/MEDIA.md records what real calls billed.
func tokensPerSecond(model string, k Kind) float64 {
	google := strings.HasPrefix(model, "google/")
	switch k {
	case KindAudio:
		if google {
			return 32
		}
		return 50
	case KindVideo:
		if google {
			return 300
		}
		return 600
	}
	return 0
}

const (
	tokensPerImage      = 1600
	textBytesPerToken   = 3.0
	ocrUSDPerPage       = 0.002 // mistral-ocr, $2 per 1,000 pages
	parsedTokensPerPage = 1000
	imageTokenBudget    = 4200 // generated image, when billed per token
	geminiImageTokens   = 2000 // Gemini bills about 1,290 tokens for a 1K image
	imageMegapixels     = 2.0
)

func pdfTokensPerPage(model string) int {
	if strings.HasPrefix(model, "google/") {
		return 600
	}
	return 3000
}

func estimateChat(m ModelInfo, req Request, inputs []Input, engine string) Estimate {
	e := Estimate{Calls: 1}
	tok := float64(len(req.Prompt)+len(req.System)) / textBytesPerToken
	var audioTok float64
	for _, in := range inputs {
		switch in.Kind {
		case KindText:
			tok += float64(in.Bytes) / textBytesPerToken
		case KindImage:
			tok += tokensPerImage
		case KindPDF:
			switch engine {
			case "native":
				tok += float64(in.Pages * pdfTokensPerPage(m.ID))
			case "mistral-ocr":
				tok += float64(in.Pages*parsedTokensPerPage) + 8*tokensPerImage
				e.FeeUSD += float64(in.Pages) * ocrUSDPerPage
			default:
				tok += float64(in.Pages * parsedTokensPerPage)
			}
		case KindAudio:
			audioTok += in.Seconds * tokensPerSecond(m.ID, KindAudio)
		case KindVideo:
			tok += in.Seconds * tokensPerSecond(m.ID, KindVideo)
		}
		if in.Duration == "size" {
			e.Notes = append(e.Notes, in.Path+": duration guessed from file size")
		}
	}
	e.InputTokens = int(tok + audioTok)
	e.OutputTokens = req.MaxTokens
	in, out := m.InPerM, math.Max(m.OutPerM, m.ReasonPerM)
	for _, lc := range m.LongContext {
		if e.InputTokens > lc.MinPromptTokens {
			in, out = math.Max(in, lc.InPerM), math.Max(out, lc.OutPerM)
		}
	}
	audioPrice := in
	if m.AudioPerM > 0 {
		audioPrice = math.Max(m.AudioPerM, in)
	}
	e.CostUSD = tok*in/1e6 + audioTok*audioPrice/1e6 + float64(e.OutputTokens)*out/1e6 + e.FeeUSD
	if req.Output == OutImage {
		fee, note := imageFee(m)
		e.FeeUSD += fee
		e.CostUSD += fee
		if note != "" {
			e.Notes = append(e.Notes, note)
		}
	}
	return e
}

func imageFee(m ModelInfo) (float64, string) {
	switch m.ImageUnit {
	case "image":
		return m.ImageCost, ""
	case "megapixel":
		return m.ImageCost * imageMegapixels, fmt.Sprintf("image priced per megapixel; %.0f MP assumed", imageMegapixels)
	case "token":
		n := imageTokenBudget
		if strings.HasPrefix(m.ID, "google/") {
			n = geminiImageTokens
		}
		return m.ImageCost * float64(n), fmt.Sprintf("image priced per token; %d image tokens assumed", n)
	}
	return 0, "no image output price found; only token prices are counted"
}

// tokenCost prices a finished chat call from the tokens its answer reported, at the model's list price
// (the highest across endpoints, and the long-context tier when the prompt is that long). ok is false
// when it cannot: not a text chat call, no price known, or no tokens reported.
func (m ModelInfo) tokenCost(route string, out Output, u *apiUsage) (float64, bool) {
	in, completion := float64(u.PromptTokens+u.InputTokens), float64(u.CompletionTokens+u.OutputTokens)
	if route != "chat" || out == OutImage || in+completion <= 0 || m.priceGap(route, out) != "" {
		return 0, false
	}
	if m.Free || strings.HasSuffix(m.ID, ":free") {
		return 0, true
	}
	inP, outP := m.InPerM, math.Max(m.OutPerM, m.ReasonPerM)
	for _, lc := range m.LongContext {
		if in > float64(lc.MinPromptTokens) {
			inP, outP = math.Max(inP, lc.InPerM), math.Max(outP, lc.OutPerM)
		}
	}
	audio := math.Min(float64(u.PromptTokensDetails.AudioTokens), in)
	audioP := inP
	if m.AudioPerM > 0 {
		audioP = math.Max(m.AudioPerM, inP)
	}
	return ((in-audio)*inP + audio*audioP + completion*outP) / 1e6, true
}

// estimateSTT prices transcription per second of audio. OpenRouter lists the price as "prompt" without a
// unit; a billed call (docs/MEDIA.md) matched the serving endpoint's price times the audio seconds.
func estimateSTT(m ModelInfo, in Input) Estimate {
	return Estimate{Calls: 1, CostUSD: m.STTRaw * math.Max(in.Seconds, 1)}
}
