package calibrate

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"p0ssess/internal/pme/paths"
)

func TestGrade(t *testing.T) {
	num := Sample{Check: CheckNumber, Answer: "53.61", Tolerance: 0.005}
	for reply, want := range map[string]bool{
		"The total is shown.\nANSWER: $53.61": true,
		"ANSWER: 53.6":                        false,
		"**ANSWER:** 53.61":                   true,
		"ANSWER: 1,053.61":                    false,
		"no number here":                      false,
	} {
		if got := num.Grade(reply); got != want {
			t.Errorf("number %q: %v", reply, got)
		}
	}
	ex := Sample{Check: CheckExact, Answer: "XW-26276-K"}
	if !ex.Grade("ANSWER: \"xw-26276-k\".") || ex.Grade("ANSWER: XW-26276") {
		t.Error("exact")
	}
	ch := Sample{Check: CheckChoice, Answer: "yes", Choices: []string{"yes", "no"}}
	if !ch.Grade("ANSWER: Yes") || ch.Grade("ANSWER: no") {
		t.Error("choice")
	}
	co := Sample{Check: CheckContains, Answer: "red"}
	if !co.Grade("ANSWER: a red car") || co.Grade("ANSWER: a reddish car") {
		t.Error("contains")
	}
	w := Sample{Check: CheckWER, Answer: "the quick brown fox jumps", MaxWER: 0.25, Output: "transcript"}
	if !w.Grade("The quick brown fox jumped.") || w.Grade("a slow fox") {
		t.Error("wer")
	}
	if v := WER("a b c d", "a x c"); math.Abs(v-0.5) > 1e-9 {
		t.Errorf("WER = %v", v)
	}
	if !strings.HasSuffix((Sample{Prompt: "Count."}).PromptFor(), Instruction) {
		t.Error("prompt lacks the answer line")
	}
}

