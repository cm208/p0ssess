package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Reading what a model call cost. OpenRouter reports a call's cost in the answer's usage object when the
// request asks for it (usage.include = true); when an answer carries none, the generation endpoint has it
// a moment later. The cloud sandbox's cost guard and the local jail's key proxy both meter this way.

// BaseModel is an OpenRouter model id without the "openrouter/" prefix or a "#" suffix, lower case.
func BaseModel(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "openrouter/")
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// ModelCall reports whether a request goes to an endpoint that runs a model and bills for it.
func ModelCall(method, p string) bool {
	return method == http.MethodPost &&
		(p == "/api/v1/chat/completions" || p == "/api/v1/completions" || p == "/api/v1/responses")
}

// WithUsage checks that a model request names exactly one model and asks OpenRouter to report its cost.
// It returns the rewritten body and the model (BaseModel form).
func WithUsage(body []byte) ([]byte, string, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("the request body is not a JSON object")
	}
	if _, ok := req["models"]; ok {
		return nil, "", fmt.Errorf("model fallback lists are not allowed")
	}
	var model string
	if err := json.Unmarshal(req["model"], &model); err != nil || model == "" {
		return nil, "", fmt.Errorf("the request names no model")
	}
	usage := map[string]json.RawMessage{}
	if raw, ok := req["usage"]; ok {
		_ = json.Unmarshal(raw, &usage)
	}
	usage["include"] = json.RawMessage("true")
	req["usage"], _ = json.Marshal(usage)
	out, err := json.Marshal(req)
	return out, BaseModel(model), err
}

// APIError answers in the OpenAI error shape, which OpenCode shows to the model and the relay.
func APIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "p0ssess: " + msg, "code": status}})
	w.Write(b)
}

// CostTap passes an answer through while keeping what is needed to read its cost: the last SSE data line
// that mentions usage, or the start of a JSON body. It never keeps the answer's text beyond that.
type CostTap struct {
	http.ResponseWriter
	status    int
	header    bool
	wroteBody bool
	sse       bool
	line      []byte
	lastUsage []byte
	id        string
	jsonBody  bytes.Buffer
}

const maxJSONTee = 16 << 20

// NewCostTap wraps w.
func NewCostTap(w http.ResponseWriter) *CostTap {
	return &CostTap{ResponseWriter: w, status: http.StatusOK}
}

// Status is the answer's status code.
func (t *CostTap) Status() int { return t.status }

// WroteBody reports whether any of the answer's body was written.
func (t *CostTap) WroteBody() bool { return t.wroteBody }

func (t *CostTap) WriteHeader(code int) {
	if t.header {
		return
	}
	t.header = true
	t.status = code
	t.sse = strings.HasPrefix(t.Header().Get("Content-Type"), "text/event-stream")
	t.ResponseWriter.WriteHeader(code)
}

func (t *CostTap) Write(b []byte) (int, error) {
	if !t.header {
		t.WriteHeader(http.StatusOK)
	}
	t.wroteBody = t.wroteBody || len(b) > 0
	if t.sse {
		t.scan(b)
	} else if t.jsonBody.Len()+len(b) <= maxJSONTee {
		t.jsonBody.Write(b)
	}
	return t.ResponseWriter.Write(b)
}

func (t *CostTap) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *CostTap) scan(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(t.line)+len(b) <= 1<<20 {
				t.line = append(t.line, b...)
			}
			return
		}
		t.line = append(t.line, b[:i]...)
		t.sseLine(bytes.TrimRight(t.line, "\r"))
		t.line = t.line[:0]
		b = b[i+1:]
	}
}

func (t *CostTap) sseLine(l []byte) {
	data, ok := bytes.CutPrefix(l, []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if t.id == "" && bytes.Contains(data, []byte(`"id"`)) {
		var c struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &c) == nil {
			t.id = c.ID
		}
	}
	if bytes.Contains(data, []byte(`"usage"`)) {
		t.lastUsage = append(t.lastUsage[:0], data...)
	}
}

type usage struct {
	Cost *float64 `json:"cost"`
	// With the user's own provider key on OpenRouter (BYOK), cost is only OpenRouter's fee and the
	// provider bills the inference itself: upstream_inference_cost. Without BYOK, upstream_inference_cost
	// is already part of cost.
	IsBYOK      bool `json:"is_byok"`
	CostDetails *struct {
		UpstreamInferenceCost *float64 `json:"upstream_inference_cost"`
	} `json:"cost_details"`
	// Chat completions name the token counts prompt_ and completion_tokens; the Responses API input_
	// and output_tokens.
	PromptTokens     float64 `json:"prompt_tokens"`
	CompletionTokens float64 `json:"completion_tokens"`
	InputTokens      float64 `json:"input_tokens"`
	OutputTokens     float64 `json:"output_tokens"`
}

func (u *usage) tokens() (in, out float64) {
	if u == nil {
		return 0, 0
	}
	return u.PromptTokens + u.InputTokens, u.CompletionTokens + u.OutputTokens
}

type usageDoc struct {
	ID    string `json:"id"`
	Usage *usage `json:"usage"`
	// The Responses API reports usage on its final response object.
	Response *struct {
		ID    string `json:"id"`
		Usage *usage `json:"usage"`
	} `json:"response"`
}

