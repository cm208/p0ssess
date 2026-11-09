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

// TestZeroPriceIsUnknownUnlessFree covers M6: a zero price is free only for a ":free" variant or a text
// model the snapshot lists at zero; a zero from the live list or an endpoint is unknown.
func TestZeroPriceIsUnknownUnlessFree(t *testing.T) {
	z := func() *float64 { v := 0.0; return &v }
	p := func(v float64) *float64 { return &v }
	snapshotZero := &Model{ID: "a/zero", Kind: "text", Pricing: Pricing{InputPerM: z(), OutputPerM: z()}}
	liveZero := &Model{ID: "a/live-zero", Kind: "text", Live: true, Pricing: Pricing{InputPerM: z(), OutputPerM: z()}}
	liveFree := &Model{ID: "a/live-zero:free", Kind: "text", Live: true, Pricing: Pricing{InputPerM: z(), OutputPerM: z()}}
	music := &Model{ID: "a/music", Kind: "audio", Pricing: Pricing{InputPerM: z(), OutputPerM: z()}}
	noPrice := &Model{ID: "a/none", Kind: "text"}
	for _, c := range []struct {
		m           *Model
		free, known bool
	}{{snapshotZero, true, true}, {liveZero, false, false}, {liveFree, true, true}, {music, false, false}, {noPrice, false, false}} {
		_, ok := c.m.Prices(false, false)
		if c.m.Free() != c.free || c.m.PriceKnown() != c.known || ok != c.known {
			t.Errorf("%s: free %v known %v prices ok %v, want free %v known %v", c.m.ID, c.m.Free(), c.m.PriceKnown(), ok, c.free, c.known)
		}
	}
	if (*Model)(nil).PriceKnown() {
		t.Error("a model missing from the registry has a known price")
	}

	// A zero-priced endpoint must not undercut the list price of a paid model.
	paid := &Model{ID: "a/paid", Kind: "text", Pricing: Pricing{InputPerM: p(1), OutputPerM: p(4)},
		Endpoints: &Endpoints{Cheapest: &Endpoint{Provider: "X", InputPerM: z(), OutputPerM: z()},
			CheapestZDR: &Endpoint{Provider: "Y", InputPerM: z(), OutputPerM: z()}}}
	if tp, ok := paid.Prices(false, true); !ok || tp.In != 1 {
		t.Errorf("cheapest price of a paid model = %+v %v, want the list price", tp, ok)
	}
	if _, ok := paid.Prices(true, true); ok {
		t.Error("a zero-priced zero-retention endpoint counted as a known price")
	}
}

func TestLiveZeroKeepsSnapshotPrice(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "live.json")
	os.WriteFile(cache, []byte(`{"fetched_at":"2026-11-09T00:00:00Z","models":[
 {"id":"anthropic/claude-haiku-5.5","pricing":{"prompt":"0","completion":"0"}},
 {"id":"newco/zero","architecture":{"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}},
 {"id":"newco/zero:free","architecture":{"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}}]}`), 0o600)
	r := load(t)
	if err := r.MergeLive(cache); err != nil {
		t.Fatal(err)
	}
	if h := r.Get("anthropic/claude-haiku-5.5"); *h.Pricing.InputPerM != 0.1 || h.Free() {
		t.Errorf("a live zero replaced haiku's snapshot price: %v", *h.Pricing.InputPerM)
	}
	if r.Get("newco/zero").PriceKnown() {
		t.Error("a live-only model listed at zero has a known price")
	}
	if !r.Get("newco/zero:free").Free() {
		t.Error("a live :free model is not free")
	}
}
