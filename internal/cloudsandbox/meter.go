package cloudsandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"p0ssess/internal/proxy"
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

	// The current run: where its spend starts, its billed calls, their known cost, and how many were
	// charged at the rest of the cap because their cost could not be read.
	runStart   float64
	runCalls   int
	runUnknown int
	runKnown   float64

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
	m.runStart, m.runCalls, m.runUnknown, m.runKnown = m.spent, 0, 0, 0
}

// Spent is the session's metered spend.
func (m *Meter) Spent() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent
}

// RunSpend is what the current run (since StartRun) was charged: its billed calls, the sum of their known
// costs, how many were charged at the rest of the cap, and the total charged.
func (m *Meter) RunSpend() (calls int, knownUSD float64, unknown int, chargedUSD float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runCalls, m.runKnown, m.runUnknown, m.spent - m.runStart
}

func (m *Meter) add(usd float64) {
	m.mu.Lock()
	m.spent += usd
	m.runKnown += usd
	m.runCalls++
	m.mu.Unlock()
}

// chargeRest charges what is left of the run's cap, for a call whose cost is unknown.
func (m *Meter) chargeRest() {
	m.mu.Lock()
	if rest := m.limit - m.spent; rest > 0 {
		m.spent += rest
		m.pending += rest
	}
	m.runCalls++
	m.runUnknown++
	m.mu.Unlock()
}

func baseModel(s string) string { return proxy.BaseModel(s) }

// allowedCall is the only set of OpenRouter endpoints a sandbox may reach (sandbox.md, model calls).
func allowedCall(method, p string) bool {
	switch method {
	case http.MethodPost:
		return proxy.ModelCall(method, p)
	case http.MethodGet:
		return p == "/api/v1/models" || strings.HasPrefix(p, "/api/v1/models/") && !strings.Contains(p, "..")
	}
	return false
}

func apiError(w http.ResponseWriter, status int, msg string) { proxy.APIError(w, status, msg) }

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
		body, model, err := proxy.WithUsage(body)
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
		tap := proxy.NewCostTap(w)
		next.ServeHTTP(tap, r)
		switch s, rd := tap.Outcome(r.Context(), r.Context().Err() != nil, m.Lookup); s {
		case proxy.SettleKnown:
			m.add(rd.CostUSD)
		case proxy.SettleUnknown:
			m.chargeRest()
		}
	})
}

// GenerationLookup returns a Meter.Lookup that asks OpenRouter (or a test server at upstream) for a
// generation's total cost, retrying while the generation is not recorded yet.
func GenerationLookup(client *http.Client, upstream, key string) func(context.Context, string) (float64, bool) {
	return proxy.GenerationLookup(client, upstream, key)
}
