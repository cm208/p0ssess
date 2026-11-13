package local

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

// load opens an engine on paths.Default() with the test route table.
func load(t testing.TB) *Engine {
	t.Helper()
	e, err := Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	e.Table = tabletest.Store(e.Dirs.Cache)
	// A background table fetch would outlive the test's directories.
	e.Table.Checked(time.Now())
	return e
}

// setup makes a scratch home with a project, the test route table and an engine service that is down.
func setup(t *testing.T, budgetTOML string) (*Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	tabletest.Down(t)
	if budgetTOML != "" {
		os.MkdirAll(home, 0o700)
		os.WriteFile(filepath.Join(home, "budget.toml"), []byte(budgetTOML), 0o600)
	}
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	return load(t), proj
}

func offline(e *Engine) bool {
	_, err := os.Stat(e.Dirs.OfflineMarker())
	return err == nil
}

func TestLeadID(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":            "anthropic/claude-opus-5.5",
		"claude-sonnet-5-5[1m]":      "anthropic/claude-sonnet-5.5",
		"anthropic/claude-haiku-5.5": "anthropic/claude-haiku-5.5",
		"gpt-6":                      "",
	} {
		if got := LeadID(in); got != want {
			t.Errorf("LeadID(%q) = %q", in, got)
		}
	}
}

