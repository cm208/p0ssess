package cloudsandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Meter is the cost guard for model calls that come from a cloud sandbox. The sandbox host is not
// trusted: every call is checked here, on the laptop, before the key is added. A run may spend its own
// cap on top of what the session spent before it; calls go one at a time, so a run overshoots its cap by
// at most the one call that crossed it.
type Meter struct {
	mu      sync.Mutex
	spent   float64
	limit   float64
	model   string
	pending float64 // charged for calls whose cost could not be read

	one chan struct{}

	// Lookup reads a generation's cost from OpenRouter when the answer carried none. Nil skips the lookup.
	Lookup func(ctx context.Context, id string) (float64, bool)
}

func NewMeter() *Meter { return &Meter{one: make(chan struct{}, 1)} }

// StartRun lets the session spend capUSD more, on model (an OpenRouter id, without "openrouter/").
func (m *Meter) StartRun(capUSD float64, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if capUSD < 0 || capUSD != capUSD {
		capUSD = 0
	}
	m.limit = m.spent + capUSD
	m.model = baseModel(model)
}

// Spent is the session's metered spend.
func (m *Meter) Spent() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent
}

func (m *Meter) add(usd float64) {
	m.mu.Lock()
	m.spent += usd
	m.mu.Unlock()
}

// chargeRest charges what is left of the run's cap, for a call whose cost is unknown.
func (m *Meter) chargeRest() {
	m.mu.Lock()
	if rest := m.limit - m.spent; rest > 0 {
		m.spent += rest
		m.pending += rest
	}
	m.mu.Unlock()
}

func baseModel(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "openrouter/")
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// allowedCall is the only set of OpenRouter endpoints a sandbox may reach (sandbox.md, model calls).
func allowedCall(method, p string) bool {
	switch method {
	case http.MethodPost:
		return p == "/api/v1/chat/completions" || p == "/api/v1/completions" || p == "/api/v1/responses"
	case http.MethodGet:
		return p == "/api/v1/models" || strings.HasPrefix(p, "/api/v1/models/") && !strings.Contains(p, "..")
	}
	return false
}

// apiError answers in the OpenAI error shape, which OpenCode shows to the model and the relay.
func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "p0ssess: " + msg, "code": status}})
	w.Write(b)
}

const maxModelBody = 64 << 20

// Guard wraps next (the key proxy) with the endpoint allowlist, the model check, the cost cap and the
// metering of each answer.
func (m *Meter) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedCall(r.Method, r.URL.Path) {
			apiError(w, http.StatusForbidden, "this endpoint is not available to the sandbox")
			return
		}
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxModelBody+1))
		if err != nil || len(body) > maxModelBody {
			apiError(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		body, model, err := withUsage(body)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		select {
		case m.one <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		defer func() { <-m.one }()
		m.mu.Lock()
		allowed, over := m.model, m.spent >= m.limit
		m.mu.Unlock()
		if allowed == "" || model != allowed {
			apiError(w, http.StatusForbidden, fmt.Sprintf("model %q is not this run's model", model))
			return
		}
		if over {
			apiError(w, http.StatusPaymentRequired, "this run's cost cap is reached")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Del("Content-Encoding")
		tw := &teeWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tw, r)
		m.settle(r.Context(), tw)
	})
}

// withUsage checks that a model request names exactly one model and asks OpenRouter to report its cost.
func withUsage(body []byte) ([]byte, string, error) {
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
	return out, baseModel(model), err
}

// settle adds the call's cost: from the answer, else from a generation lookup, else the rest of the cap.
func (m *Meter) settle(ctx context.Context, tw *teeWriter) {
	cost, id, found := tw.cost()
	switch {
	case found:
		m.add(cost)
	case ctx.Err() != nil, tw.status == http.StatusBadGateway && !tw.wroteBody:
		// Cancelled, or the upstream connection failed, after the request may have reached OpenRouter:
		// the call may have been billed, so it counts as the rest of the cap.
		m.chargeRest()
	case tw.status >= 400 && !tw.wroteBody:
		// An upstream refusal with no body bills nothing.
	case tw.status >= 400 && id == "":
		// OpenRouter errors carry no generation and bill nothing.
	default:
		if id != "" && m.Lookup != nil {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			c, ok := m.Lookup(lctx, id)
			cancel()
			if ok {
				m.add(c)
				return
			}
		}
		m.chargeRest()
	}
}

// teeWriter passes the answer through while keeping what is needed to read its cost: the last SSE data
// lines that mention usage, or the start of a JSON body.
type teeWriter struct {
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

func (t *teeWriter) WriteHeader(code int) {
	if t.header {
		return
	}
	t.header = true
	t.status = code
	t.sse = strings.HasPrefix(t.Header().Get("Content-Type"), "text/event-stream")
	t.ResponseWriter.WriteHeader(code)
}

func (t *teeWriter) Write(b []byte) (int, error) {
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

func (t *teeWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *teeWriter) scan(b []byte) {
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

func (t *teeWriter) sseLine(l []byte) {
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

type usageDoc struct {
	ID    string `json:"id"`
	Usage *struct {
		Cost *float64 `json:"cost"`
	} `json:"usage"`
	// The Responses API reports usage on its final response object.
	Response *struct {
		ID    string `json:"id"`
		Usage *struct {
			Cost *float64 `json:"cost"`
		} `json:"usage"`
	} `json:"response"`
}

func (d usageDoc) cost() (float64, bool) {
	if d.Usage != nil && d.Usage.Cost != nil && *d.Usage.Cost >= 0 {
		return *d.Usage.Cost, true
	}
	if d.Response != nil && d.Response.Usage != nil && d.Response.Usage.Cost != nil && *d.Response.Usage.Cost >= 0 {
		return *d.Response.Usage.Cost, true
	}
	return 0, false
}

func (t *teeWriter) cost() (float64, string, bool) {
	if t.sse {
		if len(t.line) > 0 {
			t.sseLine(bytes.TrimRight(t.line, "\r"))
			t.line = nil
		}
		var d usageDoc
		if len(t.lastUsage) > 0 && json.Unmarshal(t.lastUsage, &d) == nil {
			if c, ok := d.cost(); ok {
				return c, t.id, true
			}
		}
		return 0, t.id, false
	}
	var d usageDoc
	if json.Unmarshal(t.jsonBody.Bytes(), &d) != nil {
		return 0, "", false
	}
	id := d.ID
	if id == "" && d.Response != nil {
		id = d.Response.ID
	}
	c, ok := d.cost()
	return c, id, ok
}

// GenerationLookup returns a Meter.Lookup that asks OpenRouter (or a test server at upstream) for a
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
					} `json:"data"`
				}
				ok := resp.StatusCode == 200 && json.NewDecoder(bufio.NewReader(io.LimitReader(resp.Body, 1<<20))).Decode(&g) == nil
				resp.Body.Close()
				if ok && g.Data.TotalCost != nil && *g.Data.TotalCost >= 0 {
					return *g.Data.TotalCost, true
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
