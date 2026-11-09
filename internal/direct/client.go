package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultBaseURL = "https://openrouter.ai/api/v1"

type Client struct {
	BaseURL string
	Key     string // never logged or returned
	HTTP    *http.Client
	Catalog *Catalog
	Limits  Limits
	// StateDir holds generated files when Request.OutDir is empty.
	StateDir string
	// GenerationWait is how long to keep asking /generation for a cost the response did not carry.
	GenerationWait time.Duration
	// RecordDir, when set, receives each raw response body (for building test fixtures).
	RecordDir string
}

func New(key string) *Client {
	hc := &http.Client{Timeout: 15 * time.Minute}
	return &Client{
		BaseURL:        DefaultBaseURL,
		Key:            key,
		HTTP:           hc,
		Catalog:        NewCatalog(DefaultBaseURL, &http.Client{Timeout: 30 * time.Second}),
		GenerationWait: 6 * time.Second,
	}
}

// APIError is an error answer from OpenRouter.
type APIError struct {
	Status   int
	Code     any
	Message  string
	Provider string
	Raw      string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("openrouter: HTTP %d: %s", e.Status, e.Message)
	if e.Provider != "" {
		s += " (provider " + e.Provider + ")"
	}
	if e.Raw != "" {
		s += ": " + e.Raw
	}
	return s
}

// plan is a checked request: inputs inspected, route and engine chosen, cost estimated.
type plan struct {
	req    Request
	model  ModelInfo
	inputs []Input
	route  string // "chat", "images" or "stt"
	engine string
	est    Estimate
	warn   []string
}