func TestParseManifest(t *testing.T) {
	good := fstest.MapFS{
		"s/answers.toml": {Data: []byte("[[sample]]\nuse_case = \"audio-events\"\nfile = \"a.mp3\"\nprompt = \"How many?\"\nanswer = \"3\"\ncheck = \"number\"\n")},
		"s/a.mp3":        {Data: []byte("x")},
	}
	ss, err := ParseManifest(good, "s", false)
	if err != nil || len(ss) != 1 || ss[0].ID != "a" || ss[0].Path != "s/a.mp3" {
		t.Fatalf("%+v %v", ss, err)
	}
	if _, err := ParseManifest(good, "s", true); err == nil {
		t.Error("built-in without source or license accepted")
	}
	for name, body := range map[string]string{
		"unknown key": "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\nanswer=\"a\"\nsecret=1\n",
		"escape":      "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nfile=\"../a.mp3\"\nprompt=\"p\"\nanswer=\"a\"\n",
		"no answer":   "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\n",
		"bad check":   "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\nanswer=\"a\"\ncheck=\"vibes\"\n",
		"bad number":  "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\nanswer=\"a\"\ncheck=\"number\"\n",
		"missing":     "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nfile=\"gone.mp3\"\nprompt=\"p\"\nanswer=\"a\"\n",
		"duplicate":   "[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\nanswer=\"a\"\n[[sample]]\nid=\"x\"\nuse_case=\"u\"\nprompt=\"p\"\nanswer=\"a\"\n",
	} {
		fsys := fstest.MapFS{"s/answers.toml": {Data: []byte(body)}, "a.mp3": {Data: []byte("x")}}
		if _, err := ParseManifest(fsys, "s", false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadUserAndMaterialize(t *testing.T) {
	dir := t.TempDir()
	if ss, err := LoadUser(dir); err != nil || ss != nil {
		t.Fatalf("empty folder: %v %v", ss, err)
	}
	os.WriteFile(filepath.Join(dir, "q.txt"), []byte("1 2 3"), 0o600)
	os.WriteFile(filepath.Join(dir, ManifestName), []byte("[[sample]]\nuse_case = \"math-reasoning\"\nfile = \"q.txt\"\nprompt = \"Sum?\"\nanswer = \"6\"\ncheck = \"number\"\n"), 0o600)
	ss, err := LoadUser(dir)
	if err != nil || len(ss) != 1 || ss[0].Path != filepath.Join(dir, "q.txt") || ss[0].Builtin {
		t.Fatalf("%+v %v", ss, err)
	}
	b, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := Materialize(b, out); err != nil {
		t.Fatal(err)
	}
	for _, s := range b {
		if s.Path == "" {
			continue
		}
		if !strings.HasPrefix(s.Path, out) {
			t.Errorf("%s not materialized: %s", s.ID, s.Path)
		}
		if _, err := os.Stat(s.Path); err != nil {
			t.Error(err)
		}
	}
}

type fakeRunner struct {
	mu      sync.Mutex
	price   float64
	replies map[string]string // model -> reply
	fail    map[string]bool
	calls   []Call
}

func (f *fakeRunner) Run(_ context.Context, c Call) (Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if c.DryRun {
		return Reply{EstimateUSD: f.price}, nil
	}
	if f.fail[c.Model] {
		return Reply{}, errors.New("provider error 502")
	}
	return Reply{Text: f.replies[c.Model], CostUSD: f.price, Elapsed: 2 * time.Second}, nil
}

func samples() []Sample {
	return []Sample{
		{ID: "a", UseCase: "audio-events", Prompt: "How many?", Answer: "6", Check: CheckNumber, Builtin: true},
		{ID: "b", UseCase: "audio-events", Prompt: "How many?", Answer: "4", Check: CheckNumber, Builtin: true},
		{ID: "m", UseCase: "math-reasoning", Prompt: "Sum?", Answer: "6", Check: CheckNumber},
	}
}

func router(_ context.Context, uc string) ([]string, error) {
	if uc == "math-reasoning" {
		return []string{"x/one", "x/two", "x/three", "x/four"}, nil
	}
	return []string{"x/one", "x/two"}, nil
}

func TestPlanAndExecute(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{price: 0.01, replies: map[string]string{"x/one": "ANSWER: 6", "x/two": "ANSWER: 4", "x/three": "ANSWER: 6"},
		fail: map[string]bool{"x/three": true}}
	p, err := NewPlan(ctx, samples(), router, f, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// 2 audio samples x 2 models + 1 math sample x PerKind models.
	if len(p.Items) != 7 || math.Abs(p.EstimateUSD-0.07) > 1e-9 || len(p.Kinds) != 2 {
		t.Fatalf("plan: %d items, $%v, kinds %v", len(p.Items), p.EstimateUSD, p.Kinds)
	}
	for _, c := range f.calls {
		if !c.DryRun {
			t.Fatal("planning sent a real call")
		}
	}
	res, spent, err := Execute(ctx, p, f, nil, 1, time.Now, nil)
	if err != nil || len(res) != 7 || math.Abs(spent-0.06) > 1e-9 {
		t.Fatalf("execute: %d results, $%v, %v", len(res), spent, err)
	}
	rs := &Results{}
	rs.Merge(res)
	sc := rs.Scores()
	want := map[[2]string][2]int{
		{"audio-events", "x/one"}: {2, 1}, {"audio-events", "x/two"}: {2, 1},
		{"math-reasoning", "x/one"}: {1, 1}, {"math-reasoning", "x/two"}: {1, 0},
	}
	if len(sc) != len(want) {
		t.Fatalf("scores %+v", sc)
	}
	for _, s := range sc {
		if w := want[[2]string{s.UseCase, s.Model}]; w != [2]int{s.N, s.Correct} {
			t.Errorf("%s %s: %d/%d, want %v", s.UseCase, s.Model, s.Correct, s.N, w)
		}
	}

	// A rerun plans only pairs without a graded result: the failed one.
	f.calls = nil
	p2, _ := NewPlan(ctx, samples(), router, f, nil, rs, true)
	if len(p2.Items) != 1 || p2.Items[0].Model != "x/three" {
		t.Fatalf("rerun plan: %+v", p2.Items)
	}
	// A new model entering a route is planned on its own.
	r2 := func(ctx context.Context, uc string) ([]string, error) {
		if uc == "audio-events" {
			return []string{"x/new", "x/one"}, nil
		}
		return router(ctx, uc)
	}
	p3, _ := NewPlan(ctx, samples(), r2, f, nil, rs, true)
	n := 0
	for _, it := range p3.Items {
		if it.Model == "x/new" {
			n++
		}
	}
	if n != 2 || len(p3.Items) != 3 {
		t.Errorf("new model plan: %+v", p3.Items)
	}
}

func TestExecuteCeiling(t *testing.T) {
	f := &fakeRunner{price: 0.01, replies: map[string]string{}}
	p, _ := NewPlan(context.Background(), samples(), router, f, nil, nil, false)
	res, spent, err := Execute(context.Background(), p, f, nil, 0.035, time.Now, nil)
	if !errors.Is(err, ErrCeiling) || len(res) != 3 || spent > 0.035 {
		t.Fatalf("ceiling: %d results, $%v, %v", len(res), spent, err)
	}
}

func TestMergeKeepsGraded(t *testing.T) {
	rs := &Results{}
	rs.Merge([]Result{{Sample: "b:a", Model: "x/one", Correct: true}})
	rs.Merge([]Result{{Sample: "b:a", Model: "x/one", Error: "timeout"}})
	if !rs.List[0].Correct || !rs.Has("b:a", "x/one") {
		t.Error("a failed rerun replaced a graded result")
	}
}

func TestParseStream(t *testing.T) {
	f, _ := os.Open("testdata/stream-alone.jsonl")
	defer f.Close()
	st := ParseStream(f)
	if st.Session == "" || st.Model != "claude-opus-5-5" || st.Turns != 9 || len(st.Readings) != 2 || len(st.Tokens) != 2 {
		t.Fatalf("%+v", st)
	}
	if Extract(st.Result) != "5" {
		t.Errorf("answer %q", Extract(st.Result))
	}
	usd, ok := priceTokens(st.Tokens)
	opus := (12000+40000)*4.0/1e6 + 6000*20.0/1e6 + 300000*0.20/1e6
	haiku := 2000*0.10/1e6 + 300*0.50/1e6
	if !ok || math.Abs(usd-(opus+haiku)) > 1e-9 {
		t.Errorf("priced $%v ok %v, want $%v", usd, ok, opus+haiku)
	}
	if _, ok := priceTokens(map[string]Tokens{"claude-mystery-9": {In: 1}}); ok {
		t.Error("unknown model priced")
	}
}

// TestRunBaselines drives the harness with recorded Claude Code output: no Claude runs.
func TestRunBaselines(t *testing.T) {
	alone, _ := os.ReadFile("testdata/stream-alone.jsonl")
	with, _ := os.ReadFile("testdata/stream-with.jsonl")
	dir := t.TempDir()
	file := filepath.Join(dir, "beeps.mp3")
	os.WriteFile(file, []byte("audio"), 0o600)
	s := Sample{ID: "beeps", UseCase: "audio-events", File: "beeps.mp3", Path: file, Prompt: "How many beeps?", Answer: "6", Check: CheckNumber, Builtin: true}

	var got [][]string
	clock := time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(30 * time.Second); return clock }
	exec := func(_ context.Context, wd string, args, env []string) ([]byte, error) {
		got = append(got, args)
		if _, err := os.Stat(filepath.Join(wd, "beeps.mp3")); err != nil {
			t.Errorf("sample file not copied: %v", err)
		}
		if strings.Contains(strings.Join(args, " "), "--disallowedTools mcp__plugin_p0ssess_p0ssess") {
			if env[0] != BaselineEnv+"=alone" {
				t.Errorf("env %v", env)
			}
			return alone, nil
		}
		return with, nil
	}
	o := BaselineOptions{Modes: []string{ModeAlone, ModeWith}, Model: "opus", MaxUSD: 2, Timeout: time.Minute, Work: dir, Now: now,
		Spent: func(session string, _, _ time.Time) float64 {
			if session == "5b0c0b8e-0000-4000-8000-000000000002" {
				return 0.002
			}
			return 0
		}}
	runs := RunBaselines(context.Background(), []Sample{s}, o, exec, nil)
	if len(runs) != 2 || len(got) != 2 {
		t.Fatalf("%d runs", len(runs))
	}
	a, w := runs[0], runs[1]
	if a.Correct || !w.Correct {
		t.Errorf("grading: alone %v with %v", a.Correct, w.Correct)
	}
	if a.WeeklyShare == nil || !a.ShareLowerBound || math.Abs(*a.WeeklyShare-0.0035) > 1e-9 {
		t.Errorf("alone share %v lower %v", a.WeeklyShare, a.ShareLowerBound)
	}
	// The with run is measured from the alone run's last reading.
	if w.WeeklyShare == nil || w.ShareLowerBound || math.Abs(*w.WeeklyShare-(0.4170-0.4155)) > 1e-9 {
		t.Errorf("with share %v lower %v", w.WeeklyShare, w.ShareLowerBound)
	}
	if w.OpenRouterUSD != 0.002 || a.OpenRouterUSD != 0 {
		t.Error("ledger spend")
	}
	if a.ClaudeUSD == nil || a.Seconds != 30 {
		t.Errorf("alone %+v", a)
	}
	for _, args := range got {
		j := strings.Join(args, " ")
		for _, want := range []string{"-p", "--output-format stream-json", "--no-session-persistence", "--max-budget-usd 2.00", "ANSWER:", "./beeps.mp3"} {
			if !strings.Contains(j, want) {
				t.Errorf("args lack %q: %s", want, j)
			}
		}
	}
	if !strings.Contains(strings.Join(got[1], " "), "mcp__plugin_p0ssess_p0ssess") || strings.Contains(strings.Join(got[1], " "), "disallowed") {
		t.Error("with mode must allow p0ssess")
	}
}

func TestBuildReport(t *testing.T) {
	empty := BuildReport(ReportInput{Pro: true, Now: time.Now()})
	if len(empty.Kinds) != 0 || len(empty.Models) != 0 || empty.Totals != nil {
		t.Fatalf("empty report has numbers: %+v", empty)
	}
	opus, with := 1.0, 0.05
	share, wshare := 0.01, 0.001
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	rs := &Results{List: []Result{
		{Sample: "b:a", UseCase: "audio-events", Model: "x/one", Correct: true, CostUSD: 0.002, Seconds: 4, At: at},
		{Sample: "b:b", UseCase: "audio-events", Model: "x/one", Correct: false, CostUSD: 0.002, Seconds: 2, At: at},
		{Sample: "b:m", UseCase: "math-reasoning", Model: "x/two", Correct: true, CostUSD: 0.001, Seconds: 1, At: at},
		{Sample: "b:m", UseCase: "math-reasoning", Model: "x/bad", Error: "502", At: at},
	}}
	bs := &Baselines{Runs: []BaselineRun{
		{Sample: "b:a", UseCase: "audio-events", Mode: ModeAlone, Correct: true, Seconds: 400, ClaudeUSD: &opus, WeeklyShare: &share, At: at},
		{Sample: "b:b", UseCase: "audio-events", Mode: ModeAlone, Correct: false, Seconds: 200, ClaudeUSD: &opus, WeeklyShare: &share, At: at},
		{Sample: "b:m", UseCase: "math-reasoning", Mode: ModeAlone, Correct: true, Seconds: 10, ClaudeUSD: &with, WeeklyShare: &wshare, At: at},
		{Sample: "b:m", UseCase: "math-reasoning", Mode: ModeWith, Correct: true, Seconds: 12, ClaudeUSD: &with, OpenRouterUSD: 0.001, WeeklyShare: &wshare, At: at},
	}}
	r := BuildReport(ReportInput{Pro: true, Now: at, Results: rs, Baselines: bs,
		Picked: map[string]string{"audio-events": "x/one", "math-reasoning": "x/two"}})
	if len(r.Kinds) != 2 || len(r.Models) != 2 {
		t.Fatalf("%+v", r)
	}
	ae := r.Kinds[0]
	if ae.Alone.Correct != 1 || *ae.Alone.CostPerCorrectUSD != 2 || ae.Alone.MeanSeconds != 300 || *ae.Alone.WeeklySharePerCorrect != 0.02 {
		t.Errorf("alone %+v", ae.Alone)
	}
	if ae.With.Source != "calibration" || math.Abs(*ae.With.CostPerCorrectUSD-0.004) > 1e-12 || ae.With.WeeklySharePerCorrect != nil {
		t.Errorf("with %+v", ae.With)
	}
	mr := r.Kinds[1]
	if mr.With.Source != "baseline" || math.Abs(*mr.With.CostPerCorrectUSD-0.051) > 1e-12 {
		t.Errorf("math with %+v", mr.With)
	}
	if r.Totals == nil || r.Totals.Kinds != 2 || r.Totals.WeeklyShareSaved == nil || math.Abs(*r.Totals.WeeklyShareSaved-0.02) > 1e-12 {
		t.Errorf("totals %+v", r.Totals)
	}
	if !r.Models[0].Picked {
		t.Error("picked model not marked")
	}
	none := &Baselines{Runs: []BaselineRun{{Sample: "b:a", UseCase: "audio-events", Mode: ModeAlone, Correct: false, Seconds: 9, ClaudeUSD: &opus, At: at}}}
	r = BuildReport(ReportInput{Pro: true, Now: at, Baselines: none})
	if r.Kinds[0].Alone.CostPerCorrectUSD != nil || r.Totals != nil {
		t.Error("a side with no correct answers got a cost")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	d := paths.Dirs{State: t.TempDir(), Config: t.TempDir()}
	rs, err := LoadResults(d)
	if err != nil || len(rs.List) != 0 {
		t.Fatal(err)
	}
	rs.Merge([]Result{{Sample: "b:a", Model: "x/one", Correct: true}})
	if err := SaveResults(d, rs); err != nil {
		t.Fatal(err)
	}
	SaveMeta(d, Meta{Pending: true})
	again, _ := LoadResults(d)
	if !again.Has("b:a", "x/one") || !LoadMeta(d).Pending {
		t.Error("round trip")
	}
	fi, _ := os.Stat(filepath.Join(Dir(d), "results.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	Forget(d)
	if again, _ := LoadResults(d); len(again.List) != 0 {
		t.Error("forget")
	}
}

func TestUploadSendsOnlyCounts(t *testing.T) {
	rs := &Results{List: []Result{{Sample: "u:secret-meeting", UseCase: "audio-transcribe", Model: "x/one", Correct: true, CostUSD: 0.123, Seconds: 9}}}
	var body []byte
	called := false
	put := func(_ context.Context, b []byte) error { body, called = b, true; return nil }
	if err := Upload(context.Background(), put, rs); err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"scores":[{"use_case":"audio-transcribe","model":"x/one","n":1,"correct":1}]}`
	if string(body) != want {
		t.Errorf("body %s", body)
	}
	body, called = []byte("x"), false
	Upload(context.Background(), put, &Results{})
	if !called || body != nil {
		t.Error("an empty set should delete")
	}
}
