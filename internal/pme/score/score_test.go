package score

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess"
	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/bench"
	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/registry"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func model(id string, in, out float64, inMods, outMods []string) *registry.Model {
	return &registry.Model{ID: id, Kind: "text", InputMods: inMods, OutputMods: outMods, Context: ip(1_000_000),
		Caps: registry.Caps{Tools: true}, Pricing: registry.Pricing{InputPerM: fp(in), OutputPerM: fp(out)}}
}

func lead() *registry.Model {
	m := model("anthropic/test-lead", 2, 10, []string{"text", "image", "file"}, []string{"text"})
	m.Pricing.CacheReadPerM = fp(0.2)
	return m
}

var benchSet *bench.Set

func bset(t *testing.T) *bench.Set {
	t.Helper()
	if benchSet == nil {
		s, err := bench.Load(p0ssess.Data)
		if err != nil {
			t.Fatal(err)
		}
		benchSet = s
	}
	return benchSet
}

// useCase builds a synthetic use case on a family with no boards, so p_fail
// is the 0.30 prior for the recommended model and 0.40 for others.
func useCase(verify string, rec string) *catalog.UseCase {
	r := rec
	return &catalog.UseCase{ID: "test-job", Task: "Test job", Family: "test_family", Path: "direct",
		In: []string{"text"}, Out: []string{"text"}, Outputs: []string{"text"}, Recommended: &r,
		Verify: catalog.Verify{Level: verify}}
}

func baseInput(t *testing.T, uc *catalog.UseCase, models ...*registry.Model) Input {
	return Input{UseCase: uc, Work: catalog.Work{InTokens: 100_000, OutTokens: 10_000}, Stakes: 2,
		Lead: lead(), Models: models, Bench: bset(t), Lambda: 1, Cfg: Default(), Budget: budget.Default(),
		Rules: rules.Decision{}, DailyLimit: 1}
}

func find(r Result, model string) *Option {
	for i := range r.Options {
		if r.Options[i].Model == model {
			return &r.Options[i]
		}
	}
	return nil
}

func excluded(r Result, model string) string {
	for _, e := range r.Excluded {
		if e.Model == model {
			return e.Why
		}
	}
	return ""
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestFormula checks every term of the cost formula by hand.
//
//	L_direct = (100k*2 + 10k*10)/1e6 + 40k*0.2/1e6 = 0.308
//	cash     = (100k*1 + 10k*2)/1e6 * 1.055      = 0.1266
//	overhead = 0.05 (lead out price 10 = Sonnet's)
//	review   = base(level) + read(level)*10k*2/1e6
//	risk     = p_eff * (λ*L_direct + escape(level)*harm(stakes))
func TestFormula(t *testing.T) {
	const lDirect, cash = 0.308, 0.1266
	cases := []struct {
		name    string
		verify  string
		stakes  int
		lambda  float64
		review  float64
		escape  float64
		retries int
	}{
		{"spot s2 list", "spot", 2, 1, 0.03 + 0.5*0.02, 0.5, 0},
		{"spot s2 floor", "spot", 2, 0.15, 0.04, 0.5, 0},
		{"none s1", "none", 1, 1, 0.005, 1.0, 0},
		{"full s2 mid", "full", 2, 0.5, 0.06 + 0.02, 0.2, 0},
		{"spot s1 floor", "spot", 1, 0.15, 0.04, 0.5, 0},
		{"mechanical s2 retries", "mechanical", 2, 1, 0.01 + 0.1*0.02, 0.1, 2},
	}
	harm := Default().Harm
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model("x/worker", 1, 2, []string{"text"}, []string{"text"})
			in := baseInput(t, useCase(tc.verify, m.ID), m)
			in.Stakes, in.Lambda = tc.stakes, tc.lambda
			r := Score(in)
			o := find(r, m.ID)
			if o == nil {
				t.Fatalf("worker missing: %+v", r.Excluded)
			}
			p, runs := 0.30, 1.0
			if tc.retries > 0 {
				runs = 1 + p + p*p
				p = p * p * p
			}
			b := o.Breakdown
			wantRisk := p * (tc.lambda*lDirect + tc.escape*harm.At(tc.stakes))
			checks := []struct {
				name      string
				got, want float64
			}{
				{"cash", b.Cash, cash * runs},
				{"overhead", b.Overhead, tc.lambda * 0.05},
				{"review", b.Review, tc.lambda * tc.review},
				{"risk", b.Risk, wantRisk},
				{"total", o.Total, cash*runs + tc.lambda*(0.05+tc.review) + wantRisk},
				{"p_fail", o.PFail, p},
			}
			for _, c := range checks {
				if math.Abs(c.got-c.want) > 1e-6 {
					t.Errorf("%s = %.6f, want %.6f", c.name, c.got, c.want)
				}
			}
			cl := find(r, "anthropic/test-lead")
			if cl == nil {
				t.Fatal("claude option missing")
			}
			pc, rc := 0.40, 1.0
			if tc.retries > 0 {
				rc = 1 + pc + pc*pc
				pc = pc * pc * pc
			}
			wantClaude := tc.lambda*lDirect*rc + pc*(tc.lambda*lDirect+tc.escape*harm.At(tc.stakes))
			if math.Abs(cl.Total-wantClaude) > 1e-6 {
				t.Errorf("claude total %.6f, want %.6f", cl.Total, wantClaude)
			}
		})
	}
}

