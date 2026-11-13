package cloudsandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeUpstream answers like OpenRouter: a JSON completion, or an SSE stream whose last chunk has usage.
func fakeUpstream(cost string, calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		u, _ := req["usage"].(map[string]any)
		if u["include"] != true {
			http.Error(w, "usage.include missing", 400)
			return
		}
		usage := ""
		if cost != "" {
			usage = `,"usage":{"prompt_tokens":3,"completion_tokens":2,"cost":` + cost + `}`
		}
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: {\"id\":\"gen-1\",\"choices\":[]"+usage+"}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"gen-2","choices":[{"message":{"content":"hi"}}]`+usage+`}`)
	})
}

func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMeterChargesAndStopsAtCap(t *testing.T) {
	var calls atomic.Int32
	m := NewMeter()
	h := m.Guard(fakeUpstream("0.004", &calls))
	m.StartRun(0.01, "openrouter/deepseek/deepseek-v4.1-flash")
	body := `{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`
	for i := 0; i < 3; i++ {
		if w := call(t, h, "POST", "/api/v1/chat/completions", body); w.Code != 200 {
			t.Fatalf("call %d: %d %s", i, w.Code, w.Body)
		}
	}
	if got := m.Spent(); got < 0.0119 || got > 0.0121 {
		t.Fatalf("spent %v", got)
	}
	w := call(t, h, "POST", "/api/v1/chat/completions", body)
	if w.Code != http.StatusPaymentRequired || calls.Load() != 3 {
		t.Fatalf("over the cap: %d after %d upstream calls", w.Code, calls.Load())
	}
	// The next run gets its own cap on top of what was spent.
	m.StartRun(0.005, "deepseek/deepseek-v4.1-flash")
	if w := call(t, h, "POST", "/api/v1/chat/completions", body); w.Code != 200 {
		t.Fatalf("next run: %d", w.Code)
	}
}

func TestMeterReadsStreamedUsage(t *testing.T) {
	var calls atomic.Int32
	m := NewMeter()
	h := m.Guard(fakeUpstream("0.25", &calls))
	m.StartRun(1, "x/y")
	w := call(t, h, "POST", "/api/v1/chat/completions", `{"model":"x/y","stream":true}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if m.Spent() != 0.25 {
		t.Fatalf("spent %v", m.Spent())
	}
}

func TestMeterUnknownCost(t *testing.T) {
	var calls atomic.Int32
	m := NewMeter()
	h := m.Guard(fakeUpstream("", &calls))
	m.StartRun(0.5, "x/y")
	var looked string
	m.Lookup = func(_ context.Context, id string) (float64, bool) { looked = id; return 0.02, true }
	call(t, h, "POST", "/api/v1/chat/completions", `{"model":"x/y","stream":true}`)
	if looked != "gen-1" || m.Spent() != 0.02 {
		t.Fatalf("lookup %q spent %v", looked, m.Spent())
	}
	m.Lookup = func(context.Context, string) (float64, bool) { return 0, false }
	call(t, h, "POST", "/api/v1/chat/completions", `{"model":"x/y"}`)
	if m.Spent() != 0.5 {
		t.Fatalf("an unmetered call must use up the run's cap, spent %v", m.Spent())
	}
	if w := call(t, h, "POST", "/api/v1/chat/completions", `{"model":"x/y"}`); w.Code != http.StatusPaymentRequired {
		t.Fatalf("after an unmetered call: %d", w.Code)
	}
}

func TestMeterRefusesWhatTheHostMayNotAsk(t *testing.T) {
	var calls atomic.Int32
	m := NewMeter()
	h := m.Guard(fakeUpstream("0.001", &calls))
	m.StartRun(1, "x/y")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/key", ""},
		{"GET", "/api/v1/credits", ""},
		{"GET", "/api/v1/generation?id=1", ""},
		{"POST", "/api/v1/keys", `{"model":"x/y"}`},
		{"DELETE", "/api/v1/chat/completions", ""},
		{"POST", "/api/v1/chat/completions", `{"model":"openai/o9-pro"}`},
		{"POST", "/api/v1/chat/completions", `{"model":"x/y","models":["openai/o9-pro"]}`},
		{"POST", "/api/v1/chat/completions", `{"messages":[]}`},
		{"POST", "/api/v1/chat/completions", `not json`},
	} {
		w := call(t, h, c.method, c.path, c.body)
		if w.Code < 400 {
			t.Errorf("%s %s %s: %d", c.method, c.path, c.body, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d refused calls reached the upstream", calls.Load())
	}
	m2 := NewMeter()
	if w := call(t, m2.Guard(fakeUpstream("0", &calls)), "POST", "/api/v1/chat/completions", `{"model":"x/y"}`); w.Code != http.StatusForbidden {
		t.Fatalf("before any run: %d", w.Code)
	}
}

func TestGenerationLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Query().Get("id") != "gen-abc" {
			http.Error(w, "no", 401)
			return
		}
		io.WriteString(w, `{"data":{"total_cost":0.0031}}`)
	}))
	defer srv.Close()
	c, ok := GenerationLookup(srv.Client(), srv.URL, "k")(context.Background(), "gen-abc")
	if !ok || c != 0.0031 {
		t.Fatalf("%v %v", c, ok)
	}
}
