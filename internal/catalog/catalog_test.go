package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const listJSON = `{"data":[
 {"id":"google/gemini-4-carbon","name":"Google: Gemini 4 Carbon","context_length":1048576,
  "architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001","web_search":"0.014"},
  "top_provider":{"context_length":1048576,"max_completion_tokens":943718},
  "endpoints":["/api/v1/chat/completions","/api/v1/responses"]},
 {"id":"openai/gpt-6-astra","context_length":1050000,
  "architecture":{"input_modalities":["text"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.00001","completion":"0.00005","overrides":[{"min_prompt_tokens":272000,"prompt":"0.00002","completion":"0.000075"}]},
  "top_provider":{"max_completion_tokens":128000}},
 {"id":"x/variable","architecture":{"output_modalities":["image"]},"pricing":{"prompt":"-1","completion":"-1"}}
]}`

func fake(t *testing.T, hits *atomic.Int32, up *atomic.Bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !up.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(listJSON))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCacheAndOfflineFallback(t *testing.T) {
	var hits atomic.Int32
	var up atomic.Bool
	up.Store(true)
	srv := fake(t, &hits, &up)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s := &Store{Dir: dir, URL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}

	m := s.Find(context.Background(), "google/gemini-4-carbon")
	if m == nil || hits.Load() != 1 {
		t.Fatalf("first Find: %v, hits %d", m, hits.Load())
	}
	tm := m.Table()
	if !tm.Live || tm.Kind != "text" || *tm.Pricing.InputPerM != 2 || *tm.Pricing.OutputPerM != 10 || *tm.Pricing.CacheReadPerM != 0.1 {
		t.Errorf("converted = %+v", tm)
	}
	if tm.MaxOutput == nil || *tm.MaxOutput != 943718 || *tm.Context != 1048576 || len(tm.Routes) != 2 {
		t.Errorf("limits/routes = %v %v %v", tm.MaxOutput, tm.Context, tm.Routes)
	}
	astra := s.Get("openai/gpt-6-astra").Table()
	if len(astra.Pricing.LongContext) != 1 || astra.Pricing.LongContext[0].CompletionPerM != 75 || astra.Routes[0] != "/api/v1/chat/completions" {
		t.Errorf("astra = %+v", astra.Pricing)
	}
	if v := s.Get("x/variable").Table(); v.Pricing.InputPerM != nil || v.Kind != "image" {
		t.Errorf("a -1 price must be unknown: %+v", v)
	}

	// Cached: no fetch for a listed id; a missing id refetches only after MissRetry.
	s.Find(context.Background(), "openai/gpt-6-astra")
	if s.Find(context.Background(), "nobody/new") != nil || hits.Load() != 1 {
		t.Errorf("missing id fetched again within MissRetry: hits %d", hits.Load())
	}
	now = now.Add(MissRetry + time.Minute)
	s.Find(context.Background(), "nobody/new")
	if hits.Load() != 2 {
		t.Errorf("missing id after MissRetry: hits %d", hits.Load())
	}

	// A day later the list is due; with OpenRouter down the cached copy still answers.
	up.Store(false)
	now = now.Add(MaxAge)
	fresh := &Store{Dir: dir, URL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now }}
	if !fresh.Due() {
		t.Error("a day-old list is not due")
	}
	if m := fresh.Find(context.Background(), "google/gemini-4-carbon"); m == nil {
		t.Error("offline: the cached list was not used")
	}
	if len(fresh.Table()) != 3 {
		t.Errorf("cached table = %d models", len(fresh.Table()))
	}
}

func TestNoFetchInTests(t *testing.T) {
	t.Setenv("P0SSESS_MODELS_URL", "")
	if URL() != "" {
		t.Error("tests must not reach OpenRouter unless they set P0SSESS_MODELS_URL")
	}
	t.Setenv("P0SSESS_MODELS_URL", "off")
	if URL() != "" {
		t.Error("off")
	}
	if (&Store{Dir: t.TempDir()}).Refresh(context.Background(), true) == nil {
		t.Error("refresh without a URL must fail")
	}
}
