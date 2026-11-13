package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/calibrate"
	"p0ssess/internal/history"
	"p0ssess/internal/outcomes"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
)

type fakeCal struct {
	mu    sync.Mutex
	calls []calibrate.Call
}

// Run answers every sample with its own key's answer on the first model and a wrong one elsewhere, at
// a tenth of a cent a call.
func (f *fakeCal) Run(_ context.Context, c calibrate.Call) (calibrate.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if c.DryRun {
		return calibrate.Reply{EstimateUSD: 0.001}, nil
	}
	return calibrate.Reply{Text: "ANSWER: nope", CostUSD: 0.001, Elapsed: time.Second}, nil
}

func newTestCalibrator(t *testing.T, pro bool) (*calibrator, *bytes.Buffer, *[][]byte) {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()
	eng, err := local.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if pro {
		account.SaveToken(d, "p0d_test")
		os.MkdirAll(d.State, 0o700)
		os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)
	}
	var out bytes.Buffer
	var sent [][]byte
	c := &calibrator{d: d, eng: eng, runner: &fakeCal{}, now: time.Now, out: &out, userDir: calibrate.UserDir(d),
		put: func(_ context.Context, b []byte) error { sent = append(sent, b); return nil }}
	return c, &out, &sent
}

func TestCalibrateFreePlan(t *testing.T) {
	c, out, _ := newTestCalibrator(t, false)
	if err := c.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "part of p0ssess Pro") || len(c.runner.(*fakeCal).calls) != 0 {
		t.Errorf("free plan: %q", out)
	}
}