// Plan checks a request and estimates its cost without sending anything.
func (c *Client) Plan(ctx context.Context, req Request) (*plan, error) {
	if req.Model == "" {
		return nil, errors.New("model is required")
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = DefaultMaxTokens
	}
	if req.Output == "" {
		req.Output = OutText
	}
	m, err := c.Catalog.Lookup(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	p := &plan{req: req, model: m}
	var total int64
	for _, f := range req.Files {
		in, err := Inspect(f, c.Limits)
		if err != nil {
			return nil, err
		}
		total += in.Bytes
		p.inputs = append(p.inputs, in)
	}
	lim := c.Limits.withDefaults()
	if total > lim.TotalBytes && req.VideoPrep == PrepNone {
		return nil, fmt.Errorf("attachments total %s, over the %s request limit", human(total), human(lim.TotalBytes))
	}

	switch {
	case req.Output == OutTranscript || (m.Makes("transcription") && !m.Makes("text")):
		p.route = "stt"
	case req.Output == OutImage:
		if !m.Makes("image") {
			return nil, fmt.Errorf("%s does not generate images (output: %s)", m.ID, strings.Join(m.Output, ", "))
		}
		if m.Makes("text") && has(m.Routes, "/api/v1/chat/completions") {
			p.route = "chat"
		} else {
			p.route = "images"
		}
	default:
		if !m.Makes("text") {
			return nil, fmt.Errorf("%s does not produce text (output: %s); set output to match", m.ID, strings.Join(m.Output, ", "))
		}
		p.route = "chat"
	}
	if req.StrictZDR && p.route == "images" {
		return nil, fmt.Errorf("strict ZDR: the image API does not document zdr or data_collection routing, so %s cannot be used; pick an image model served on chat completions", m.ID)
	}
	if req.StrictZDR && !m.HasZDR && m.Source == "registry" {
		p.warn = append(p.warn, m.ID+" has no ZDR endpoint in the registry snapshot; OpenRouter will likely refuse the call")
	}

	switch p.route {
	case "stt":
		if len(p.inputs) != 1 || p.inputs[0].Kind != KindAudio {
			return nil, errors.New("transcription takes exactly one audio file")
		}
		if !m.Makes("transcription") {
			return nil, fmt.Errorf("%s is not a speech-to-text model; use a chat model that takes audio with output text", m.ID)
		}
		p.est = estimateSTT(m, p.inputs[0])
	case "images":
		for _, in := range p.inputs {
			if in.Kind != KindImage {
				return nil, fmt.Errorf("%s: image generation takes only reference images", in.Path)
			}
		}
		fee, note := imageFee(m)
		p.est = Estimate{Calls: 1, InputTokens: int(float64(len(req.Prompt)) / textBytesPerToken), FeeUSD: fee}
		p.est.CostUSD = fee + float64(p.est.InputTokens)*m.InPerM/1e6
		if note != "" {
			p.est.Notes = append(p.est.Notes, note)
		}
	case "chat":
		if err := p.checkModalities(); err != nil {
			return nil, err
		}
		p.engine = pdfEngine(req.PDFEngine, m, p.inputs)
		if err := p.checkVideo(lim); err != nil {
			return nil, err
		}
		p.est = estimateChat(m, req, p.inputs, p.engine)
		if req.VideoPrep == PrepSegment {
			p.est = p.segmentEstimate(lim)
		}
		if req.VideoPrep == PrepKeyframes {
			p.est = p.keyframeEstimate(lim)
		}
	}
	if !req.DryRun && p.est.CostUSD > req.MaxCostUSD {
		return p, fmt.Errorf("%w: about $%.4f for %s, limit $%.4f", ErrOverBudget, p.est.CostUSD, m.ID, req.MaxCostUSD)
	}
	return p, nil
}

func (p *plan) checkModalities() error {
	need := map[Kind]string{KindImage: "image", KindPDF: "file", KindAudio: "audio", KindVideo: "video"}
	for _, in := range p.inputs {
		mod, ok := need[in.Kind]
		if !ok {
			continue
		}
		if in.Kind == KindPDF {
			continue // OpenRouter parses PDFs for any model
		}
		if in.Kind == KindVideo && p.req.VideoPrep == PrepKeyframes {
			if !p.model.Takes("image") {
				return fmt.Errorf("keyframes need a model that takes images; %s does not", p.model.ID)
			}
			continue
		}
		if !p.model.Takes(mod) {
			return fmt.Errorf("%s does not take %s input (it takes: %s)", p.model.ID, mod, strings.Join(p.model.Input, ", "))
		}
	}
	return nil
}

func (p *plan) checkVideo(lim Limits) error {
	for _, in := range p.inputs {
		if in.Kind != KindVideo {
			continue
		}
		why := VideoOverLimit(in, lim)
		if why == "" || p.req.VideoPrep != PrepNone {
			continue
		}
		return fmt.Errorf("%s: %s. Nothing was uploaded. Options (set video_prep): "+
			"\"downscale\" re-encodes to a %dp, 1 fps copy with mono audio; "+
			"\"keyframes\" sends up to %d scene-change frames as images (plus the audio track when the model takes audio); "+
			"\"segment\" splits it into %.0f s parts and runs one call per part. "+
			"Or trim it yourself (ffmpeg -ss START -t LENGTH -i in.mp4 out.mp4)",
			in.Path, why, lim.DownscaleHeight, lim.MaxKeyframes, lim.SegmentSeconds)
	}
	return nil
}

func (p *plan) segmentEstimate(lim Limits) Estimate {
	e := estimateChat(p.model, p.req, p.inputs, p.engine)
	for _, in := range p.inputs {
		if in.Kind == KindVideo {
			n := int(in.Seconds/lim.SegmentSeconds) + 1
			if n > e.Calls {
				e.Calls = n
			}
		}
	}
	// Each extra call repeats the prompt, the other attachments and the completion cap.
	base := estimateChat(p.model, p.req, nonVideo(p.inputs), p.engine)
	e.CostUSD += float64(e.Calls-1) * base.CostUSD
	e.OutputTokens *= e.Calls
	return e
}

func (p *plan) keyframeEstimate(lim Limits) Estimate {
	var ins []Input
	for _, in := range p.inputs {
		if in.Kind != KindVideo {
			ins = append(ins, in)
			continue
		}
		for i := 0; i < lim.MaxKeyframes; i++ {
			ins = append(ins, Input{Kind: KindImage})
		}
		if p.model.Takes("audio") {
			ins = append(ins, Input{Kind: KindAudio, Seconds: in.Seconds})
		}
	}
	return estimateChat(p.model, p.req, ins, p.engine)
}

func nonVideo(ins []Input) []Input {
	var out []Input
	for _, in := range ins {
		if in.Kind != KindVideo {
			out = append(out, in)
		}
	}
	return out
}

// pdfEngine follows OpenRouter's guidance: native when the model takes files, Mistral OCR for scans,
// the free Cloudflare parser for text PDFs otherwise.
func pdfEngine(asked string, m ModelInfo, ins []Input) string {
	if asked != "" {
		return asked
	}
	anyPDF, scanned := false, false
	for _, in := range ins {
		if in.Kind == KindPDF {
			anyPDF = true
			scanned = scanned || !in.HasText
		}
	}
	switch {
	case !anyPDF:
		return ""
	case m.Takes("file"):
		return "native"
	case scanned:
		return "mistral-ocr"
	}
	return "cloudflare-ai"
}

// Run checks, estimates and sends a request.
func (c *Client) Run(ctx context.Context, req Request) (*Result, error) {
	start := time.Now()
	p, err := c.Plan(ctx, req)
	if err != nil {
		return nil, err
	}
	res := &Result{Model: p.model.ID, Estimate: p.est, Route: p.route, Warnings: p.warn}
	if p.req.DryRun {
		res.Text = fmt.Sprintf("dry run: about $%.4f (%d input tokens, up to %d output tokens, %d call(s))",
			p.est.CostUSD, p.est.InputTokens, p.est.OutputTokens, p.est.Calls)
		return res, nil
	}
	if c.Key == "" {
		return nil, errors.New("no OpenRouter key")
	}
	outDir, err := c.outDir(p.req.OutDir)
	if err != nil {
		return nil, err
	}
	switch p.route {
	case "stt":
		err = c.runSTT(ctx, p, res)
	case "images":
		err = c.runImages(ctx, p, res, outDir)
	default:
		err = c.runChatPrepared(ctx, p, res, outDir)
	}
	res.Elapsed = time.Since(start)
	if err != nil {
		return res, err
	}
	return res, nil
}

func (c *Client) outDir(asked string) (string, error) {
	dir := asked
	if dir == "" {
		base := c.StateDir
		if base == "" {
			if s := os.Getenv("XDG_STATE_HOME"); s != "" {
				base = filepath.Join(s, "p0ssess", "direct")
			} else {
				home, _ := os.UserHomeDir()
				base = filepath.Join(home, ".local", "state", "p0ssess", "direct")
			}
		}
		dir = filepath.Join(base, time.Now().UTC().Format("20060102-150405.000"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Abs(dir)
}

func (c *Client) provider(p *plan) map[string]any {
	pr := map[string]any{"data_collection": "deny"}
	if p.req.StrictZDR {
		pr["zdr"] = true
	}
	return pr
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.headers(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return redact(err, c.Key)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return err
	}
	if c.RecordDir != "" {
		name := fmt.Sprintf("%s-%s.json", time.Now().UTC().Format("150405.000"), strings.Trim(strings.ReplaceAll(path, "/", "-"), "-"))
		_ = os.WriteFile(filepath.Join(c.RecordDir, name), data, 0o600)
	}
	if resp.StatusCode != 200 {
		return apiError(resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("openrouter: bad JSON answer: %w", err)
	}
	return nil
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("X-OpenRouter-Title", "p0ssess")
	req.Header.Set("X-Title", "p0ssess")
}

func apiError(status int, data []byte) error {
	var e struct {
		Error struct {
			Code     any            `json:"code"`
			Message  string         `json:"message"`
			Metadata map[string]any `json:"metadata"`
		} `json:"error"`
	}
	ae := &APIError{Status: status}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		ae.Code, ae.Message = e.Error.Code, e.Error.Message
		if pn, ok := e.Error.Metadata["provider_name"].(string); ok {
			ae.Provider = pn
		}
		if raw, ok := e.Error.Metadata["raw"].(string); ok {
			ae.Raw = truncate(raw, 400)
		}
	} else {
		ae.Message = truncate(strings.TrimSpace(string(data)), 400)
	}
	return ae
}

func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[key]"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// generation asks OpenRouter for the billed cost and provider of a finished call. Stats can lag the
// answer by a few seconds.
func (c *Client) generation(ctx context.Context, id string) (cost float64, provider string, err error) {
	deadline := time.Now().Add(c.GenerationWait)
	for wait := 500 * time.Millisecond; ; wait *= 2 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/generation?id="+id, nil)
		c.headers(req)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return 0, "", redact(err, c.Key)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			var g struct {
				Data struct {
					TotalCost    float64 `json:"total_cost"`
					ProviderName string  `json:"provider_name"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &g); err != nil {
				return 0, "", err
			}
			return g.Data.TotalCost, g.Data.ProviderName, nil
		}
		if resp.StatusCode != 404 || time.Now().Add(wait).After(deadline) {
			return 0, "", apiError(resp.StatusCode, data)
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(wait):
		}
	}
}

func encodeFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
