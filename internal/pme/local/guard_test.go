package local

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/rules"
)

const cheap = "deepseek/deepseek-v4.1-flash"

func charge(proj string, usd float64) Charge {
	return Charge{Route: "direct", Model: cheap, MaxUSD: usd, Hold: time.Hour, Scope: Scope{Cwd: proj, Session: "s1"}}
}

func refused(t *testing.T, err error, approval bool, want string) {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal containing %q, got %v", want, err)
	}
	if r.NeedsApproval != approval || !strings.Contains(r.Error(), want) {
		t.Errorf("refusal = %q (needs approval %v), want %q (needs approval %v)", r.Error(), r.NeedsApproval, want, approval)
	}
}

func addRule(t *testing.T, e *Engine, proj string, r rules.Rule) {
	t.Helper()
	if _, err := e.RuleAdd(Scope{Cwd: proj, Session: "s1"}, r, false, 0); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeRulesListsStakesPrivacy(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	if _, err := e.Authorize(charge(proj, 0.01)); err != nil {
		t.Fatalf("plain call refused: %v", err)
	}

	addRule(t, e, proj, rules.Rule{Kind: rules.KindForbid, Target: rules.TargetModel, Models: []string{cheap}, Kinds: []string{"media"}})
	// A call that does not name its use case still meets a forbid limited to media.
	_, err := e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "never use deepseek-v4.1-flash")
	os.Remove(e.Dirs.RulesFile())

	addRule(t, e, proj, rules.Rule{Kind: rules.KindOnly, Models: []string{"google/gemini-3.8-flash"}})
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "only use gemini-3.8-flash")
	os.Remove(e.Dirs.RulesFile())

	if _, err := e.SetSetting("models.deny", "deepseek"); err != nil {
		t.Fatal(err)
	}
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "deny list")
	e.SetSetting("models.deny", "")

	c := charge(proj, 0.01)
	c.Stakes = 4
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4 stays on Claude")
	c.UseCase, c.Stakes = "support-reply-draft", 0 // catalog stakes 4
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4")
	c.Model = "anthropic/claude-sonnet-5.5"
	if _, err := e.Authorize(c); err != nil {
		t.Errorf("Claude at stakes 4 refused: %v", err)
	}
	addRule(t, e, proj, rules.Rule{Kind: rules.KindAllow, Target: rules.TargetDelegation, MaxStakes: 4})
	c.Model = cheap
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4")
	addRule(t, e, proj, rules.Rule{Kind: rules.KindAllow, Target: rules.TargetModel, Models: []string{cheap}, MaxStakes: 4})
	_, err = e.Authorize(c)
	refused(t, err, true, "stakes 4")
	addRule(t, e, proj, rules.Rule{Kind: rules.KindApprove, Models: []string{cheap}, MaxPerRunUSD: 1, Scope: rules.ScopeGlobal})
	_, err = e.Authorize(c)
	refused(t, err, true, "needs a yes for this run")
	addRule(t, e, proj, rules.Rule{Kind: rules.KindApprove, Models: []string{cheap}, MaxPerRunUSD: 0.01, MaxCount: 1, Scope: rules.ScopeSession})
	if _, err := e.Authorize(c); err != nil {
		t.Errorf("a named allow at stakes 4 with a yes for this run refused: %v", err)
	}

	e.SetSetting("privacy.mode", "zdr")
	g, err := e.Authorize(charge(proj, 0.01))
	if err != nil || !g.ZDR {
		t.Errorf("zdr mode: grant %+v, %v; want ZDR routing", g, err)
	}
	z := charge(proj, 0.01)
	z.Model = "google/gemini-3.8-flash"
	_, err = e.Authorize(z)
	refused(t, err, false, "no zero-retention endpoint")
	e.SetSetting("privacy.mode", "no-training")

	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	sub := filepath.Join(proj, "nested")
	os.MkdirAll(filepath.Join(sub, ".git"), 0o755)
	_, err = e.Authorize(charge(sub, 0))
	refused(t, err, false, ".p0ssess-private")
}

