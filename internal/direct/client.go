package direct

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"sync/atomic"
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

	sent, rejected atomic.Int64
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
	if e.Status == 200 {
		// The provider failed after OpenRouter had answered 200; the code is the provider's.
		s = fmt.Sprintf("openrouter: provider error %v: %s", e.Code, e.Message)
	}
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
	tokensDefaulted := req.MaxTokens <= 0
	if tokensDefaulted {
		req.MaxTokens = DefaultMaxTokens
	}
	if req.Output == "" {
		req.Output = OutText
	}
	asked := req.Model
	id, err := c.Catalog.Resolve(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	req.Model = id
	m, err := c.Catalog.Lookup(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	p := &plan{req: req, model: m}
	if asked != id {
		p.warn = append(p.warn, fmt.Sprintf("model %s is %s", asked, id))
	}
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
	p.autoPrep(total, lim)
	req = p.req
	if total > lim.TotalBytes && req.VideoPrep == PrepNone {
		msg := fmt.Sprintf("attachments total %s, over the %s request limit", human(total), human(lim.TotalBytes))
		var videos []string
		for _, in := range p.inputs {
			if in.Kind == KindVideo {
				videos = append(videos, fmt.Sprintf("%s (%s)", in.Path, human(in.Bytes)))
			}
		}
		if len(videos) > 0 {
			return nil, fmt.Errorf("%s; video: %s. Nothing was uploaded. %s", msg, strings.Join(videos, ", "), videoPrepOptions(lim))
		}
		return nil, errors.New(msg)
	}

	switch {
	case req.Output == OutTranscript && !m.Makes("transcription") && m.Makes("text") && m.Takes("audio"):
		// A chat model that hears audio transcribes through chat; only speech-to-text models use the
		// transcription route.
		p.route = "chat"
		req.Output = OutText
		if strings.TrimSpace(req.Prompt) == "" {
			req.Prompt = transcribePrompt(req)
		}
		p.req = req
		p.warn = append(p.warn, m.ID+" is not a speech-to-text model, so it transcribes through chat")
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
	if req.StrictZDR && m.ZDRKnown && !m.HasZDR {
		p.warn = append(p.warn, m.ID+" has no ZDR endpoint in the registry snapshot; with zdr on, OpenRouter refuses the call or, if the account allows it, uses a Safety Retention endpoint")
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
	if req.ImageResolution != "" && req.Output == OutImage {
		res, err := imageResolution(req.ImageResolution, p.route)
		if err != nil {
			return nil, err
		}
		if res != req.ImageResolution {
			p.warn = append(p.warn, fmt.Sprintf("resolution %s sent as %s", req.ImageResolution, res))
		}
		req.ImageResolution, p.req.ImageResolution = res, res
	}
	if tokensDefaulted && !req.DryRun && p.route == "chat" && req.VideoPrep == PrepNone &&
		req.MaxCostUSD > 0 && p.est.CostUSD > req.MaxCostUSD && m.priceGap(p.route, req.Output) == "" {
		p.fitTokens(req)
		req = p.req
	}
	if gap := m.priceGap(p.route, req.Output); gap != "" {
		p.est.CostUnknown = true
		p.est.Notes = append(p.est.Notes, gap+": the estimate is not a bound, and OpenRouter is sent no price cap")
		if !req.DryRun && !(req.MaxCostUSD > 0) {
			return p, fmt.Errorf("%w: %s, so the cost cannot be estimated; pass an explicit max_cost_usd above 0, "+
				"which is reserved and counted against every limit", ErrPriceUnknown, gap)
		}
	}
	if !req.DryRun && p.est.CostUSD > req.MaxCostUSD {
		return p, fmt.Errorf("%w: about $%.4f for %s, limit $%.4f", ErrOverBudget, p.est.CostUSD, m.ID, req.MaxCostUSD)
	}
	return p, nil
}

// minFitTokens is the smallest answer cap fitTokens will set. Below it a reasoning model can spend the
// whole cap thinking, so the call is refused instead.
const minFitTokens = 1024

// fitTokens lowers the default answer cap so the estimate fits the cost limit. The estimate prices the
// whole cap, so for a short answer it is far above the bill (a 5 s clip estimated $0.028 and billed
// $0.0013); OpenRouter stops at max_tokens, so the lower cap keeps the bound true.
func (p *plan) fitTokens(req Request) {
	base := req
	base.MaxTokens = 0
	fixed := estimateChat(p.model, base, p.inputs, p.engine).CostUSD
	perToken := (p.est.CostUSD - fixed) / float64(req.MaxTokens)
	if !(perToken > 0) {
		return
	}
	n := int((req.MaxCostUSD - fixed) / perToken)
	if n < minFitTokens || n >= req.MaxTokens {
		return
	}
	req.MaxTokens = n
	p.req = req
	p.est = estimateChat(p.model, req, p.inputs, p.engine)
	p.warn = append(p.warn, fmt.Sprintf("answer capped at %d tokens to stay within the $%.4f limit", n, req.MaxCostUSD))
}

func transcribePrompt(req Request) string {
	s := "Transcribe this audio word for word."
	if req.Timestamps {
		s += " Start each line with its time range, mm:ss - mm:ss."
	}
	if req.Language != "" {
		s += " The language is " + req.Language + "."
	}
	return s
}

// Estimate is the plan's cost estimate.
func (p *plan) Estimate() Estimate { return p.est }

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
		return fmt.Errorf("%s: %s. Nothing was uploaded. %s", in.Path, why, videoPrepOptions(lim))
	}
	return nil
}

// autoPrep picks a local video step when the request cannot go as it is and the model takes video, so the
// caller does not pay a round trip to choose one: "segment" for a video over the length limit, else
// "downscale". The note says what was done; the estimate and the limits apply to the prepared request.
func (p *plan) autoPrep(total int64, lim Limits) {
	if p.req.VideoPrep != PrepNone || !p.model.Takes("video") || p.req.Output == OutImage || p.req.Output == OutTranscript || !haveFFmpeg() {
		return
	}
	var long, over []string
	for _, in := range p.inputs {
		if in.Kind != KindVideo {
			continue
		}
		if in.Duration != "ffprobe" {
			return // not a video ffmpeg can read; the error names the options
		}
		if in.Seconds > lim.VideoSeconds {
			long = append(long, filepath.Base(in.Path))
		} else if VideoOverLimit(in, lim) != "" || total > lim.TotalBytes {
			over = append(over, filepath.Base(in.Path))
		}
	}
	switch {
	case len(long) == 1 && len(over) == 0:
		p.req.VideoPrep = PrepSegment
		p.warn = append(p.warn, fmt.Sprintf("%s is over the %.0f s limit, so it was split into %.0f s parts, one call each (video_prep segment, chosen automatically)",
			long[0], lim.VideoSeconds, lim.SegmentSeconds))
	case len(long) == 0 && len(over) > 0:
		p.req.VideoPrep = PrepDownscale
		p.warn = append(p.warn, fmt.Sprintf("%s is over the size limit, so it was downscaled locally to %dp at 1 fps (video_prep downscale, chosen automatically)",
			strings.Join(over, ", "), lim.DownscaleHeight))
	}
}

// videoPrepOptions names the opt-in steps for a video over a limit, so the caller can retry with one
// instead of re-encoding the file itself.
func videoPrepOptions(lim Limits) string {
	return fmt.Sprintf("Options (set video_prep): "+
		"\"downscale\" re-encodes to a %dp, 1 fps copy with mono audio; "+
		"\"keyframes\" sends up to %d scene-change frames as images (plus the audio track when the model takes audio); "+
		"\"segment\" splits it into %.0f s parts and runs one call per part. "+
		"Or trim it yourself (ffmpeg -ss START -t LENGTH -i in.mp4 out.mp4)",
		lim.DownscaleHeight, lim.MaxKeyframes, lim.SegmentSeconds)
}

func (p *plan) segmentEstimate(lim Limits) Estimate {
	ins := append([]Input(nil), p.inputs...)
	calls := 1
	for i := range ins {
		if ins[i].Kind == KindVideo {
			n := max(int(math.Ceil(ins[i].Seconds/lim.SegmentSeconds)), 1)
			skip := min(max(p.req.FromSegment, 0), n-1)
			ins[i].Seconds = max(ins[i].Seconds-float64(skip)*lim.SegmentSeconds, 0)
			calls = max(calls, n-skip)
		}
	}
	e := estimateChat(p.model, p.req, ins, p.engine)
	e.Calls = calls
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
		if p.est.CostUnknown {
			res.Text = fmt.Sprintf("dry run: cost unknown (%d input tokens, up to %d output tokens, %d call(s)); %s",
				p.est.InputTokens, p.est.OutputTokens, p.est.Calls, strings.Join(p.est.Notes, "; "))
		}
		return res, nil
	}
	if c.Key == "" {
		return nil, errors.New("no OpenRouter key")
	}
	outDir, err := c.outDir(p.req.OutDir)
	if err != nil {
		return nil, err
	}
	sent0, rejected0 := c.sent.Load(), c.rejected.Load()
	defer func() {
		res.Sent = int(c.sent.Load() - sent0)
		res.Rejected = int(c.rejected.Load() - rejected0)
	}()
	defer c.prepAudio(ctx, p)()
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
	if lim := p.req.MaxTokens * max(p.est.Calls, 1); p.route == "chat" && res.Usage.CompletionTokens > lim {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the provider billed %d completion tokens against a max_tokens of %d", res.Usage.CompletionTokens, lim))
	}
	if res.Usage.CostUSD > p.est.CostUSD*1.0001 && !p.est.CostUnknown {
		res.Warnings = append(res.Warnings, fmt.Sprintf("actual cost $%.6f was above the estimate $%.6f", res.Usage.CostUSD, p.est.CostUSD))
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
	c.sent.Add(1)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return redact(err, c.Key)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// OpenRouter does not bill a request it answers with an error status.
		c.rejected.Add(1)
	}
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
	var top struct {
		Error *json.RawMessage `json:"error"`
		Usage *apiUsage        `json:"usage"`
	}
	if json.Unmarshal(data, &top) == nil && top.Error != nil && string(*top.Error) != "null" {
		// A provider failure after the 200 status was sent. With no usage in it, nothing was billed.
		if top.Usage == nil || top.Usage.Cost == nil {
			c.rejected.Add(1)
		}
		return apiError(200, data)
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

// ProviderFailed reports whether err is a provider timeout or server error that OpenRouter answered and
// did not bill, so the same request may go to another model.
func ProviderFailed(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	code := ae.Status
	if code == 200 {
		switch v := ae.Code.(type) {
		case float64:
			code = int(v)
		case string:
			code, _ = strconv.Atoi(v)
		}
	}
	return code == 408 || code >= 500
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
type generationInfo struct {
	TotalCost    float64  `json:"total_cost"`
	ProviderName string   `json:"provider_name"`
	IsBYOK       bool     `json:"is_byok"`
	Upstream     *float64 `json:"upstream_inference_cost"`
}

func (c *Client) generation(ctx context.Context, id string) (generationInfo, error) {
	deadline := time.Now().Add(c.GenerationWait)
	for wait := 500 * time.Millisecond; ; wait *= 2 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/generation?id="+id, nil)
		c.headers(req)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return generationInfo{}, redact(err, c.Key)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			var g struct {
				Data generationInfo `json:"data"`
			}
			err := json.Unmarshal(data, &g)
			return g.Data, err
		}
		if resp.StatusCode != 404 || time.Now().Add(wait).After(deadline) {
			return generationInfo{}, apiError(resp.StatusCode, data)
		}
		select {
		case <-ctx.Done():
			return generationInfo{}, ctx.Err()
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
