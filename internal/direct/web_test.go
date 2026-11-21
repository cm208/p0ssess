package direct

import (
	"context"
	"testing"
)

// A web request carries OpenRouter's web plugin, and its estimate includes the search fee.
func TestWebSearchPlugin(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("EV prices fell 4% this quarter [1]", "")
	req := Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "latest EV battery price news", Web: true, MaxCostUSD: 1, MaxGiven: true}
	plain := req
	plain.Web = false
	pw, err := c.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	pp, _ := c.Plan(context.Background(), plain)
	if d := pw.Estimate().CostUSD - pp.Estimate().CostUSD; d < WebResults*WebUSDPerResult {
		t.Errorf("web adds $%.4f to the estimate", d)
	}
	if _, err := c.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	plugins, _ := f.last("/chat/completions")["plugins"].([]any)
	if len(plugins) != 1 || plugins[0].(map[string]any)["id"] != "web" {
		t.Errorf("plugins %v", plugins)
	}
	r, err := ParseArgs([]byte(`{"prompt":"x","web":true}`))
	if err != nil || !r.Web {
		t.Errorf("%+v %v", r, err)
	}
	if _, err := c.Plan(context.Background(), Request{Model: "recraft/recraft-v4.1-flash", Prompt: "a logo", Output: OutImage, Web: true, MaxCostUSD: 1, MaxGiven: true}); err == nil {
		t.Error("web on the images route")
	}
}

// A model with its own search is priced per search request, not per plugin result (gate run of
// 2026-11-21: gpt-6-luna estimated $0.025, billed $0.045 for about four searches at $0.01).
func TestWebSearchNativePrice(t *testing.T) {
	_, c := newFake(t)
	c.Catalog.Add(ModelInfo{ID: "openai/gpt-6-luna", Input: []string{"text"}, Output: []string{"text"}, Routes: chatRoutes,
		InPerM: 0.1, OutPerM: 0.5, WebSearchUSD: 0.01})
	req := Request{Model: "openai/gpt-6-luna", Prompt: "AI news this week", Web: true, MaxTokens: 1000, MaxCostUSD: 1, MaxGiven: true}
	p, err := c.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if e := p.Estimate(); e.FeeUSD != webNativeSearches*0.01 {
		t.Errorf("fee $%.4f, want $%.4f", e.FeeUSD, webNativeSearches*0.01)
	}
}