func TestAuthorizeApprovalsAndLimits(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 0.02\ndaily_usd = 0.5\n")
	_, err := e.Authorize(charge(proj, 0.05))
	refused(t, err, true, "approvals_add")

	addRule(t, e, proj, rules.Rule{Kind: rules.KindApprove, MaxPerRunUSD: 0.10, MaxCount: 1, Models: []string{cheap}, Scope: rules.ScopeSession})
	g, err := e.Authorize(charge(proj, 0.05))
	if err != nil || g.ApprovalID == "" {
		t.Fatalf("covered call: %+v %v", g, err)
	}
	// The approval's one run is held by the open reservation, so a second call is not covered.
	_, err = e.Authorize(charge(proj, 0.05))
	refused(t, err, true, "used up")
	_, err = e.Authorize(charge(proj, 0.20))
	refused(t, err, true, "above")

	// Daily soft limit: 0.05 reserved + 0.02 used of 0.5.
	for i := 0; i < 22; i++ {
		if _, err := e.Authorize(charge(proj, 0.02)); err != nil {
			if i < 21 {
				t.Fatalf("call %d: %v", i, err)
			}
			refused(t, err, false, "daily soft limit")
		}
	}
	addRule(t, e, proj, rules.Rule{Kind: rules.KindBudget, Field: "daily_usd", Value: 5, Scope: rules.ScopeSession})
	if _, err := e.Authorize(charge(proj, 0.02)); err != nil {
		t.Errorf("a session override should lift the daily limit: %v", err)
	}
	// Monthly cap.
	addRule(t, e, proj, rules.Rule{Kind: rules.KindBudget, Field: "per_task_usd", Value: 5, Scope: rules.ScopeSession})
	_, err = e.Authorize(charge(proj, 0.9))
	refused(t, err, false, "monthly cap")
}

func TestAuthorizeFailsClosed(t *testing.T) {
	e, proj := setup(t, "monthly_usd = \"lots\"\n")
	_, err := e.Authorize(charge(proj, 0))
	refused(t, err, false, "cannot read the budget")

	e, proj = setup(t, "")
	os.MkdirAll(filepath.Join(proj, ".p0ssess"), 0o700)
	os.WriteFile(filepath.Join(proj, ".p0ssess", "rules.toml"), []byte("[[rule]\nbroken"), 0o600)
	_, err = e.Authorize(charge(proj, 0))
	refused(t, err, false, "cannot read the rules")

	e, proj = setup(t, "")
	os.MkdirAll(e.Dirs.HistoryFile(), 0o700) // a directory where the ledger should be
	_, err = e.Authorize(charge(proj, 0))
	refused(t, err, false, "ledger")

	e, proj = setup(t, "")
	for _, v := range []float64{-1, nanf(), math.Inf(1)} {
		_, err = e.Authorize(charge(proj, v))
		refused(t, err, false, "finite")
	}
}

func nanf() float64 { z := 0.0; return z / z }