// TestTableAnswersWhenTheEngineIsDown: the route table answers in the shape a live answer has, without
// a word about it, and the status line marker is set.
func TestTableAnswersWhenTheEngineIsDown(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	r, err := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	if r.ClaudeFeasible || r.Pick == nil || r.Pick.Route != RouteDirect || r.Pick.Model != "qwen/qwen3.8-omni-flash" {
		t.Fatalf("video should be delegated: %+v", r.Pick)
	}
	if r.Pick.NeedsApproval != (r.Pick.EstimateUSD > 0.02) {
		t.Errorf("asks exactly when the estimate is above the $0.02 per-task limit: %+v", r.Pick)
	}
	if r.Announce != "handing this to qwen3.8-omni-flash: summarize a talk, meeting or lecture video" || r.Stakes != 1 || r.Lambda.Value != 1 {
		t.Errorf("announce %q, stakes %d, λ %v", r.Announce, r.Stakes, r.Lambda.Value)
	}
	for _, n := range r.Notes {
		if strings.Contains(n, "offline") || strings.Contains(n, "table") {
			t.Errorf("the fallback must be silent: %q", n)
		}
	}
	if !offline(e) {
		t.Error("the offline marker should be set")
	}
	r, _ = e.Recommend(Request{UseCase: "quick-code-question", Scope: Scope{Cwd: proj}})
	if r.Pick.Route != RouteClaude || r.Announce != "keeping this on Claude" {
		t.Errorf("a one-turn question stays on Claude: %+v", r.Pick)
	}
	r, _ = e.Recommend(Request{UseCase: "security-critical-code", Scope: Scope{Cwd: proj}})
	for _, o := range r.Options {
		if o.Route != RouteClaude {
			t.Errorf("stakes 4 offered %s", o.Model)
		}
	}
	if _, err := e.Recommend(Request{Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("empty request should fail")
	}
	if _, err := e.Recommend(Request{UseCase: "nope", Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("unknown use case should fail")
	}
	if _, err := e.Recommend(Request{UseCase: "log-digest", Stakes: 5, Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("stakes 5 should fail")
	}
}

func TestProfileOffline(t *testing.T) {
	e, proj := setup(t, "")
	r, err := e.Recommend(Request{Profile: &Profile{Text: "transcribe this podcast episode", Inputs: []string{"mp3"}, AudioSec: 3600}, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	if r.UseCase.ID != "audio-transcribe" || r.Work.AudioSec != 3600 || r.Pick == nil || r.Pick.Route != RouteDirect {
		t.Fatalf("classified as %s, work %+v, pick %+v", r.UseCase.ID, r.Work, r.Pick)
	}
	r, err = e.Recommend(Request{Profile: &Profile{Text: "explain the parser"}, Scope: Scope{Cwd: proj}})
	if err != nil || r.Pick == nil || r.Pick.Route != RouteClaude || !strings.Contains(strings.Join(r.Notes, " "), "pass use_case") {
		t.Fatalf("a text-only profile offline stays on Claude with a note: %+v %v %v", r.Pick, r.Notes, err)
	}
}

func okAnswer(req remote.Request) remote.Response {
	return remote.Response{V: 1, UseCase: remote.UseCase{ID: "video-understand", N: 75, Task: "Summarize a talk, meeting or lecture video", Kind: "media",
		Family: "video_understanding", Path: "direct", Verify: "spot", StakesRange: []int{1, 2}}, Stakes: 1, LeadModel: "anthropic/claude-sonnet-5.5",
		Options: []remote.Option{
			{Rank: 1, Route: "direct", Model: "google/gemini-3.8-flash", Total: 0.05, Breakdown: remote.Breakdown{Cash: 0.03}, CashMax: 0.09, EstimateUSD: 0.03},
			{Rank: 2, Route: "direct", Model: "qwen/qwen3.8-omni-flash", Total: 0.06, Breakdown: remote.Breakdown{Cash: 0.01}, CashMax: 0.015, EstimateUSD: 0.01},
		},
		Excluded: []remote.Exclusion{{Model: "z-ai/glm-5.3-flash", Why: "rule", RuleID: "r-0000aaaa"}, {Model: "x/y", Why: "models_deny: x"}},
		Notes:    []string{"from the engine"}}
}

// TestEngineAnswer covers the request the client sends and what it does with the answer: rules are
// worded locally, the monthly cap drops what would pass it, the pick moves, approval is filled in.
func TestEngineAnswer(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 0.05\nper_task_usd = 0.005\ndaily_usd = -1\n[models]\ndeny = [\"x\"]\n")
	srv := tabletest.Serve(t, func(req remote.Request) (int, any) { return 200, okAnswer(req) })
	e.SetOffline(true)
	files := e.RuleFiles(Scope{Cwd: proj})
	now := time.Now()
	add := func(r rules.Rule) {
		if _, err := rules.Add(files, r, now); err != nil {
			t.Fatal(err)
		}
	}
	add(rules.Rule{ID: "r-0000aaaa", Kind: rules.KindForbid, Target: rules.TargetModel, Models: []string{"z-ai/glm-5.3-flash"}, Scope: rules.ScopeGlobal, Note: "private words"})
	add(rules.Rule{Kind: rules.KindApprove, Kinds: []string{"media"}, MaxPerRunUSD: 0.02, Scope: rules.ScopeGlobal})
	add(rules.Rule{Kind: rules.KindAsk, Models: []string{"x-ai/grok-4.20"}, Scope: rules.ScopeGlobal})
	past := now.Add(-time.Hour)
	add(rules.Rule{Kind: rules.KindForbid, Target: rules.TargetModel, Models: []string{"openai/gpt-6.1-sol"}, Scope: rules.ScopeGlobal, Expires: &past})
	e.RecordRun(Run{ID: "d-1", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.001, Status: "done", UseCase: "video-understand", Cwd: proj})

	r, err := e.Recommend(Request{UseCase: "video-understand", Record: true, Scope: Scope{Cwd: proj, Session: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	req := srv.Last()
	if len(req.Rules) != 1 || req.Rules[0].ID != "r-0000aaaa" || req.Rules[0].Target != "model" || req.Lambda != 1 || req.Tuning != nil {
		t.Errorf("request rules %+v, λ %v, tuning %v", req.Rules, req.Lambda, req.Tuning)
	}
	if len(req.Lists.ModelsDeny) != 1 || req.Lists.ModelsAllow == nil || req.Privacy.Mode != "no-training" {
		t.Errorf("lists %+v privacy %+v", req.Lists, req.Privacy)
	}
	if len(req.History) != 1 || req.History[0].Model != "google/gemini-3.8-flash" || req.History[0].Accepted != 1 {
		t.Errorf("history %+v", req.History)
	}
	b, _ := json.Marshal(req)
	for _, leak := range []string{"private words", proj, "s1", "expires", "created"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("the request carries %q: %s", leak, b)
		}
	}
	if h := srv.Headers[0]; !strings.HasPrefix(h.Get("P0ssess-Install"), "p0i_") || h.Get("Authorization") != "" {
		t.Errorf("headers %v", h)
	}

	if len(r.Options) != 1 || r.Pick == nil || r.Pick.Model != "qwen/qwen3.8-omni-flash" || r.Pick.Rank != 1 {
		t.Fatalf("the $0.09 ceiling passes the $0.05 cap and must go; options %+v", r.Options)
	}
	if r.Pick.ApprovalID == "" {
		t.Errorf("the standing approval up to $0.02 covers a $0.01 estimate: %+v", r.Pick)
	}
	if r.Pick.NeedsApproval {
		t.Errorf("covered, so no ask: %+v", r.Pick)
	}
	var words []string
	for _, x := range r.Excluded {
		words = append(words, x.Why)
	}
	all := strings.Join(words, "|")
	if !strings.Contains(all, "rule: never use glm-5.3-flash") || !strings.Contains(all, "models_deny: x") || !strings.Contains(all, "monthly cap") {
		t.Errorf("exclusions %v", words)
	}
	if r.Announce != "handing this to qwen3.8-omni-flash: summarize a talk, meeting or lecture video" || r.OfferID == "" || len(r.Rules) == 0 {
		t.Errorf("announce %q offer %q rules %v", r.Announce, r.OfferID, r.Rules)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "from the engine") || offline(e) {
		t.Errorf("notes %v, offline %v", r.Notes, offline(e))
	}

	os.WriteFile(e.Dirs.EngineFile(), []byte("[harm]\ns3 = 2.0\n[lambda]\npreset = \"eager\"\n"), 0o600)
	e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if tu := srv.Last().Tuning; tu["harm"] == nil || tu["lambda"] != nil {
		t.Errorf("tuning %v", tu)
	}
}

// TestAnyFailureFallsBack: every kind of failure gets the table's answer.
func TestAnyFailureFallsBack(t *testing.T) {
	for name, answer := range map[string]func(remote.Request) (int, any){
		"429": func(remote.Request) (int, any) {
			return http.StatusTooManyRequests, map[string]string{"error": "rate_limited"}
		},
		"400": func(remote.Request) (int, any) {
			return http.StatusBadRequest, map[string]string{"error": "bad_request"}
		},
		"bad json":  func(remote.Request) (int, any) { return 200, "not an answer" },
		"no stakes": func(r remote.Request) (int, any) { a := okAnswer(r); a.Stakes = 0; return 200, a },
		"slow": func(r remote.Request) (int, any) {
			time.Sleep(300 * time.Millisecond)
			return 200, okAnswer(r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, proj := setup(t, "plan = \"api\"\n")
			old := RemoteTimeout
			RemoteTimeout = 100 * time.Millisecond
			defer func() { RemoteTimeout = old }()
			tabletest.Serve(t, answer)
			r, err := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
			if err != nil || r.Pick == nil || r.Pick.Model != "qwen/qwen3.8-omni-flash" || len(r.Options) != 2 {
				t.Fatalf("%+v %v", r, err)
			}
			if !offline(e) {
				t.Error("offline marker")
			}
		})
	}
}

func TestPrivateRepoNeverCallsTheEngine(t *testing.T) {
	e, proj := setup(t, "")
	srv := tabletest.Serve(t, func(r remote.Request) (int, any) { return 200, okAnswer(r) })
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	for _, uc := range []string{"log-digest", "pdf-table-extract", "video-understand", "code-implement-feature"} {
		r, err := e.Recommend(Request{UseCase: uc, Stakes: 3, Scope: Scope{Cwd: proj}})
		if uc == "log-digest" || uc == "video-understand" {
			r, err = e.Recommend(Request{UseCase: uc, Scope: Scope{Cwd: proj}})
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range r.Options {
			if o.Route != RouteClaude && o.Route != RouteLocal {
				t.Errorf("%s: private repo offered %s", uc, o.Model)
			}
		}
		if !strings.Contains(strings.Join(r.Notes, " "), ".p0ssess-private") || r.SecondOpinion != nil {
			t.Errorf("%s: notes %v", uc, r.Notes)
		}
		if uc == "video-understand" && (r.Pick != nil || r.Announce != "no route fits this task") {
			t.Errorf("Claude cannot watch video, so nothing fits: %+v", r.Pick)
		}
		if uc == "log-digest" && (r.Pick == nil || r.Pick.Route != RouteClaude) {
			t.Errorf("log-digest stays on Claude: %+v", r.Pick)
		}
	}
	if srv.Count() != 0 {
		t.Errorf("the engine was called %d times from a private repository", srv.Count())
	}
}

func TestRulesAndRecord(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	files := e.RuleFiles(Scope{Cwd: proj})
	if files.Project != filepath.Join(proj, ".p0ssess", "rules.toml") {
		t.Fatalf("project rules path = %q", files.Project)
	}
	_, err := rules.Add(files, rules.Rule{Kind: rules.KindForbid, Target: rules.TargetModel,
		Models: []string{"qwen/qwen3.8-omni-flash"}, Kinds: []string{"media"}, Scope: rules.ScopeProject}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Recommend(Request{UseCase: "video-understand", Record: true, Profile: &Profile{Text: "transcribe the board meeting"}, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range r.Options {
		if o.Model == "qwen/qwen3.8-omni-flash" {
			t.Error("forbidden model offered")
		}
	}
	if r.Pick == nil || r.Pick.Model != "google/gemini-3.8-flash" || r.Pick.Rank != 1 {
		t.Errorf("pick after the drop: %+v", r.Pick)
	}
	if len(r.Rules) == 0 || r.OfferID == "" {
		t.Errorf("rules applied %v, offer %q", r.Rules, r.OfferID)
	}
	es, _ := history.Read(e.Dirs.HistoryFile())
	if len(es) != 1 || es[0].Type != history.TypeOffer || es[0].UseCase != "video-understand" || es[0].Kind != "media" {
		t.Fatalf("offer not recorded: %+v", es)
	}
	raw, _ := os.ReadFile(e.Dirs.HistoryFile())
	if strings.Contains(string(raw), "board meeting") {
		t.Error("history must not hold task text")
	}
}

func TestMixRuleFromTable(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	if _, err := e.RuleAdd(Scope{Cwd: proj}, rules.Rule{Kind: rules.KindMix, Models: []string{"qwen/qwen3.8-omni-flash", "google/gemini-3.8-flash"}, Scope: rules.ScopeGlobal}, false, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		e.RecordRun(Run{ID: "d-" + string(rune('a'+i)), Route: "direct", Model: "qwen/qwen3.8-omni-flash", Status: "done", UseCase: "video-understand", Cwd: proj})
	}
	r, _ := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if r.Pick == nil || r.Pick.Model != "google/gemini-3.8-flash" || !r.Pick.MixNext {
		t.Errorf("the mix's next member within 1.5x the cheapest is picked: %+v", r.Pick)
	}
}

func TestPressureShiftsRouting(t *testing.T) {
	e, proj := setup(t, "plan = \"subscription\"\n")
	pick := func(used float64) Option {
		t.Helper()
		reset := time.Now().Add(150 * time.Minute).Unix()
		st := usage.State{ReadingAt: time.Now(), Subscriber: true,
			RateLimits: &usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &used, ResetsAt: &reset}}}
		if err := usage.Save(e.Dirs.UsageFile(), st); err != nil {
			t.Fatal(err)
		}
		r, err := e.Recommend(Request{UseCase: "code-implement-feature", Scope: Scope{Cwd: proj}})
		if err != nil {
			t.Fatal(err)
		}
		return *r.Pick
	}
	if p := pick(5); p.Route != RouteClaude {
		t.Errorf("low pressure: Claude should keep a feature, got %s", p.Model)
	}
	if p := pick(55); p.Route == RouteClaude {
		t.Errorf("projected exhaustion: feature work should move off Claude")
	}
}

func TestSecondOpinion(t *testing.T) {
	answer := func(req remote.Request) (int, any) {
		a := okAnswer(req)
		a.UseCase = remote.UseCase{ID: "code-implement-feature", Kind: "agent", Task: "Implement a feature", EditsCode: true}
		a.Stakes, a.Options = 3, []remote.Option{{Route: "claude", Model: "anthropic/claude-sonnet-5.5", Total: 0.5}}
		a.SecondOpinion = &remote.SecondOpinion{UseCase: SecondOpinionUseCase, Models: []string{"openai/gpt-6.1-sol", "google/gemini-3.8-flash"}, CashUSD: 0.06, CashMaxUSD: 0.09}
		return 200, a
	}
	e, proj := setup(t, "plan = \"api\"\n")
	tabletest.Serve(t, answer)
	r, err := e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 3, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	if so := r.SecondOpinion; so == nil || len(so.Models) != 2 || !so.NeedsApproval || so.How == "" {
		t.Fatalf("a $0.03 review per model is above the $0.02 limit and asks: %+v", so)
	}
	e2, proj2 := setup(t, "plan = \"api\"\nmonthly_usd = 0.05\n")
	tabletest.Serve(t, answer)
	r, _ = e2.Recommend(Request{UseCase: "code-implement-feature", Stakes: 3, Scope: Scope{Cwd: proj2}})
	if r.SecondOpinion != nil || !strings.Contains(strings.Join(r.Notes, " "), "monthly cap") {
		t.Errorf("over budget: %+v %v", r.SecondOpinion, r.Notes)
	}
	e3, proj3 := setup(t, "plan = \"api\"\n[models]\ndeny = [\"openai\"]\n")
	tabletest.Serve(t, answer)
	if r, _ = e3.Recommend(Request{UseCase: "code-implement-feature", Stakes: 3, Scope: Scope{Cwd: proj3}}); r.SecondOpinion != nil {
		t.Error("a review by a denied model was offered")
	}
}

func TestRunsAreSpend(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	if err := e.RecordRun(Run{ID: "d-1", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03, Cwd: proj}); err != nil {
		t.Fatal(err)
	}
	en, err := e.RecordOutcome(Outcome{Model: "gemini-3.8-flash", Outcome: "accepted", CashUSD: 0.5, UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if err != nil || en.RunID != "d-1" || en.CashUSD != 0.03 || en.Kind != "media" {
		t.Fatalf("outcome should take the measured run: %+v %v", en, err)
	}
	if _, err := e.RecordOutcome(Outcome{Model: "x/y", Outcome: "accepted", CashUSD: 0.01, Scope: Scope{Cwd: proj}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RecordOutcome(Outcome{Model: "x/y", Outcome: "redo", UseCase: "nope", Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("an unknown use case was recorded")
	}
	st, _ := e.Status(Scope{Cwd: proj})
	if st.Spend.Runs != 2 || st.Spend.Month < 0.0399 || st.Spend.Month > 0.0401 || !strings.Contains(st.RouteTable, "13 use cases") {
		t.Errorf("spend = %+v, table %q", st.Spend, st.RouteTable)
	}
}

func TestAsksFromTheEstimate(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 0.0001\n")
	p := &Profile{VideoSec: 10}
	r, _ := e.Recommend(Request{UseCase: "video-understand", Stakes: 1, Profile: p, Scope: Scope{Cwd: proj}})
	if !r.Pick.NeedsApproval {
		t.Fatalf("above the per-task limit asks: %+v", r.Pick)
	}
	if _, err := e.RuleAdd(Scope{Cwd: proj}, rules.Rule{Kind: rules.KindApprove, Kinds: []string{"media"}, MaxPerRunUSD: 0.05, Scope: "global"}, false, 0); err != nil {
		t.Fatal(err)
	}
	r, _ = e.Recommend(Request{UseCase: "video-understand", Stakes: 1, Profile: p, Scope: Scope{Cwd: proj}})
	if r.Pick.NeedsApproval || r.Pick.ApprovalID == "" {
		t.Errorf("a standing approval should cover it: %+v", r.Pick)
	}
	e2, proj2 := setup(t, "plan = \"api\"\nper_task_usd = 0.10\n")
	r, _ = e2.Recommend(Request{UseCase: "video-understand", Stakes: 1, Profile: p, Scope: Scope{Cwd: proj2}})
	if r.Pick.NeedsApproval {
		t.Errorf("a first run under the limit runs without asking: %+v", r.Pick.ApprovalReasons)
	}
}

func TestNoTableYet(t *testing.T) {
	e, proj := setup(t, "")
	r, err := (&Engine{Dirs: e.Dirs, Now: time.Now}).Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if err != nil || r.Pick == nil || r.Pick.Route != RouteClaude {
		t.Fatalf("with no table and no engine, keep on Claude: %+v %v", r, err)
	}
}

func TestRefreshTable(t *testing.T) {
	e, _ := setup(t, "")
	tabletest.Serve(t, func(r remote.Request) (int, any) { return 503, nil })
	e.SetOffline(true)
	line, err := e.RefreshTable(t.Context())
	if err != nil || !strings.HasPrefix(line, "fetched route table") || offline(e) {
		t.Fatalf("%q %v", line, err)
	}
	if e.Table.Due(time.Now()) {
		t.Error("a check was just made")
	}
	if line, err = e.RefreshTable(t.Context()); err != nil || !strings.HasSuffix(line, "is current") {
		t.Errorf("304: %q %v", line, err)
	}
	tabletest.Down(t)
	if _, err := e.RefreshTable(t.Context()); err == nil || !offline(e) || e.Table.Table().Empty() {
		t.Errorf("a failed fetch keeps the table and marks offline: %v", err)
	}
}
