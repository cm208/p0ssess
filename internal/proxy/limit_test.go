package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutputLimitFromCap: a run capped at $0.50 on a $50-per-million-output model asks for at most what
// the rest of the cap buys, not the model's whole 128000 tokens (1.0.4 item 14: OpenRouter reserved
// about $6.40 against the key and refused the run).
func TestOutputLimitFromCap(t *testing.T) {
	astra := Rates{InPerM: 10, OutPerM: 50}
	n := OutputLimit(0.5, 4000, astra)
	if n <= MinOutputTokens || n >= 10000 || float64(n)*astra.OutPerM/1e6 > 0.5 {
		t.Fatalf("limit = %d", n)
	}
	if OutputLimit(0.5, 0, Rates{}) != 0 || OutputLimit(0, 0, astra) != 0 {
		t.Error("no price or no cap means no limit")
	}
	if OutputLimit(0.001, 0, astra) != MinOutputTokens {
		t.Error("the limit never goes below MinOutputTokens")
	}

	for _, c := range []struct{ path, body, field string }{
		{"/api/v1/chat/completions", `{"model":"a/b","max_completion_tokens":128000}`, "max_completion_tokens"},
		{"/api/v1/chat/completions", `{"model":"a/b"}`, "max_tokens"},
		{"/api/v1/responses", `{"model":"a/b","max_output_tokens":128000}`, "max_output_tokens"},
	} {
		var got map[string]any
		json.Unmarshal(CapOutput("POST", c.path, []byte(c.body), n), &got)
		if v, _ := got[c.field].(float64); int(v) != n {
			t.Errorf("%s %s: %s = %v, want %d", c.path, c.body, c.field, got[c.field], n)
		}
	}
	small := `{"model":"a/b","max_tokens":500}`
	if string(CapOutput("POST", "/api/v1/chat/completions", []byte(small), n)) != small {
		t.Error("a limit already below the cap's must be kept")
	}
	if string(CapOutput("GET", "/api/v1/models", []byte(small), 10)) != small {
		t.Error("only model calls are changed")
	}
}

// TestMeterSendsTheCapsOutputLimit: the jail's key proxy rewrites OpenCode's max_completion_tokens.
func TestMeterSendsTheCapsOutputLimit(t *testing.T) {
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&seen)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"g","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"cost":0.001}}`))
	}))
	t.Cleanup(up.Close)
	dir := t.TempDir()
	m := &Meter{Dir: dir, RunFile: filepath.Join(dir, "run.json")}
	h := m.Wrap(Handler(up.URL, "k", false))
	if err := WriteRunSpec(m.RunFile, RunSpec{Run: "r", CapUSD: 0.5, Model: "openai/gpt-6-astra", Rates: Rates{InPerM: 10, OutPerM: 50}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-6-astra","max_completion_tokens":128000,"messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	v, _ := seen["max_completion_tokens"].(float64)
	if v <= 0 || v*50/1e6 > 0.5 {
		t.Fatalf("upstream got max_completion_tokens %v", seen["max_completion_tokens"])
	}
}