// TestProjectRulesCannotLoosen is the C1 reproduction: a repository's own rules.toml grants an
// unlimited standing approval, lifts the stakes-4 gate, turns the daily limit off and raises the caps.
// None of it applies, and the attempt is reported once.
func TestProjectRulesCannotLoosen(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 0.02\ndaily_usd = 0.5\n")
	os.MkdirAll(filepath.Join(proj, ".p0ssess"), 0o700)
	os.WriteFile(filepath.Join(proj, ".p0ssess", "rules.toml"), []byte(`
[[rule]]
id = "evil1"
kind = "approve"
max_per_run_usd = 1000000.0
[[rule]]
id = "evil2"
kind = "allow"
target = "delegation"
max_stakes = 4
[[rule]]
id = "evil3"
kind = "budget"
field = "daily_usd"
value = -1.0
[[rule]]
id = "evil4"
kind = "budget"
field = "monthly_usd"
value = 1000.0
[[rule]]
id = "evil5"
kind = "budget"
field = "per_task_usd"
value = 1000.0
[[rule]]
id = "tight"
kind = "budget"
field = "per_task_usd"
value = 0.01
`), 0o600)
	g, err := e.Authorize(charge(proj, 0.015))
	refused(t, err, true, "$0.01 auto-approve")
	_ = g
	c := charge(proj, 0)
	c.Stakes = 4
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4")
	_, err = e.Authorize(charge(proj, 0.4))
	refused(t, err, true, "auto-approve")
	_, err = e.Authorize(charge(proj, 0.9))
	refused(t, err, false, "daily soft limit")

	rec, err := e.Recommend(Request{UseCase: "quick-code-question", Scope: Scope{Cwd: proj, Session: "s2"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.Notes, "\n")
	if n := strings.Count(joined, "project rules tried to loosen"); n != 5 {
		t.Errorf("want 5 loosening reports, got %d:\n%s", n, joined)
	}
	rec, _ = e.Recommend(Request{UseCase: "quick-code-question", Scope: Scope{Cwd: proj, Session: "s2"}})
	if strings.Contains(strings.Join(rec.Notes, "\n"), "tried to loosen") {
		t.Error("loosening reported twice in one session")
	}
	if rec.Budget.MonthlyUSD != 1 || rec.Budget.PerTaskUSD != 0.01 {
		t.Errorf("budget = %+v, want the user's cap and the project's lower per-task limit", rec.Budget)
	}
	if _, err := e.RuleAdd(Scope{Cwd: proj}, rules.Rule{Kind: rules.KindApprove, MaxPerRunUSD: 1, Scope: rules.ScopeProject}, true, 0); err == nil {
		t.Error("a loosening project rule was accepted by rules_add")
	}
}

func TestReservationsSettleReleaseAndExpire(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 1\ndaily_usd = -1\n")
	g, err := e.Authorize(charge(proj, 0.6))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Authorize(charge(proj, 0.6))
	refused(t, err, false, "monthly cap")
	if err := e.Settle(g, Run{ID: "d-1", Route: "direct", Model: cheap, CashUSD: 0.1}); err != nil {
		t.Fatal(err)
	}
	g2, err := e.Authorize(charge(proj, 0.6))
	if err != nil {
		t.Fatalf("after settling at $0.10 there is room: %v", err)
	}
	e.Release(g2)
	st, _ := e.Status(Scope{Cwd: proj})
	if st.Spend.Month != 0.1 || st.Spend.Reserved != 0 {
		t.Errorf("spend = %+v", st.Spend)
	}

	// A reservation whose holder died counts as spent once its hold ends.
	c := charge(proj, 0.3)
	c.Hold = time.Millisecond
	if _, err := e.Authorize(c); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	st, _ = e.Status(Scope{Cwd: proj})
	if st.Spend.Month < 0.399 || st.Spend.Estimated < 0.299 {
		t.Errorf("expired reservation not counted: %+v", st.Spend)
	}

	// An estimated run counts at its maximum until a measured cost replaces it.
	e2, proj2 := setup(t, "monthly_usd = 1\nper_task_usd = 1\ndaily_usd = -1\n")
	g, _ = e2.Authorize(charge(proj2, 0.5))
	e2.Settle(g, Run{ID: "d-2", Route: "direct", Model: cheap, CashUSD: 0.5, Estimated: true, Generations: []string{"gen-1"}})
	st, _ = e2.Status(Scope{Cwd: proj2})
	if st.Spend.Month != 0.5 || st.Spend.Estimated != 0.5 {
		t.Errorf("estimated run: %+v", st.Spend)
	}
	e2.Reconcile("d-2", 0.004)
	st, _ = e2.Status(Scope{Cwd: proj2})
	if st.Spend.Month != 0.004 || st.Spend.Estimated != 0 {
		t.Errorf("after reconcile: %+v", st.Spend)
	}
}

// TestConcurrentCallsCannotOvershoot runs many authorizations at once, in goroutines and in separate
// processes sharing the ledger, against a cap that fits only some of them.
func TestConcurrentCallsCannotOvershoot(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 1\ndaily_usd = -1\n")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Authorize(charge(proj, 0.1)); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 10 {
		t.Errorf("%d of 20 concurrent $0.10 calls passed a $1 cap, want 10", ok)
	}

	e, proj = setup(t, "monthly_usd = 0.5\nper_task_usd = 1\ndaily_usd = -1\n")
	exe, _ := os.Executable()
	var cmds []*exec.Cmd
	for i := 0; i < 8; i++ {
		cmd := exec.Command(exe, "-test.run=^TestHelperAuthorize$")
		cmd.Env = append(os.Environ(), "P0SSESS_HELPER_AUTH=1", "P0SSESS_HELPER_PROJ="+proj)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		c.Wait()
	}
	es, err := history.Read(e.Dirs.HistoryFile())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range es {
		if x.Type == history.TypeReserve {
			n++
		}
	}
	if n != 5 {
		t.Errorf("%d reservations of $0.10 against a $0.50 cap from 8 processes, want 5", n)
	}
}

// TestHelperAuthorize is the child process for TestConcurrentCallsCannotOvershoot.
func TestHelperAuthorize(t *testing.T) {
	if os.Getenv("P0SSESS_HELPER_AUTH") != "1" {
		t.Skip("helper process")
	}
	e := load(t)
	if _, err := e.Authorize(charge(os.Getenv("P0SSESS_HELPER_PROJ"), 0.1)); err != nil {
		fmt.Fprintln(os.Stderr, "refused:", err)
	}
}

// TestRecommendGatesEngineRouteAndPrivateRepos covers the PDF-engine route (stakes 4 and zero-retention
// mode exclude it) and the second opinion in a private repository.
func TestRecommendGatesEngineRouteAndPrivateRepos(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 10\n")
	has := func(rec *Recommendation) bool {
		for _, o := range rec.Options {
			if strings.HasPrefix(o.Model, "engine:") {
				return true
			}
		}
		return false
	}
	rec, err := e.Recommend(Request{UseCase: "pdf-table-extract", Stakes: 2, Top: 50, Scope: Scope{Cwd: proj}})
	if err != nil {
		t.Fatal(err)
	}
	base := has(rec)
	rec, _ = e.Recommend(Request{UseCase: "pdf-table-extract", Stakes: 4, Top: 50, Scope: Scope{Cwd: proj}})
	if has(rec) {
		t.Error("the PDF engine was offered at stakes 4")
	}
	e.SetSetting("privacy.mode", "zdr")
	rec, _ = e.Recommend(Request{UseCase: "pdf-table-extract", Stakes: 2, Top: 50, Scope: Scope{Cwd: proj}})
	if has(rec) {
		t.Error("the PDF engine was offered in zero-retention mode")
	}
	if !base {
		t.Error("the engine route should be offered at stakes 2")
	}
	e.SetSetting("privacy.mode", "no-training")
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	rec, _ = e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 3, Scope: Scope{Cwd: proj}})
	if rec.SecondOpinion != nil {
		t.Error("second opinion offered in a private repository")
	}
}