func TestCannotSave(t *testing.T) {
	m := model("x/worker", 0.01, 0.01, []string{"text"}, []string{"text"})
	in := baseInput(t, useCase("spot", m.ID), m)
	in.Work = catalog.Work{InTokens: 5000, OutTokens: 1000} // L_direct ≈ 0.028
	for _, lam := range []float64{0.15, 1, 3} {
		in.Lambda = lam
		r := Score(in)
		if find(r, m.ID) != nil || !strings.HasPrefix(excluded(r, m.ID), "cannot save") {
			t.Errorf("λ=%v: a job smaller than the hand-off must never be offered (%q)", lam, excluded(r, m.ID))
		}
		if len(r.Options) != 1 || r.Options[0].Route != RouteClaude {
			t.Errorf("λ=%v: Claude should be the only option", lam)
		}
	}
}

func TestLambda(t *testing.T) {
	lc := Default().Lambda
	known := func(u float64) usage.Pressure { return usage.Pressure{Known: true, Projected: u} }
	cases := []struct {
		name    string
		plan    string
		credits bool
		save    bool
		preset  string
		p       usage.Pressure
		want    float64
	}{
		{"api", "api", false, false, "balanced", known(0.9), 1},
		{"no reading", "subscription", false, false, "balanced", usage.Pressure{}, 0.15},
		{"idle", "subscription", false, false, "balanced", known(0), 0.15},
		{"half", "subscription", false, false, "balanced", known(0.5), 0.15 + 0.85*0.25},
		{"exhaustion", "subscription", false, false, "balanced", known(1), 1},
		{"past, credits", "subscription", true, false, "balanced", known(1.4), 1},
		{"past, no credits", "subscription", false, false, "balanced", known(1.4), 1.4},
		{"capped", "subscription", false, false, "balanced", known(9), 3},
		{"gentle half", "subscription", false, false, "gentle", known(0.5), 0.10 + 0.90*0.125},
		{"save claude", "subscription", false, true, "balanced", known(0.5), 0.35 + 0.65*0.5},
		{"list preset", "subscription", false, false, "list", known(0.1), 1},
	}
	for _, tc := range cases {
		c := lc
		c.Preset = tc.preset
		got := LambdaFor(tc.plan, tc.credits, tc.save, tc.p, c)
		if !near(got.Value, tc.want) {
			t.Errorf("%s: λ = %.4f, want %.4f", tc.name, got.Value, tc.want)
		}
	}
	c := lc
	c.Preset, c.Floor, c.Exponent = "custom", 0.2, 1
	if got := LambdaFor("subscription", false, false, known(0.5), c); !near(got.Value, 0.6) {
		t.Errorf("custom = %v", got.Value)
	}
}

