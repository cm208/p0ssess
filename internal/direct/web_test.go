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