func TestCalibratePlanRunUpload(t *testing.T) {
	c, out, sent := newTestCalibrator(t, true)
	ctx := context.Background()
	if err := c.run(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	plan := out.String()
	if !strings.Contains(plan, "calibrate  ") || !strings.Contains(plan, "p0ssess calibrate run --max-usd") {
		t.Fatalf("plan output: %s", plan)
	}
	for _, call := range c.runner.(*fakeCal).calls {
		if !call.DryRun {
			t.Fatal("plan spent money")
		}
	}
	if err := c.run(ctx, []string{"run"}); err == nil {
		t.Error("run without --max-usd")
	}
	bud, _ := c.eng.Budget()
	if err := c.run(ctx, []string{"run", "--max-usd", "1000"}); err == nil || !strings.Contains(err.Error(), "auto-approve") || bud.PerTaskUSD >= 1000 {
		t.Errorf("above the limit without --yes: %v", err)
	}
	if err := c.run(ctx, []string{"run", "--max-usd", "0.0001", "--yes"}); err == nil {
		t.Error("ceiling below the estimate accepted")
	}
	out.Reset()
	if err := c.run(ctx, []string{"run", "--max-usd", "1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "measured") || len(*sent) != 1 {
		t.Fatalf("run output: %s (uploads %d)", out, len(*sent))
	}
	var body struct {
		V      int               `json:"v"`
		Scores []json.RawMessage `json:"scores"`
	}
	if err := json.Unmarshal((*sent)[0], &body); err != nil || body.V != 1 || len(body.Scores) == 0 {
		t.Fatalf("upload %s", (*sent)[0])
	}
	for _, row := range body.Scores {
		var m map[string]any
		json.Unmarshal(row, &m)
		if len(m) != 4 {
			t.Errorf("a score row carries more than its four fields: %s", row)
		}
	}
	// Nothing changed in the routes, so a second plan is up to date.
	out.Reset()
	c.run(ctx, []string{"plan"})
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("second plan: %s", out)
	}
	r, err := c.Report()
	if err != nil || !r.Pro || len(r.Models) == 0 || r.Baselined.IsZero() == false {
		t.Errorf("report %+v %v", r, err)
	}
	pdf := filepath.Join(t.TempDir(), "s.pdf")
	if err := c.run(ctx, []string{"report", "--pdf", pdf}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(pdf); !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Error("not a PDF")
	}
	out.Reset()
	if err := c.run(ctx, []string{"forget"}); err != nil || (*sent)[len(*sent)-1] != nil {
		t.Errorf("forget: %v", err)
	}
	if rs, _ := calibrate.LoadResults(c.d); len(rs.List) != 0 {
		t.Error("forget left results")
	}
}

func TestCalibrateUploadPending(t *testing.T) {
	c, out, _ := newTestCalibrator(t, true)
	c.put = func(context.Context, []byte) error { return os.ErrDeadlineExceeded }
	if err := c.run(context.Background(), []string{"run", "--max-usd", "1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !calibrate.LoadMeta(c.d).Pending || !strings.Contains(out.String(), "next start") {
		t.Errorf("pending not kept: %s", out)
	}
	var got []byte
	calibrate.SendPending(context.Background(), c.d, true, func(_ context.Context, b []byte) error { got = b; return nil })
	if got == nil || calibrate.LoadMeta(c.d).Pending {
		t.Error("pending upload not retried")
	}
}

func TestCalibrateBaselineDryRunAndFake(t *testing.T) {
	c, out, _ := newTestCalibrator(t, true)
	ctx := context.Background()
	if err := c.run(ctx, []string{"baseline", "--dry-run", "--use-case", "audio-events"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claude -p") || !strings.Contains(out.String(), "--disallowedTools mcp__plugin_p0ssess_p0ssess") {
		t.Fatalf("dry run: %s", out)
	}
	rec, err := os.ReadFile("../../internal/calibrate/testdata/stream-with.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	c.claude = func(_ context.Context, dir string, args, env []string) ([]byte, error) {
		ran++
		return rec, nil
	}
	out.Reset()
	if err := c.run(ctx, []string{"baseline", "--use-case", "audio-events"}); err != nil {
		t.Fatal(err)
	}
	bs, _ := calibrate.LoadBaselines(c.d)
	if ran != 2 || len(bs.Runs) != 2 || !bs.Runs[1].Correct {
		t.Fatalf("baseline: ran %d, %+v\n%s", ran, bs.Runs, out)
	}
	r, _ := c.Report()
	if len(r.Kinds) != 1 || r.Kinds[0].Alone == nil || r.Kinds[0].With == nil {
		t.Errorf("report kinds %+v", r.Kinds)
	}
}

func TestCalibrationRequestCapsReplyAndLowersReasoning(t *testing.T) {
	for _, c := range []calibrate.Call{
		{UseCase: "audio-transcribe", Model: "google/gemini-3.8-flash", Prompt: "p", File: "/x/a.wav", Transcript: true},
		{UseCase: "data-extract-structured", Model: "deepseek/deepseek-v4.1-flash", Prompt: "p"},
		{UseCase: "data-extract-structured", Model: "deepseek/deepseek-v4.1-flash", Prompt: "p", DryRun: true},
	} {
		req := calibrationRequest(c)
		if req.MaxTokens != 2048 || req.Reasoning != "low" {
			t.Errorf("%+v: max_tokens %d, reasoning %q", c, req.MaxTokens, req.Reasoning)
		}
		if req.DryRun != c.DryRun {
			t.Errorf("dry run lost")
		}
	}
}

func TestCalibrationResultsGoToLocalHistoryNotToOutcomeRecords(t *testing.T) {
	c, _, _ := newTestCalibrator(t, true)
	res := []calibrate.Result{
		{Sample: "b:one", UseCase: "audio-transcribe", Model: "google/gemini-3.8-flash", Correct: true},
		{Sample: "b:one", UseCase: "audio-transcribe", Model: "openai/whisper-large-v3", Correct: false},
		{Sample: "b:two", UseCase: "audio-transcribe", Model: "openai/whisper-large-v3", Error: "provider error"},
	}
	c.recordHistory(res)

	es, err := history.Read(c.d.HistoryFile())
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("%d history entries, want 2 (the failed call writes none): %+v", len(es), es)
	}
	got := map[string]string{}
	for _, e := range es {
		if e.Type != history.TypeOutcome || e.Route != history.RouteCalibrate {
			t.Errorf("entry %+v", e)
		}
		got[e.Model] = e.Outcome
	}
	if got["google/gemini-3.8-flash"] != history.Accepted || got["openai/whisper-large-v3"] != history.Declined {
		t.Errorf("outcomes %v", got)
	}

	l := history.NewLedger(es)
	if n := l.Stats("openai/whisper-large-v3", "audio-transcribe", "").UseCase.Declined; n != 1 {
		t.Errorf("routing history misses the declined result: %d", n)
	}
	if rows := l.UseCaseCounts(10); len(rows) != 2 {
		t.Errorf("history summary rows: %+v", rows)
	}
	// Not a habit of the user: no tendencies, no recent outcomes, no used models.
	if len(l.Tendencies()) != 0 || len(l.Recent(10)) != 0 || len(l.UsedModels()) != 0 {
		t.Error("calibration results leaked into the user's own patterns")
	}

	// Never sent as anonymous outcome records, now or a week on.
	for _, at := range []time.Time{time.Now(), time.Now().AddDate(0, 0, 8)} {
		if recs := outcomes.Build(es, at, time.Time{}); len(recs) != 0 {
			t.Errorf("outcome records built from calibration: %+v", recs)
		}
	}
}