func TestStakesGate(t *testing.T) {
	m := model("x/worker", 1, 2, []string{"text"}, []string{"text"})
	in := baseInput(t, useCase("spot", m.ID), m)
	in.Stakes = 4
	r := Score(in)
	if find(r, m.ID) != nil || !strings.Contains(excluded(r, m.ID), "stakes 4") {
		t.Fatalf("stakes 4 must stay on Claude: %+v", r)
	}
	allow := rules.Rule{ID: "a", Kind: rules.KindAllow, Target: rules.TargetModel, Models: []string{m.ID}, MaxStakes: 4}
	in.Rules = rules.Decision{Allowed: map[string]*rules.Rule{m.ID: &allow}}
	o := find(Score(in), m.ID)
	if o == nil || o.Review != "full" {
		t.Fatalf("allow rule should lift stakes 4 with full review: %+v", o)
	}
	// A Claude subagent is Claude: stakes 4 alone does not exclude it.
	sub := model("anthropic/test-sub", 0.1, 0.5, []string{"text"}, []string{"text"})
	in = baseInput(t, useCase("spot", m.ID), m)
	in.Stakes, in.Subagent = 4, sub
	if why := excluded(Score(in), sub.ID); strings.Contains(why, "stakes 4") {
		t.Errorf("subagent barred at stakes 4: %s", why)
	}
	// Ceiling: an unnamed model has p 0.40 + 0.20 = 0.60 upper.
	other := model("y/other", 1, 2, []string{"text"}, []string{"text"})
	in = baseInput(t, useCase("spot", m.ID), other)
	for stakes, pass := range map[int]bool{1: true, 2: true, 3: false} {
		in.Stakes = stakes
		got := find(Score(in), other.ID) != nil
		if got != pass {
			t.Errorf("stakes %d: offered=%v want %v", stakes, got, pass)
		}
	}
}

func TestCapabilityRoute(t *testing.T) {
	asr := &registry.Model{ID: "x/asr", Kind: "transcription", InputMods: []string{"audio"}, OutputMods: []string{"transcription"},
		Pricing: registry.Pricing{PromptRaw: fp(0.0001)}}
	uc := useCase("none", asr.ID)
	uc.In, uc.Out = []string{"audio"}, []string{"transcript"}
	in := baseInput(t, uc, asr)
	in.Work = catalog.Work{AudioSec: 600}
	r := Score(in)
	if r.ClaudeFeasible || find(r, "anthropic/test-lead") != nil {
		t.Fatal("Claude cannot take audio")
	}
	o := find(r, asr.ID)
	if o == nil {
		t.Fatalf("asr missing: %+v", r.Excluded)
	}
	cash := 0.0001 * 600 * 1.055
	if !near(o.Breakdown.Cash, cash) {
		t.Errorf("cash %.5f want %.5f", o.Breakdown.Cash, cash)
	}
	wantRisk := 0.30 * (cash + 0.05 + 1.0*0.10) // redo is another run
	if math.Abs(o.Breakdown.Risk-wantRisk) > 1e-9 {
		t.Errorf("risk %.5f want %.5f", o.Breakdown.Risk, wantRisk)
	}
	if len(o.Notes) == 0 || !strings.Contains(o.Notes[0], "unconfirmed") {
		t.Error("unit caveat missing")
	}
}

// Video is priced at the model's measured token rate when the video check has one, else at the flat rate.
func TestVideoTokenRate(t *testing.T) {
	mods := []string{"text", "video", "audio"}
	measured := model("google/gemini-3.8-flash", 1, 0, mods, []string{"text"}) // 91 tokens per second, measured
	flat := model("x/unmeasured-video", 1, 0, mods, []string{"text"})
	uc := useCase("none", measured.ID)
	uc.In = []string{"video"}
	in := baseInput(t, uc, measured, flat)
	in.Work = catalog.Work{VideoSec: 600}
	r := Score(in)
	for _, tc := range []struct {
		m    *registry.Model
		rate float64
	}{{measured, 91}, {flat, Default().Media.VideoTokensPerSec}} {
		o := find(r, tc.m.ID)
		if o == nil {
			t.Fatalf("%s missing: %+v", tc.m.ID, r.Excluded)
		}
		want := 600 * tc.rate * 1 / 1e6 * 1.055
		if !near(o.Breakdown.Cash, want) {
			t.Errorf("%s: cash %.6f want %.6f (%v tokens per second)", tc.m.ID, o.Breakdown.Cash, want, tc.rate)
		}
	}
}

