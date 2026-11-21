package local

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

// askArgs pulls the approvals_add arguments out of a needs-approval refusal and saves the rule the way
// approvals_add does.
func askArgs(t *testing.T, e *Engine, err error) rules.Rule {
	t.Helper()
	m := regexp.MustCompile(`call approvals_add with (\{.*?\}), then`).FindStringSubmatch(errString(err))
	if m == nil {
		t.Fatalf("no approval ask in %v", err)
	}
	var a struct {
		rules.Rule
		ScopeName string `json:"scope"`
		Session   string `json:"session_id"`
		ExpiresIn int    `json:"expires_in_minutes"`
	}
	if err := json.Unmarshal([]byte(m[1]), &a); err != nil {
		t.Fatal(err)
	}
	r := a.Rule
	r.Kind, r.Scope = rules.KindApprove, a.ScopeName
	saved, err := e.RuleAdd(Scope{Session: a.Session}, r, false, a.ExpiresIn)
	if err != nil {
		t.Fatalf("approvals_add %s: %v", m[1], err)
	}
	return saved.Rule
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestOneYesCoversTheTask: the PME asks once; that yes covers the run and its retries from any entry
// point of the Claude Code session, and a run that failed before reaching a model leaves the approval's
// count and the spend unchanged (1.0.4 items 6, 9, 11 and design point A).
func TestOneYesCoversTheTask(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 0.0001\ndaily_usd = 1\n")
	// The Claude Code session works in the directory above the project; a command run from the project
	// (the CLI) finds it.
	var in usage.StatusInput
	in.SessionID, in.Cwd = "sess-A", filepath.Dir(proj)
	in.Workspace.CurrentDir = filepath.Dir(proj)
	usage.Update(e.Dirs.UsageFile(), func(s *usage.State) { s.Record(in, time.Now()) })

	agent := Charge{Route: "agent", Model: cheap, MaxUSD: 0.5, Fit: true, Hold: time.Hour, Scope: Scope{Cwd: proj}}
	_, err := e.Authorize(agent)
	if !strings.Contains(errString(err), `"session_id":"sess-A"`) {
		t.Fatalf("the ask must name the session of the entry point: %v", err)
	}
	ap := askArgs(t, e, err)
	if ap.Scope != rules.ScopeSession || ap.MaxCount != TaskRetries {
		t.Fatalf("saved approval %+v", ap)
	}

	used := func() int {
		l := e.Ledger()
		return l.ApprovalUse([]rules.Rule{ap}, e.Now())[ap.ID].Count
	}
	// First try: authorized under the yes, then it fails before any model call.
	g, err := e.Authorize(agent)
	if err != nil || g.ApprovalID != ap.ID {
		t.Fatalf("first try: %+v %v", g, err)
	}
	if err := e.Settle(&Grant{ReserveID: g.ReserveID, Session: g.Session}, Run{ID: "ses1/1", Route: "agent", Model: cheap, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if n, sp := used(), e.Ledger().Spend(e.Now()); n != 0 || sp.Month != 0 || sp.Reserved != 0 {
		t.Fatalf("a run that never reached a model counted: uses %d, spend %+v", n, sp)
	}
	// A failed run that spent nothing still tagged with the approval does not use it up either.
	g, _ = e.Authorize(agent)
	e.Settle(g, Run{ID: "ses1/2", Route: "agent", Model: cheap, Status: "failed"})
	if n := used(); n != 0 {
		t.Fatalf("an unbilled failure used the approval: %d", n)
	}
	// Retries from the direct path (another entry point, no session passed) ask nothing.
	direct := Charge{Route: "direct", Model: cheap, MaxUSD: 0.01, ExplicitMax: true, EstimateUSD: 0.01, Hold: time.Hour, Scope: Scope{Cwd: proj}}
	for i := 0; i < TaskRetries; i++ {
		g, err := e.Authorize(direct)
		if err != nil {
			t.Fatalf("retry %d asked again: %v", i+1, err)
		}
		e.Settle(g, Run{ID: "d-" + string(rune('a'+i)), Route: "direct", Model: cheap, CashUSD: 0.002, Status: "done"})
	}
	if _, err := e.Authorize(direct); !strings.Contains(errString(err), "needs approval") {
		t.Errorf("after the task's runs the yes is used up: %v", err)
	}
}

// TestDailyWarnsMonthlyStops: past the daily limit a run goes ahead with a note; the monthly limit
// stops it.
func TestDailyWarnsMonthlyStops(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 1\nper_task_usd = 1\ndaily_usd = 0.05\n")
	history.Append(e.Dirs.HistoryFile(), history.Entry{TS: time.Now().UTC(), Type: history.TypeRun, RunID: "r0", Route: "direct", Model: cheap, CashUSD: 0.04, Status: "done"})
	g, err := e.Authorize(charge(proj, 0.03))
	if err != nil {
		t.Fatalf("the daily limit must not stop a run: %v", err)
	}
	if !strings.Contains(strings.Join(g.Notes, " "), "daily limit") {
		t.Errorf("no daily warning: %v", g.Notes)
	}
	e.Release(g)
	_, err = e.Authorize(charge(proj, 0.99))
	refused(t, err, false, "monthly cap")
}

// TestReserveAboutTheEstimate: the ledger holds about 1.5 times a run's estimate, not its whole default
// ceiling (1.0.4 item 7: "up to $0.50" was reserved for a job that cost cents).
func TestReserveAboutTheEstimate(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 1\ndaily_usd = 1\n")
	est, ok := e.AgentEstimate(cheap, "")
	if !ok {
		t.Fatal("no estimate")
	}
	g, err := e.Authorize(Charge{Route: "agent", Model: cheap, MaxUSD: 0.5, Fit: true, Hold: time.Hour, Scope: Scope{Cwd: proj, Session: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if g.MaxUSD != 0.5 {
		t.Errorf("the ceiling is still the run's cap: %v", g.MaxUSD)
	}
	sp := e.Ledger().Spend(e.Now())
	if want := ReserveFactor * est; sp.Reserved < want-1e-9 || sp.Reserved > want+1e-9 {
		t.Errorf("reserved %v, want 1.5 x %v", sp.Reserved, est)
	}
}

func TestPickInsideTheEnvelope(t *testing.T) {
	opts := []Option{
		{Route: RouteAgent, Model: "openai/gpt-6.1-sol", EstimateUSD: 0.42, NeedsApproval: true, Total: 0.5},
		{Route: RouteClaude, Model: "anthropic/claude-sonnet-5.5", Total: 0.6},
		{Route: RouteAgent, Model: cheap, EstimateUSD: 0.05, Total: 0.7},
	}
	if p := pickInside(opts); p.Model != cheap || !strings.Contains(strings.Join(p.Notes, " "), "gpt-6.1-sol would cost about $0.420") {
		t.Errorf("pick = %+v", p)
	}
	opts[2].NeedsApproval = true
	if p := pickInside(opts); p.Model != "openai/gpt-6.1-sol" {
		t.Errorf("nothing inside: the best is kept (and asked once), got %s", p.Model)
	}
}

func TestFreeDefaultsForNewInstallsOnly(t *testing.T) {
	dir := t.TempDir()
	b, _ := budget.LoadFor(filepath.Join(dir, "budget.toml"), false)
	if b.DailyUSD != 1 || b.MonthlyUSD != 10 {
		t.Errorf("new install: %+v", b)
	}
	b, _ = budget.LoadFor(filepath.Join(dir, "budget.toml"), true)
	if b.DailyUSD != 0 {
		t.Errorf("an install that has run work keeps its paced daily limit: %v", b.DailyUSD)
	}
	e, _ := setup(t, "monthly_usd = 20\n")
	if b, _ := e.Budget(); b.DailyUSD != 0 || b.MonthlyUSD != 20 {
		t.Errorf("a saved budget is never changed: %+v", b)
	}
}
