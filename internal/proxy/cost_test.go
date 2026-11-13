package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCostCountsBYOK: with the user's own provider key on OpenRouter, "cost" is only OpenRouter's fee
// (often 0) and the provider bills upstream_inference_cost; without BYOK the upstream figure is already
// part of cost and must not be added twice.
func TestCostCountsBYOK(t *testing.T) {
	for _, c := range []struct {
		body string
		want float64
		ok   bool
	}{
		{`{"id":"g","usage":{"cost":0.00001184,"is_byok":false,"cost_details":{"upstream_inference_cost":0.00001184}}}`, 0.00001184, true},
		{`{"id":"g","usage":{"cost":0,"is_byok":true,"cost_details":{"upstream_inference_cost":0.0000195}}}`, 0.0000195, true},
		{`{"id":"g","usage":{"cost":0.000001,"is_byok":true,"cost_details":{"upstream_inference_cost":0.0000195}}}`, 0.0000205, true},
		{`{"id":"g","usage":{"cost":0,"is_byok":true}}`, 0, false},
		{`{"id":"g","usage":{"prompt_tokens":3}}`, 0, false},
	} {
		tap := NewCostTap(httptest.NewRecorder())
		tap.Header().Set("Content-Type", "application/json")
		io.WriteString(tap, c.body)
		got, _, ok := tap.Cost()
		if ok != c.ok || got < c.want-1e-12 || got > c.want+1e-12 {
			t.Errorf("%s: %v %v", c.body, got, ok)
		}
	}
}

func TestGenerationLookupBYOK(t *testing.T) {
	answer := `{"data":{"is_byok":true,"total_cost":0,"upstream_inference_cost":0.0000195}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, answer) }))
	defer srv.Close()
	c, ok := GenerationLookup(srv.Client(), srv.URL, "k")(context.Background(), "gen-1")
	if !ok || c != 0.0000195 {
		t.Fatalf("byok lookup: %v %v", c, ok)
	}
	answer = `{"data":{"is_byok":false,"total_cost":0.003,"upstream_inference_cost":0.003}}`
	if c, ok := GenerationLookup(srv.Client(), srv.URL, "k")(context.Background(), "gen-1"); !ok || c != 0.003 {
		t.Fatalf("plain lookup: %v %v", c, ok)
	}
}