func TestFitAndPrivacy(t *testing.T) {
	text := model("x/text", 1, 2, []string{"text"}, []string{"text"})
	small := model("x/small", 1, 2, []string{"text"}, []string{"text"})
	small.Context = ip(50_000)
	notools := model("x/notools", 1, 2, []string{"text"}, []string{"text"})
	notools.Caps.Tools = false
	zdr := model("x/zdr", 1, 2, []string{"text"}, []string{"text"})
	zdr.Endpoints = &registry.Endpoints{HasZDR: true, CheapestZDR: &registry.Endpoint{Provider: "P", InputPerM: fp(1.5), OutputPerM: fp(3)}}
	batch := model("x/text:batch", 0.5, 1, []string{"text"}, []string{"text"})
	in := baseInput(t, useCase("spot", text.ID), text, small, notools, zdr, batch)
	r := Score(in)
	if !strings.Contains(excluded(r, small.ID), "context") {
		t.Errorf("context filter: %q", excluded(r, small.ID))
	}
	in.NeedsTools = true
	if !strings.Contains(excluded(Score(in), notools.ID), "tool") {
		t.Error("tool filter")
	}
	in.NeedsTools = false
	in.Budget.Privacy.Mode = "zdr"
	r = Score(in)
	if find(r, text.ID) != nil || find(r, batch.ID) != nil || find(r, zdr.ID) == nil {
		t.Errorf("zdr mode filter wrong: %+v", r.Excluded)
	}
	if o := find(r, zdr.ID); !near(o.Breakdown.Cash, (100_000*1.5+10_000*3)/1e6*1.055) {
		t.Errorf("zdr mode should price the ZDR endpoint: %v", o.Breakdown.Cash)
	}
	in.Budget.Privacy.Mode = "no-training"
	in.Private = true
	r = Score(in)
	if len(r.Options) != 1 || r.Options[0].Route != RouteClaude {
		t.Error(".p0ssess-private must block delegation")
	}
}

func TestRules(t *testing.T) {
	a := model("x/a", 1, 2, []string{"text"}, []string{"text"})
	b := model("x/b", 1.1, 2, []string{"text"}, []string{"text"})
	sub := model("anthropic/test-sub", 0.1, 0.5, []string{"text"}, []string{"text"})
	in := baseInput(t, useCase("spot", a.ID), a, b)
	in.Subagent = sub
	forbid := rules.Rule{ID: "f", Kind: rules.KindForbid, Target: rules.TargetModel, Models: []string{a.ID}}
	only := rules.Rule{ID: "o", Kind: rules.KindOnly, Models: []string{a.ID}}
	noAgents := rules.Rule{ID: "c", Kind: rules.KindForbid, Target: rules.TargetClaudeAgents}
	cases := []struct {
		name string
		d    rules.Decision
		a, b bool
		sub  bool
	}{
		{"none", rules.Decision{}, true, true, true},
		{"forbid a", rules.Decision{Forbidden: map[string]*rules.Rule{a.ID: &forbid}}, false, true, true},
		{"only a", rules.Decision{Only: &only}, true, false, true},
		{"forbid beats only", rules.Decision{Only: &only, Forbidden: map[string]*rules.Rule{a.ID: &forbid}}, false, false, true},
		{"no claude agents", rules.Decision{ForbidClaudeAgents: &noAgents}, true, true, false},
	}
	for _, tc := range cases {
		in.Rules = tc.d
		r := Score(in)
		if (find(r, a.ID) != nil) != tc.a || (find(r, b.ID) != nil) != tc.b || (find(r, sub.ID) != nil) != tc.sub {
			t.Errorf("%s: a=%v b=%v sub=%v", tc.name, find(r, a.ID) != nil, find(r, b.ID) != nil, find(r, sub.ID) != nil)
		}
	}
	uc := useCase("full", a.ID)
	in = baseInput(t, uc, a)
	in.Work = catalog.Work{Turns: 20}
	oc := rules.Rule{ID: "oc", Kind: rules.KindForbid, Target: rules.TargetOpenCode}
	in.Rules = rules.Decision{ForbidOpenCode: &oc}
	if r := Score(in); find(r, a.ID) != nil {
		t.Error("forbidding OpenCode must drop agent-path routes")
	}
}