func (d usageDoc) usage() *usage {
	if d.Usage != nil {
		return d.Usage
	}
	if d.Response != nil {
		return d.Response.Usage
	}
	return nil
}

// total is what a call cost the user: OpenRouter's charge, plus the provider's own charge on a BYOK call.
func (u *usage) total() (float64, bool) {
	if u == nil || u.Cost == nil || !(*u.Cost >= 0) {
		return 0, false
	}
	c := *u.Cost
	if u.IsBYOK {
		if u.CostDetails == nil || u.CostDetails.UpstreamInferenceCost == nil || !(*u.CostDetails.UpstreamInferenceCost >= 0) {
			// The provider's charge is not reported: the call's cost is not known.
			return 0, false
		}
		c += *u.CostDetails.UpstreamInferenceCost
	}
	return c, true
}

func (d usageDoc) cost() (float64, bool) { return d.usage().total() }

// Reading is what an answer says about its own cost.
type Reading struct {
	// CostUSD is valid when Found.
	CostUSD float64
	Found   bool
	// ID is the generation id, for a lookup when the cost was not in the answer.
	ID string
	// Input and output token counts, when the answer reported them.
	InputTokens, OutputTokens float64
}

func (t *CostTap) doc() (usageDoc, bool) {
	var d usageDoc
	if t.sse {
		if len(t.line) > 0 {
			t.sseLine(bytes.TrimRight(t.line, "\r"))
			t.line = nil
		}
		ok := len(t.lastUsage) > 0 && json.Unmarshal(t.lastUsage, &d) == nil
		d.ID = t.id
		return d, ok
	}
	if json.Unmarshal(t.jsonBody.Bytes(), &d) != nil {
		return usageDoc{}, false
	}
	if d.ID == "" && d.Response != nil {
		d.ID = d.Response.ID
	}
	return d, true
}

// Read returns what the answer said about its cost, once the answer is complete.
func (t *CostTap) Read() Reading {
	d, ok := t.doc()
	r := Reading{ID: d.ID}
	if !ok {
		return r
	}
	r.CostUSD, r.Found = d.cost()
	r.InputTokens, r.OutputTokens = d.usage().tokens()
	return r
}

// Cost returns the answer's cost, its generation id and whether the cost was found.
func (t *CostTap) Cost() (float64, string, bool) {
	r := t.Read()
	return r.CostUSD, r.ID, r.Found
}

// Settle is how a finished call is charged.
type Settle int

const (
	// SettleNone: the call billed nothing (refused upstream before any model ran).
	SettleNone Settle = iota
	// SettleKnown: the cost is known.
	SettleKnown
	// SettleUnknown: the call may have been billed and its cost could not be read.
	SettleUnknown
)

// Outcome decides how a finished call is charged: from the answer, else from a generation lookup (nil
// skips it), else unknown. cancelled says the caller went away while the call was in flight.
func (t *CostTap) Outcome(ctx context.Context, cancelled bool, lookup func(context.Context, string) (float64, bool)) (Settle, Reading) {
	r := t.Read()
	switch {
	case r.Found:
		return SettleKnown, r
	case cancelled, t.status == http.StatusBadGateway && !t.wroteBody:
		// Cancelled, or the upstream connection failed, after the request may have reached OpenRouter:
		// the call may have been billed.
		return SettleUnknown, r
	case t.status >= 400 && !t.wroteBody:
		// An upstream refusal with no body bills nothing.
		return SettleNone, r
	case t.status >= 400 && r.ID == "":
		// OpenRouter errors carry no generation and bill nothing.
		return SettleNone, r
	}
	if r.ID != "" && lookup != nil {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		c, ok := lookup(lctx, r.ID)
		cancel()
		if ok {
			r.CostUSD, r.Found = c, true
			return SettleKnown, r
		}
	}
	return SettleUnknown, r
}

// GenerationLookup returns a lookup that asks OpenRouter (or a test server at upstream) for a
// generation's total cost, retrying while the generation is not recorded yet.
func GenerationLookup(client *http.Client, upstream, key string) func(context.Context, string) (float64, bool) {
	return func(ctx context.Context, id string) (float64, bool) {
		for wait := time.Second; wait <= 4*time.Second; wait *= 2 {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/api/v1/generation?id="+urlQuery(id), nil)
			if err != nil {
				return 0, false
			}
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := client.Do(req)
			if err == nil {
				var g struct {
					Data struct {
						TotalCost *float64 `json:"total_cost"`
						IsBYOK    bool     `json:"is_byok"`
						Upstream  *float64 `json:"upstream_inference_cost"`
					} `json:"data"`
				}
				ok := resp.StatusCode == 200 && json.NewDecoder(bufio.NewReader(io.LimitReader(resp.Body, 1<<20))).Decode(&g) == nil
				resp.Body.Close()
				if ok && g.Data.TotalCost != nil && *g.Data.TotalCost >= 0 {
					if !g.Data.IsBYOK {
						return *g.Data.TotalCost, true
					}
					if g.Data.Upstream != nil && *g.Data.Upstream >= 0 {
						return *g.Data.TotalCost + *g.Data.Upstream, true
					}
				}
			}
			select {
			case <-ctx.Done():
				return 0, false
			case <-time.After(wait):
			}
		}
		return 0, false
	}
}

func urlQuery(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
