package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess"
)

func load(t *testing.T) *Registry {
	t.Helper()
	r, err := Load(p0ssess.Data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadSnapshot(t *testing.T) {
	r := load(t)
	if len(r.Models) != 63 {
		t.Fatalf("want 63 models, got %d", len(r.Models))
	}
	h := r.Get("anthropic/claude-haiku-5.5")
	if h == nil || !h.HasInput("image") || h.HasInput("audio") {
		t.Fatalf("haiku modalities wrong: %+v", h)
	}
	if h.Policy() != PolicyZDR {
		t.Errorf("haiku policy = %q", h.Policy())
	}
	g := r.Get("google/gemini-3.8-flash")
	if g.Endpoints.HasZDR {
		t.Error("gemini 3.8 flash should have no ZDR endpoint")
	}
}

func TestPrices(t *testing.T) {
	r := load(t)
	g := r.Get("google/gemini-3.8-flash")
	list, _ := g.Prices(false, false)
	cheap, _ := g.Prices(false, true)
	if list.In != 0.825 || cheap.In != 0.375 {
		t.Fatalf("list %.3f cheapest %.3f", list.In, cheap.In)
	}
	if _, ok := g.Prices(true, true); ok {
		t.Error("gemini should have no ZDR price")
	}
	h := r.Get("anthropic/claude-haiku-5.5")
	tp, _ := h.Prices(false, false)
	long := h.ForPrompt(tp, 150_000)
	if long.In != 0.5 || long.Out != 2.5 {
		t.Errorf("haiku long-context = %+v", long)
	}
	if short := h.ForPrompt(tp, 50_000); short.In != 0.1 {
		t.Errorf("short prompt should keep base price, got %v", short.In)
	}
	if _, ok := r.Get("cohere/rerank-5-fast").Prices(false, false); ok {
		t.Error("rerank 0/0 price should count as unknown")
	}
}

const liveFixture = `{"data":[
 {"id":"anthropic/claude-haiku-5.5","name":"Haiku","context_length":1000000,
  "architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.0000002","completion":"0.000001","input_cache_read":"0.00000002"},
  "supported_parameters":["tools","response_format"],
  "benchmarks":{"artificial_analysis":{"score":99}}},
 {"id":"newco/new-model","name":"New","context_length":32000,
  "architecture":{"input_modalities":["text"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.000001","completion":"0.000002"},
  "supported_parameters":["tools"]}
]}`

func TestRefreshAndMerge(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(liveFixture))
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "live.json")
	fetched, err := Refresh(context.Background(), srv.Client(), cache, time.Hour, srv.URL)
	if err != nil || !fetched {
		t.Fatalf("refresh: %v %v", fetched, err)
	}
	fetched, _ = Refresh(context.Background(), srv.Client(), cache, time.Hour, srv.URL)
	if fetched || calls != 1 {
		t.Fatalf("fresh cache should not refetch (calls=%d)", calls)
	}
	raw, _ := os.ReadFile(cache)
	if strings.Contains(string(raw), "artificial_analysis") {
		t.Error("benchmarks object must not be cached")
	}
	r := load(t)
	if err := r.MergeLive(cache); err != nil {
		t.Fatal(err)
	}
	if got := *r.Get("anthropic/claude-haiku-5.5").Pricing.InputPerM; got < 0.199 || got > 0.201 {
		t.Errorf("merged price = %v", got)
	}
	n := r.Get("newco/new-model")
	if n == nil || !n.Live || n.Endpoints != nil || !n.Caps.Tools {
		t.Fatalf("new model not merged: %+v", n)
	}
	if err := (&Registry{Models: map[string]*Model{}}).MergeLive(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Error("missing cache should be a no-op")
	}
}