func TestApproval(t *testing.T) {
	m := model("x/worker", 1, 2, []string{"text"}, []string{"text"})
	dir := t.TempDir()
	hist := filepath.Join(dir, "h.jsonl")
	history.Append(hist, history.Entry{Type: history.TypeOutcome, Model: m.ID, Outcome: history.Accepted})
	es, _ := history.Read(hist)
	used := history.NewLedger(es)
	in := baseInput(t, useCase("spot", m.ID), m)
	in.History = used
	// the estimate, 0.1266 at list price, is above 0.02; the 1.5x headroom is the ceiling, not the test
	o := find(Score(in), m.ID)
	if !o.NeedsApproval || !strings.Contains(o.ApprovalReasons[0], "auto-approve limit") || !near(o.EstimateUSD, 0.1266) {
		t.Fatalf("expected per-task approval: %+v", o)
	}
	appr := rules.Rule{ID: "ap", Kind: rules.KindApprove, MaxPerRunUSD: 0.25}
	in.Rules = rules.Decision{Approvals: []rules.Rule{appr}}
	if o := find(Score(in), m.ID); o.NeedsApproval || o.ApprovalID != "ap" {
		t.Errorf("standing approval should cover: %+v", o)
	}
	in.Rules = rules.Decision{}
	in.Budget.PerTaskUSD = 0.15
	if o := find(Score(in), m.ID); o.NeedsApproval {
		t.Errorf("an estimate under the limit runs without asking, though its ceiling is above it: %+v", o.ApprovalReasons)
	}
	ask := rules.Rule{ID: "ask", Kind: rules.KindAsk, Scope: rules.ScopeGlobal}
	in.Rules = rules.Decision{Asks: []rules.Rule{ask}, Approvals: []rules.Rule{{ID: "g", Kind: rules.KindApprove, MaxPerRunUSD: 1, Scope: rules.ScopeGlobal}}}
	if o := find(Score(in), m.ID); !o.NeedsApproval || !strings.Contains(strings.Join(o.ApprovalReasons, ";"), "rule: ask first") {
		t.Errorf("an ask rule asks, and a standing approval does not lift it: %+v", o)
	}
	in.Rules.Approvals = append(in.Rules.Approvals, rules.Rule{ID: "s", Kind: rules.KindApprove, MaxPerRunUSD: 1, MaxCount: 1, Scope: rules.ScopeSession})
	in.Rules.Approvals[0], in.Rules.Approvals[1] = in.Rules.Approvals[1], in.Rules.Approvals[0]
	if o := find(Score(in), m.ID); o.NeedsApproval || o.ApprovalID != "s" {
		t.Errorf("a one-run session approval answers an ask rule: %+v", o)
	}
	in.Rules = rules.Decision{}
	in.Spend = history.Spend{Today: 2}
	if o := find(Score(in), m.ID); !o.NeedsApproval || !strings.Contains(o.ApprovalReasons[0], "daily") {
		t.Errorf("daily soft limit should force asking: %+v", o)
	}
	in.Spend = history.Spend{Month: 9.9}
	if r := Score(in); find(r, m.ID) != nil || !strings.Contains(excluded(r, m.ID), "monthly cap") {
		t.Error("monthly cap should exclude")
	}
	in.Spend = history.Spend{}
	in.History = history.NewLedger(nil)
	if o := find(Score(in), m.ID); o.NeedsApproval {
		t.Errorf("a first run does not ask by default: %+v", o.ApprovalReasons)
	}
	in.Budget.Ask.FirstUse = true
	if o := find(Score(in), m.ID); !o.NeedsApproval || o.ApprovalReasons[len(o.ApprovalReasons)-1] != "first use of this model" {
		t.Errorf("first use should ask when ask.first_use is on: %+v", o.ApprovalReasons)
	}
}

