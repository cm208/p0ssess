// Package tabletest gives tests a small route table signed with a key made from a fixed seed, and a
// fake engine service. Only tests import it; the numbers are fixtures, not data.
package tabletest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table"
)

// Key is the fixture's signing key.
func Key() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte("p0ssess route table test key"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	return priv.Public().(ed25519.PublicKey), priv
}

// Seal signs a payload with priv and returns the envelope.
func Seal(priv ed25519.PrivateKey, payload []byte) []byte {
	pub := priv.Public().(ed25519.PublicKey)
	b, _ := json.Marshal(table.Envelope{V: 1, KeyID: table.KeyID(pub), Payload: base64.StdEncoding.EncodeToString(payload),
		Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	return b
}

// Envelope is the fixture table, signed with Key.
func Envelope() []byte {
	_, priv := Key()
	return Seal(priv, Payload())
}

// Store opens a store in cacheDir that trusts Key and has the fixture built in.
func Store(cacheDir string) *table.Store {
	pub, _ := Key()
	return table.Open(cacheDir, table.WithKeys(pub), table.WithBuiltIn(Envelope()))
}

func f(v float64) *float64 { return &v }
func n(v int) *int         { return &v }

func textModel(id, author string, in, out float64, providers []string, zdr bool) table.Model {
	m := table.Model{ID: id, Author: author, Kind: "text", InputMods: []string{"text"}, OutputMods: []string{"text"},
		Routes: []string{"/api/v1/chat/completions"}, Context: n(200000), MaxOutput: n(32000),
		Pricing:   table.Pricing{InputPerM: f(in), OutputPerM: f(out)},
		Endpoints: &table.Endpoints{ProviderCount: len(providers), Providers: providers, HasZDR: zdr, BestDataPolicy: "not_in_zdr_list"}}
	if zdr {
		m.Endpoints.BestDataPolicy = table.PolicyZDR
		m.Endpoints.CheapestZDR = &table.Endpoint{Provider: providers[len(providers)-1], InputPerM: f(in * 1.2), OutputPerM: f(out * 1.2)}
	}
	return m
}

// Models are the fixture's model facts.
func Models() []table.Model {
	gem := textModel("google/gemini-3.8-flash", "google", 0.3, 2.5, []string{"Google"}, false)
	gem.InputMods = []string{"text", "image", "file", "audio", "video"}
	qwen := textModel("qwen/qwen3.8-omni-flash", "qwen", 0.2, 1.0, []string{"Alibaba"}, false)
	qwen.InputMods = []string{"text", "image", "audio", "video"}
	img := table.Model{ID: "google/gemini-3.4-flash-image", Author: "google", Kind: "image", InputMods: []string{"text", "image"},
		OutputMods: []string{"image", "text"}, Routes: []string{"/api/v1/chat/completions"},
		Pricing: table.Pricing{InputPerM: f(0.3), OutputPerM: f(2.5), ImageOutRaw: f(0.00003)}, Endpoints: &table.Endpoints{ProviderCount: 1, Providers: []string{"Google"}}}
	stt := table.Model{ID: "openai/whisper-large-v3-turbo", Author: "openai", Kind: "speech_to_text", InputMods: []string{"audio"},
		OutputMods: []string{"text"}, Routes: []string{"/api/v1/audio/transcriptions"},
		Pricing: table.Pricing{PromptRaw: f(0.0000111)}, Endpoints: &table.Endpoints{ProviderCount: 1, Providers: []string{"Groq"}}}
	return []table.Model{
		textModel("anthropic/claude-sonnet-5.5", "anthropic", 3, 15, []string{"Anthropic", "Google Vertex"}, true),
		textModel("anthropic/claude-opus-5.5", "anthropic", 5, 25, []string{"Anthropic"}, true),
		textModel("anthropic/claude-haiku-5.5", "anthropic", 1, 5, []string{"Anthropic"}, true),
		textModel("deepseek/deepseek-v4.1-flash", "deepseek", 0.1, 0.4, []string{"DeepSeek", "Fireworks"}, true),
		textModel("openai/gpt-6.1-sol", "openai", 1.25, 10, []string{"OpenAI", "Azure"}, true),
		textModel("z-ai/glm-5.3-flash", "z-ai", 0.06, 0.4, []string{"Z.AI"}, false),
		gem, qwen, img, stt,
	}
}

func uc(id string, num int, task, kind, family, path string, in, out []string, lo, hi, def, turns int, edits bool, verify string) table.UseCase {
	return table.UseCase{ID: id, N: num, Task: task, Kind: kind, Family: family, Path: path, Inputs: in, Outputs: out,
		StakesRange: []int{lo, hi}, DefaultStakes: def, Turns: turns, EditsCode: edits, Verify: verify}
}

var text = []string{"text"}

// UseCases are the fixture's use cases.
func UseCases() []table.UseCase {
	return []table.UseCase{
		uc("quick-code-question", 1, "One-turn coding question", "code", "coding", "direct", text, text, 1, 1, 1, 0, false, "spot"),
		uc("code-implement-feature", 8, "Implement a feature across a few files", "agent", "agentic_coding", "agent", text, text, 2, 3, 2, 20, true, "full"),
		uc("security-critical-code", 30, "Security-critical code", "code", "coding", "direct", text, text, 4, 4, 4, 0, true, "full"),
		uc("pdf-table-extract", 40, "Tables out of PDFs", "media", "document", "direct", []string{"file"}, text, 2, 3, 2, 0, false, "spot"),
		uc("support-reply-draft", 50, "Draft a reply to a customer", "text", "writing", "direct", text, text, 4, 4, 4, 0, false, "full"),
		uc("log-digest", 60, "Digest a long log", "text", "long_context", "direct", text, text, 1, 1, 1, 0, false, "spot"),
		uc("audio-transcribe", 71, "Meetings, memos, interviews to text; no Claude path", "media", "speech_to_text", "direct", []string{"audio"}, text, 2, 2, 2, 0, false, "none"),
		uc("audio-tone", 72, "How something was said", "media", "audio_understanding", "direct", []string{"audio"}, text, 1, 2, 1, 0, false, "spot"),
		uc("video-understand", 75, "Summarize a talk, meeting or lecture video", "media", "video_understanding", "direct", []string{"video", "audio"}, text, 1, 2, 1, 0, false, "spot"),
		uc("image-generate", 80, "Make an image from a description", "generate", "image_gen", "direct", text, []string{"image"}, 1, 1, 1, 0, false, "spot"),
		uc("dual-review-high-risk", 90, "Two-family review of a high-risk diff", "review", "coding", "direct", text, text, 4, 4, 4, 0, false, "full"),
		uc("limit-fallback-coding", 100, "Coding when Claude's limit is near", "agent", "agentic_coding", "agent", text, text, 1, 4, 0, 20, true, "full"),
		uc("audio-transcribe-noisy", 105, "Noisy or hard audio to text", "media", "speech_to_text", "direct", []string{"audio"}, text, 2, 2, 2, 0, false, "spot"),
		uc("qa-factual", 110, "Factual question with a short answer", "text", "knowledge", "direct", text, text, 1, 2, 1, 0, false, "spot"),
		uc("ocr-degraded-image", 111, "Text out of a degraded scan", "media", "ocr", "direct", []string{"image"}, text, 2, 2, 2, 0, false, "spot"),
		uc("invoice-receipt-extract", 112, "Fields out of invoices and receipts", "media", "document", "direct", []string{"image", "file"}, []string{"json"}, 3, 4, 3, 0, false, "spot"),
	}
}

func claude(total float64) remote.Option {
	return remote.Option{Route: "claude", Model: "anthropic/claude-sonnet-5.5", Total: total, Breakdown: remote.Breakdown{Claude: total},
		PFail: 0.1, PFailUpper: 0.2, Evidence: "fixture", ClaudeListUSD: total, Review: "Claude's own work", Reason: "keep on Claude"}
}

func paid(route, model string, total, est float64) remote.Option {
	return remote.Option{Route: route, Model: model, Total: total, Breakdown: remote.Breakdown{Cash: est, Overhead: total - est},
		PFail: 0.2, PFailUpper: 0.3, Evidence: "fixture", CashMax: est * 1.5, EstimateUSD: est, Review: "spot",
		Reason: "fixture option"}
}

func route(useCase string, stakes int, lambda string, feasible bool, opts ...remote.Option) table.Route {
	for i := range opts {
		opts[i].Rank = i + 1
	}
	return table.Route{UseCase: useCase, Stakes: stakes, Lambda: lambda, ClaudeFeasible: feasible, Options: opts}
}

// Routes are the fixture's answers.
func Routes() []table.Route {
	var rs []table.Route
	for _, l := range []string{"api", "subscription"} {
		for _, s := range []int{1, 2} {
			rs = append(rs, route("video-understand", s, l, false,
				paid("direct", "qwen/qwen3.8-omni-flash", 0.0926, 0.0223), paid("direct", "google/gemini-3.8-flash", 0.0957, 0.0275)))
		}
		rs = append(rs,
			route("audio-transcribe", 2, l, false, paid("direct", "openai/whisper-large-v3-turbo", 0.0123, 0.0006), paid("direct", "google/gemini-3.8-flash", 0.02, 0.003)),
			route("audio-transcribe-noisy", 2, l, false, paid("direct", "google/gemini-3.8-flash", 0.03, 0.004)),
			route("audio-tone", 1, l, false, paid("direct", "google/gemini-3.8-flash", 0.02, 0.003)),
			route("audio-tone", 2, l, false, paid("direct", "google/gemini-3.8-flash", 0.03, 0.003)),
			route("quick-code-question", 1, l, true, claude(0.01)),
			route("security-critical-code", 4, l, true, claude(0.4)),
			route("support-reply-draft", 4, l, true, claude(0.1)),
			route("dual-review-high-risk", 4, l, true, claude(0.3)),
			route("log-digest", 1, l, true, paid("direct", "deepseek/deepseek-v4.1-flash", 0.005, 0.001), claude(0.02)),
			route("image-generate", 1, l, false, paid("direct", "google/gemini-3.4-flash-image", 0.05, 0.039)),
			route("pdf-table-extract", 2, l, true, paid("engine", "engine:mistral-ocr", 0.03, 0.01), paid("direct", "google/gemini-3.8-flash", 0.04, 0.012), claude(0.2)),
			route("pdf-table-extract", 3, l, true, paid("engine", "engine:mistral-ocr", 0.05, 0.01), claude(0.25)),
		)
		for s := 1; s <= 4; s++ {
			rs = append(rs, route("limit-fallback-coding", s, l, true, claude(0.5)))
		}
		// With a Claude lead the break-even rule keeps these small jobs on Claude.
		rs = append(rs,
			route("qa-factual", 1, l, true, claude(0.01)),
			route("qa-factual", 2, l, true, claude(0.02)),
			route("ocr-degraded-image", 2, l, true, claude(0.03)),
			route("invoice-receipt-extract", 3, l, true, claude(0.05)),
			route("invoice-receipt-extract", 4, l, true, claude(0.06)),
		)
	}
	rs = append(rs,
		route("code-implement-feature", 2, "api", true, paid("agent", "deepseek/deepseek-v4.1-flash", 0.2, 0.05), paid("agent", "openai/gpt-6.1-sol", 0.45, 0.2), claude(0.5)),
		route("code-implement-feature", 2, "subscription", true, claude(0.1), paid("agent", "deepseek/deepseek-v4.1-flash", 0.15, 0.05)),
		route("code-implement-feature", 3, "api", true, paid("agent", "openai/gpt-6.1-sol", 0.5101, 0.211), claude(0.5395)),
		route("code-implement-feature", 3, "subscription", true, claude(0.1204), paid("agent", "openai/gpt-6.1-sol", 0.3072, 0.211)),
	)
	return rs
}

// NoLeadRoutes are the fixture's answers for a caller with no Claude lead.
func NoLeadRoutes() []table.Route {
	return []table.Route{
		route("qa-factual", 1, "none", false, paid("direct", "deepseek/deepseek-v4.1-flash", 0.001, 0.001), paid("direct", "z-ai/glm-5.3-flash", 0.0015, 0.001)),
		route("qa-factual", 2, "none", false, paid("direct", "deepseek/deepseek-v4.1-flash", 0.008, 0.001), paid("direct", "z-ai/glm-5.3-flash", 0.009, 0.001)),
		route("ocr-degraded-image", 2, "none", false, paid("direct", "google/gemini-3.8-flash", 0.009, 0.002)),
		route("invoice-receipt-extract", 3, "none", false, paid("direct", "google/gemini-3.8-flash", 0.065, 0.004), paid("direct", "qwen/qwen3.8-omni-flash", 0.08, 0.003)),
		route("invoice-receipt-extract", 4, "none", false),
		route("log-digest", 1, "none", false, paid("direct", "deepseek/deepseek-v4.1-flash", 0.004, 0.001)),
		route("video-understand", 1, "none", false, paid("direct", "qwen/qwen3.8-omni-flash", 0.03, 0.0223)),
	}
}

// Payload is the fixture table's JSON.
func Payload() []byte { return payload(true) }

// PayloadBeforeNoLead is the fixture as a server before 1.0.2 signed it: no no_lead_routes.
func PayloadBeforeNoLead() []byte { return payload(false) }

func payload(noLead bool) []byte {
	t := map[string]any{"v": 1, "tier": "free", "lambda_points": map[string]float64{"api": 1.0, "subscription": 0.15},
		"use_cases": UseCases(), "models": Models(), "routes": Routes()}
	if noLead {
		t["no_lead_routes"] = NoLeadRoutes()
	}
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	t["id"] = hex.EncodeToString(sum[:8])
	b, _ = json.Marshal(t)
	return b
}

// Engine is a fake engine service.
type Engine struct {
	*httptest.Server
	mu       sync.Mutex
	Requests []remote.Request
	Headers  []http.Header
}

// Last returns the last request the fake received.
func (e *Engine) Last() remote.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.Requests) == 0 {
		return remote.Request{}
	}
	return e.Requests[len(e.Requests)-1]
}

