package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/score"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

func setup(t *testing.T, budgetTOML string) (*Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	if budgetTOML != "" {
		os.MkdirAll(home, 0o700)
		os.WriteFile(filepath.Join(home, "budget.toml"), []byte(budgetTOML), 0o600)
	}
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	e, err := Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	return e, proj
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

func TestRecommendCapabilityAndKeep(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	r, err := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	if r.ClaudeFeasible || r.Pick == nil || r.Pick.Route != score.RouteDirect {
		t.Fatalf("video should be delegated: %+v", r.Pick)
	}
	if r.Pick.NeedsApproval != (r.Pick.EstimateUSD > 0.02) {
		t.Errorf("asks exactly when the estimate is above the $0.02 per-task limit: %+v", r.Pick)
	}
	for _, o := range r.Options {
		if m := e.Reg.Get(o.Model); m != nil && !m.HasInput("audio") {
			t.Errorf("video-understand needs the soundtrack; offered %s, which takes no audio", o.Model)
		}
	}
	if !strings.HasPrefix(r.Announce, "handing this to ") {
		t.Errorf("announce = %q", r.Announce)
	}
	r, _ = e.Recommend(Request{UseCase: "quick-code-question", Scope: Scope{Cwd: proj}})
	if r.Pick.Route != score.RouteClaude || r.Announce != "keeping this on Claude" {
		t.Errorf("a one-turn question stays on Claude: %+v", r.Pick)
	}
	r, _ = e.Recommend(Request{UseCase: "security-critical-code", Scope: Scope{Cwd: proj}})
	for _, o := range r.Options {
		if o.Route != score.RouteClaude {
			t.Errorf("stakes 4 offered %s", o.Model)
		}
	}
}

func TestRecommendProfile(t *testing.T) {
	e, proj := setup(t, "")
	r, err := e.Recommend(Request{Profile: &catalog.Profile{Text: "transcribe this podcast episode", Inputs: []string{"mp3"}, AudioSec: 3600}, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	if r.UseCase.ID != "audio-transcribe" || r.Work.AudioSec != 3600 || len(r.Classification) == 0 {
		t.Fatalf("classified as %s, work %+v", r.UseCase.ID, r.Work)
	}
	if _, err := e.Recommend(Request{Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("empty request should fail")
	}
	if _, err := e.Recommend(Request{UseCase: "nope", Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("unknown use case should fail")
	}
}

func TestRulesAndRecord(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	files := e.RuleFiles(Scope{Cwd: proj})
	if files.Project != filepath.Join(proj, ".p0ssess", "rules.toml") {
		t.Fatalf("project rules path = %q", files.Project)
	}
	_, err := rules.Add(files, rules.Rule{Kind: rules.KindForbid, Target: rules.TargetModel,
		Models: []string{"google/gemini-3.8-flash"}, Kinds: []string{"media"}, Scope: rules.ScopeProject}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Recommend(Request{UseCase: "video-understand", Record: true, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range r.Options {
		if o.Model == "google/gemini-3.8-flash" {
			t.Error("forbidden model offered")
		}
	}
	if len(r.Rules) == 0 || r.OfferID == "" {
		t.Errorf("rules applied %v, offer %q", r.Rules, r.OfferID)
	}
	es, _ := history.Read(e.Dirs.HistoryFile())
	if len(es) != 1 || es[0].Type != history.TypeOffer || es[0].UseCase != "video-understand" {
		t.Fatalf("offer not recorded: %+v", es)
	}
	raw, _ := os.ReadFile(e.Dirs.HistoryFile())
	if strings.Contains(string(raw), "transcribe") {
		t.Error("history must not hold task text")
	}
}

func TestPressureShiftsRouting(t *testing.T) {
	e, proj := setup(t, "plan = \"subscription\"\n")
	pick := func(used float64) score.Option {
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
	if p := pick(5); p.Route != score.RouteClaude {
		t.Errorf("low pressure: Claude should keep a feature, got %s", p.Model)
	}
	if p := pick(55); p.Route == score.RouteClaude {
		t.Errorf("projected exhaustion: feature work should move off Claude")
	}
}

func TestPrivateRepo(t *testing.T) {
	e, proj := setup(t, "")
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	r, _ := e.Recommend(Request{UseCase: "log-digest", Scope: Scope{Cwd: proj}})
	for _, o := range r.Options {
		if o.Route != score.RouteClaude && o.Route != score.RouteLocal {
			t.Errorf("private repo offered %s", o.Model)
		}
	}
}

// TestSessionCalibration checks the agent-session estimate against the
// session-cost medians for 10-49-turn Claude Code sessions in
// docs/RESEARCH.md (SC): the default 20-turn shape must land within 2.5x.
func TestSessionCalibration(t *testing.T) {
	e, _ := setup(t, "")
	cfg := score.Default()
	medians := map[string]float64{
		"anthropic/claude-sonnet-5.5":       0.534,
		"anthropic/claude-opus-5.5":         1.331,
		"openai/gpt-6.1-sol":                0.426,
		"deepseek/deepseek-v4.1-flash-1017": 0.027,
		"z-ai/glm-5.3-flash":                0.038,
		"anthropic/claude-haiku-5.5":        0.043,
	}
	for id, med := range medians {
		m := e.Reg.Get(id)
		tp, ok := m.Prices(false, false)
		if !ok {
			t.Fatalf("%s has no price", id)
		}
		s := cfg.Agent
		got := 20 * (float64(s.ContextTokens)*tp.CacheRead + float64(s.NewInTokens)*tp.In + float64(s.OutTokens)*tp.Out) / 1e6
		if got < med/2.5 || got > med*2.5 {
			t.Errorf("%s: estimate $%.3f vs median $%.3f", id, got, med)
		}
	}
}

func TestSecondOpinion(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	r, err := e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 3, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	so := r.SecondOpinion
	if so == nil || len(so.Models) != 2 || !so.NeedsApproval {
		t.Fatalf("stakes 3 code work should offer a second opinion that asks first: %+v", so)
	}
	a, b := e.Reg.Get(so.Models[0]), e.Reg.Get(so.Models[1])
	if a.Author == b.Author || a.IsClaude() || b.IsClaude() {
		t.Errorf("second opinion must be two non-Claude families: %v", so.Models)
	}
	if r, _ := e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 2, Scope: Scope{Cwd: proj}}); r.SecondOpinion != nil {
		t.Error("no second opinion below stakes 3")
	}
	if r, _ := e.Recommend(Request{UseCase: "video-understand", Stakes: 3, Scope: Scope{Cwd: proj}}); r.SecondOpinion != nil {
		t.Error("no second opinion for work that edits no code")
	}
	e2, proj2 := setup(t, "plan = \"api\"\nmonthly_usd = 0.05\n")
	r, _ = e2.Recommend(Request{UseCase: "code-implement-feature", Stakes: 4, Scope: Scope{Cwd: proj2}})
	if r.SecondOpinion != nil || !strings.Contains(strings.Join(r.Notes, " "), "monthly cap") {
		t.Errorf("over budget: %+v %v", r.SecondOpinion, r.Notes)
	}
}

func TestRunsAreSpend(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	if err := e.RecordRun(Run{ID: "d-1", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03, Cwd: proj}); err != nil {
		t.Fatal(err)
	}
	en, err := e.RecordOutcome(Outcome{Model: "gemini-3.8-flash", Outcome: "accepted", CashUSD: 0.5, UseCase: "video-understand", Scope: Scope{Cwd: proj}})
	if err != nil || en.RunID != "d-1" || en.CashUSD != 0.03 {
		t.Fatalf("outcome should take the measured run: %+v %v", en, err)
	}
	if _, err := e.RecordOutcome(Outcome{Model: "x/y", Outcome: "accepted", CashUSD: 0.01, Scope: Scope{Cwd: proj}}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.Status(Scope{Cwd: proj})
	if st.Spend.Runs != 2 || st.Spend.Month < 0.0399 || st.Spend.Month > 0.0401 {
		t.Errorf("spend = %+v", st.Spend)
	}
}

func TestAsksFromTheEstimate(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 0.0001\n")
	p := &catalog.Profile{VideoSec: 10}
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
