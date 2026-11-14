package server

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/local"
)

// The direct tool's await_outcome holds the run; recording an outcome for its run id releases it.
func TestDirectAwaitOutcome(t *testing.T) {
	withFake(t, &fakeRouter{chat: func() (int, string) { return 200, okChat }})
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	tool := &directTool{cfg: cfg, eng: eng}
	if !strings.Contains(string(tool.schema()), `"await_outcome"`) {
		t.Fatal("schema has no await_outcome")
	}
	args, _ := json.Marshal(map[string]any{"model": testModel, "prompt": "hi", "max_tokens": 10, "use_case": "video-understand",
		"stakes": 2, "await_outcome": true})
	out := tool.call(context.Background(), args, nil)
	if out.IsError {
		t.Fatal(out.Content[0].Text)
	}
	m := regexp.MustCompile(`run (d-[0-9a-f]+)`).FindStringSubmatch(out.Content[0].Text)
	if m == nil {
		t.Fatalf("no run id in %q", out.Content[0].Text)
	}
	l, _ := ledger(t, eng)
	var run history.Entry
	for _, e := range l.Entries {
		if e.Type == history.TypeRun {
			run = e
		}
	}
	if run.RunID != m[1] || !run.AwaitOutcome || run.Stakes != 2 {
		t.Fatalf("run %+v", run)
	}
	if p := eng.PendingOutcomes(); len(p) != 0 {
		t.Fatalf("held run is pending: %+v", p)
	}
	if _, err := eng.RecordOutcome(local.Outcome{RunID: m[1], Model: testModel, Outcome: history.Declined}); err != nil {
		t.Fatal(err)
	}
	if p := eng.PendingOutcomes(); len(p) != 1 || p[0].Outcome != history.Declined || p[0].Stakes != 2 {
		t.Fatalf("pending after rating: %+v", p)
	}
}