// Count is how many recommend requests the fake received.
func (e *Engine) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.Requests)
}

// Serve starts a fake engine service and points P0SSESS_CLOUD_URL at it. answer gets each decoded
// recommend request and returns the status and body; unknown request fields are refused, as the real
// service does. GET /v1/table serves the fixture envelope.
func Serve(t testing.TB, answer func(remote.Request) (int, any)) *Engine {
	t.Helper()
	e := &Engine{}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/table":
			w.Header().Set("ETag", `"fixture"`)
			if r.Header.Get("If-None-Match") == `"fixture"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Write(Envelope())
		case "/v1/recommend":
			dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
			dec.DisallowUnknownFields()
			var req remote.Request
			if err := dec.Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"bad_request","message":"` + err.Error() + `"}`))
				return
			}
			e.mu.Lock()
			e.Requests = append(e.Requests, req)
			e.Headers = append(e.Headers, r.Header.Clone())
			e.mu.Unlock()
			code, body := answer(req)
			w.Header().Set("P0ssess-Plan", "free")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.Close)
	t.Setenv("P0SSESS_CLOUD_URL", e.URL)
	return e
}

// Down points P0SSESS_CLOUD_URL at a fake service that answers 503 to everything.
func Down(t testing.TB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"unavailable","message":"down"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	return srv
}
