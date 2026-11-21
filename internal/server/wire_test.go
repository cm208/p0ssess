package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"p0ssess/internal/opencode"
	"p0ssess/internal/relay"
)

// The relay's direct path answers with web search when asked, and its notes stay out of the result
// text: they go to the notices file (1.1 decision 2).
func TestDirectRunnerWebAndNotices(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.10\ndaily_usd = -1\n")
	run := directRunner(cfg, eng)
	res, err := run(context.Background(), relay.DirectTask{Task: "latest EV battery price news", Web: true, Model: testModel})
	if err != nil || res.Answer != "hi" || res.Route != "direct" || res.RunID == "" {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	body := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	if !strings.Contains(body, `"id":"web"`) {
		t.Errorf("no web plugin in %s", body)
	}
	notice(eng, "r1", []string{"picked inside your limits; gpt-6.1-sol would cost $0.42"}, "daily limit 80% used")
	b, _ := os.ReadFile(eng.Dirs.NoticesFile())
	if !strings.Contains(string(b), "gpt-6.1-sol") || !strings.Contains(string(b), "daily limit") {
		t.Errorf("notices %s", b)
	}
}

// The receipt is one line: model, cost and the saving against the same tokens on Claude.
func TestReceipt(t *testing.T) {
	_, eng, _ := directEnv(t, "monthly_usd = 1\n")
	got := receipt(eng, "moonshotai/kimi-k3.1-pro", 0.10, false, nil)
	if got != "(kimi-k3.1-pro, $0.10)" {
		t.Errorf("no tokens: %q", got)
	}
	if _, ok := eng.TokenCost(claudeBaseline, 1e6, 1e5, 0, 0); !ok {
		t.Skip("the test route table has no Claude prices")
	}
	got = receipt(eng, "moonshotai/kimi-k3.1-pro", 0.10, false, &opencode.Tokens{Input: 1e6, Output: 1e5})
	if !strings.HasPrefix(got, "(kimi-k3.1-pro, $0.10, saved ~$") || !strings.HasSuffix(got, ")") {
		t.Errorf("receipt %q", got)
	}
}