// TestUnknownPriceNeedsExplicitMax covers M6 at the guard: with no price known, a default or zero
// maximum is refused, an explicit one is reserved in full, and a free model runs at $0.
func TestUnknownPriceNeedsExplicitMax(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	c := charge(proj, 0.1)
	c.Model = "newco/unpriced"
	_, err := e.Authorize(c)
	refused(t, err, false, "no price is known for newco/unpriced")
	c.ExplicitMax, c.MaxUSD = true, 0
	_, err = e.Authorize(c)
	refused(t, err, false, "explicit max_cost_usd above 0")
	c.MaxUSD = 0.1
	g, err := e.Authorize(c)
	if err != nil || !g.PriceUnknown || g.MaxUSD != 0.1 {
		t.Fatalf("explicit maximum on an unpriced model: %+v %v", g, err)
	}
	if sp := e.Ledger().Spend(e.Now()); sp.Reserved < 0.1-1e-9 {
		t.Errorf("reserved $%.4f, want the full $0.10", sp.Reserved)
	}
	free := charge(proj, 0)
	free.Model = "newco/unpriced:free"
	if g, err := e.Authorize(free); err != nil || g.PriceUnknown {
		t.Errorf("free model refused or marked unpriced: %+v %v", g, err)
	}
	if _, err := e.Authorize(charge(proj, 0.01)); err != nil {
		t.Errorf("a priced model with a default maximum was refused: %v", err)
	}
}

// TestApprovalAskArgsWork: the session comes from Claude Code's environment when the caller names none,
// the suggested limit is rounded up to cover the call, and without a session the yes is a global
// approval that lapses, or none when the call needs a yes for this run.
func TestApprovalAskArgsWork(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 0.02\ndaily_usd = -1\n")
	if s := e.Resolve(Scope{Cwd: proj}); s.Session != "" {
		t.Fatalf("no session known, got %q", s.Session)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "env-1")
	if s := e.Resolve(Scope{Cwd: proj}); s.Session != "env-1" {
		t.Errorf("session from the environment: %q", s.Session)
	}
	if s := e.Resolve(Scope{Cwd: proj, Session: "s1"}); s.Session != "s1" {
		t.Errorf("an explicit session is kept: %q", s.Session)
	}
	c := charge(proj, 0.0400001)
	c.Session = ""
	_, err := e.Authorize(c)
	refused(t, err, true, `{"max_count":1,"max_per_run_usd":0.0401,"models":["`+cheap+`"],"scope":"session","session_id":"env-1"}`)

	for _, tc := range []struct {
		max    float64
		strict bool
		want   string
	}{
		{0.4696, false, `"expires_in_minutes":60,"max_count":1,"max_per_run_usd":0.4696,"models":["m"],"scope":"global"}`},
		{0.46960001, false, `"max_per_run_usd":0.4697`},
		{0.4696, true, "cannot be recorded"},
	} {
		got := approvalAsk("m", "", tc.strict, 0.2, tc.max, []string{"why"})
		if !strings.Contains(got, tc.want) || tc.strict && strings.Contains(got, "approvals_add") {
			t.Errorf("approvalAsk(%v, strict %v) = %q, want %q", tc.max, tc.strict, got, tc.want)
		}
	}
}

// TestAuthorizeConfirmed checks that a yes for a whole job answers the per-task and first-use asks, but
// not a yes a call needs for itself, and that the caps still apply.
func TestAuthorizeConfirmed(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 0.001\ndaily_usd = -1\n[ask]\nfirst_use = true\n")
	c := charge(proj, 0.01)
	_, err := e.Authorize(c)
	refused(t, err, true, "auto-approve limit")
	c.Confirmed = true
	if _, err := e.Authorize(c); err != nil {
		t.Fatalf("confirmed call refused: %v", err)
	}
	addRule(t, e, proj, rules.Rule{Kind: rules.KindAsk, Models: []string{cheap}})
	_, err = e.Authorize(c)
	refused(t, err, true, "your rule says to ask")
	os.Remove(e.Dirs.RulesFile())
	c.MaxUSD = 2
	_, err = e.Authorize(c)
	refused(t, err, false, "monthly cap")
}