func TestHistoryAdjusts(t *testing.T) {
	m := model("x/worker", 1, 2, []string{"text"}, []string{"text"})
	in := baseInput(t, useCase("spot", m.ID), m)
	base := find(Score(in), m.ID).PFail
	var es []history.Entry
	for i := 0; i < 3; i++ {
		es = append(es, history.Entry{Type: history.TypeOutcome, Model: m.ID, UseCase: "test-job", Family: "test_family", Outcome: history.Redo, TS: time.Now()})
	}
	in.History = history.NewLedger(es)
	in.Stakes = 1
	o := find(Score(in), m.ID)
	// prior 0.30 with strength 3, plus 3 failures: (0.9 + 3) / 6
	if o == nil || !near(o.PFail, (0.30*3+3)/6) {
		t.Fatalf("p_fail after redos = %+v (base %.2f)", o, base)
	}
	if !strings.Contains(o.History, "3 redone") {
		t.Errorf("history line = %q", o.History)
	}
	es = nil
	for i := 0; i < 6; i++ {
		es = append(es, history.Entry{Type: history.TypeOutcome, Model: m.ID, UseCase: "test-job", Family: "test_family", Outcome: history.Accepted})
	}
	in.History = history.NewLedger(es)
	in.Stakes = 2
	if o := find(Score(in), m.ID); o.PFail >= base {
		t.Errorf("accepts should lower p_fail: %.3f vs %.3f", o.PFail, base)
	}
}

func TestMixNext(t *testing.T) {
	a := model("x/a", 1, 2, []string{"text"}, []string{"text"})
	b := model("x/b", 1, 2, []string{"text"}, []string{"text"})
	mix := rules.Rule{ID: "m", Kind: rules.KindMix, Models: []string{a.ID, b.ID}, Weights: []float64{1, 1}}
	es := []history.Entry{
		{Type: history.TypeOutcome, Model: a.ID, UseCase: "test-job", Outcome: history.Accepted},
		{Type: history.TypeOutcome, Model: a.ID, UseCase: "test-job", Outcome: history.Accepted},
	}
	in := baseInput(t, useCase("spot", a.ID), a, b)
	in.Rules = rules.Decision{Only: &mix}
	in.History = history.NewLedger(es)
	r := Score(in)
	if o := find(r, b.ID); o == nil || !o.MixNext {
		t.Errorf("b is behind its share and should be next: %+v", r.Options)
	}
}

func TestConfigLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(filepath.Join(dir, "missing.toml"))
	if err != nil || c.Ceilings.S3 != 0.40 {
		t.Fatalf("default config: %v", err)
	}
	p := filepath.Join(dir, "engine.toml")
	writeFile(t, p, "[harm]\ns3 = 5.0\n[lambda]\npreset = \"gentle\"\n")
	c, err = LoadConfig(p)
	if err != nil || c.Harm.S3 != 5 || c.Harm.S2 != 0.10 || c.Lambda.Preset != "gentle" {
		t.Errorf("override: %+v %v", c.Harm, err)
	}
	writeFile(t, p, "[ceilings]\ns2 = 2.0\n")
	if _, err := LoadConfig(p); err == nil {
		t.Error("ceiling above 1 should fail")
	}
}

func TestConfigRejectsHeadroomBelowOne(t *testing.T) {
	for _, in := range []string{"cash_headroom = 0\n", "cash_headroom = 0.5\n", "cash_headroom = nan\n", "credit_fee = -1\n"} {
		if _, err := LoadConfigFrom([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if _, err := LoadConfigFrom([]byte("cash_headroom = 2\n")); err != nil {
		t.Error(err)
	}
}

// TestUnknownPriceIsNotCheap covers M6: a model listed at zero that is not known to be free is left
// out with "price unknown" instead of being ranked as the cheapest option.
func TestUnknownPriceIsNotCheap(t *testing.T) {
	unknown := model("newco/zero", 0, 0, []string{"text"}, []string{"text"})
	unknown.Live = true
	free := model("newco/zero:free", 0, 0, []string{"text"}, []string{"text"})
	free.Live = true
	paid := model("vendor/paid", 1, 2, []string{"text"}, []string{"text"})
	r := Score(baseInput(t, useCase("spot", "vendor/paid"), unknown, free, paid))
	if find(r, "newco/zero") != nil || !strings.Contains(excluded(r, "newco/zero"), "price unknown") {
		t.Errorf("unknown-priced model ranked (excluded: %q)", excluded(r, "newco/zero"))
	}
	if o := find(r, "newco/zero:free"); o == nil || o.Breakdown.Cash != 0 {
		t.Errorf("free model should be ranked at $0: %+v (excluded: %q)", o, excluded(r, "newco/zero:free"))
	}
	if find(r, "vendor/paid") == nil {
		t.Errorf("paid model not ranked: %q", excluded(r, "vendor/paid"))
	}
}
