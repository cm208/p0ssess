package local

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/pme/table/tabletest"
)

// noLeadJobs keep on Claude when a Claude session leads, and go to a direct model when none does.
var noLeadJobs = []struct {
	useCase string
	stakes  int
	profile *Profile
	model   string
}{
	{"invoice-receipt-extract", 3, &Profile{Images: 1, InTokens: 4000}, "google/gemini-3.8-flash"},
	{"invoice-receipt-extract", 2, &Profile{Images: 1, InTokens: 4000}, "google/gemini-3.8-flash"},
	{"ocr-degraded-image", 2, &Profile{Images: 1}, "google/gemini-3.8-flash"},
	{"ocr-degraded-image", 3, &Profile{Images: 1}, "google/gemini-3.8-flash"},
	{"qa-factual", 2, nil, "deepseek/deepseek-v4.1-flash"},
	{"qa-factual", 3, nil, "deepseek/deepseek-v4.1-flash"},
}

func writeEngine(t *testing.T, e *Engine, body string) {
	t.Helper()
	if err := os.WriteFile(e.Dirs.EngineFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func noClaude(t *testing.T, what string, r *Recommendation) {
	t.Helper()
	for _, o := range r.Options {
		if o.Route == RouteClaude || o.Route == RouteSubagent || o.Route == RouteLocal {
			t.Errorf("%s: no-lead answer offers %s %s", what, o.Route, o.Model)
		}
	}
	if r.Lead != LeadNone || r.ClaudeFeasible {
		t.Errorf("%s: lead %q claude_feasible %v", what, r.Lead, r.ClaudeFeasible)
	}
}

func TestNoLeadFromTheTable(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 1\n")
	for _, j := range noLeadJobs {
		r, err := e.Recommend(Request{UseCase: j.useCase, Stakes: j.stakes, Profile: j.profile, Scope: Scope{Cwd: proj}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Pick == nil || r.Pick.Route != RouteClaude {
			t.Fatalf("%s/%d with a lead: %+v", j.useCase, j.stakes, r.Pick)
		}
	}
	writeEngine(t, e, "lead = \"none\"\n")
	for _, j := range noLeadJobs {
		r, err := e.Recommend(Request{UseCase: j.useCase, Stakes: j.stakes, Profile: j.profile, Scope: Scope{Cwd: proj}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Pick == nil || r.Pick.Route != RouteDirect || r.Pick.Model != j.model {
			t.Fatalf("%s/%d with no lead: pick %+v, options %+v, notes %v", j.useCase, j.stakes, r.Pick, r.Options, r.Notes)
		}
		noClaude(t, j.useCase, r)
		if !strings.HasPrefix(r.Announce, "handing this to ") {
			t.Errorf("announce %q", r.Announce)
		}
	}
	// The stakes-4 rule and the user's rules and limits still apply.
	r, _ := e.Recommend(Request{UseCase: "invoice-receipt-extract", Stakes: 4, Scope: Scope{Cwd: proj}})
	if r.Pick != nil || len(r.Options) != 0 {
		t.Errorf("stakes 4 with no lead: %+v", r.Options)
	}
	os.WriteFile(e.Dirs.BudgetFile(), []byte("plan = \"api\"\nper_task_usd = 1\n[models]\ndeny = [\"deepseek\"]\n"), 0o600)
	r, _ = e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if r.Pick == nil || r.Pick.Model != "z-ai/glm-5.3-flash" {
		t.Errorf("deny list with no lead: %+v", r.Pick)
	}
	os.WriteFile(e.Dirs.BudgetFile(), []byte("plan = \"api\"\nper_task_usd = 0.0001\n"), 0o600)
	r, _ = e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if r.Pick == nil || !r.Pick.NeedsApproval {
		t.Errorf("approval with no lead: %+v", r.Pick)
	}
	// A private repository still delegates nothing.
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	r, _ = e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if r.Pick != nil {
		t.Errorf("private repository with no lead: %+v", r.Pick)
	}
}

func TestNoLeadEnvironment(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 1\n")
	t.Setenv("P0SSESS_LEAD", "none")
	r, err := e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if err != nil || r.Pick == nil || r.Pick.Route != RouteDirect {
		t.Fatalf("P0SSESS_LEAD=none: %v %+v", err, r)
	}
	writeEngine(t, e, "lead = \"none\"\n")
	t.Setenv("P0SSESS_LEAD", "claude")
	r, _ = e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if r.Pick == nil || r.Pick.Route != RouteClaude {
		t.Errorf("P0SSESS_LEAD=claude overrides engine.toml: %+v", r.Pick)
	}
	t.Setenv("P0SSESS_LEAD", "nobody")
	if _, err := e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}}); err == nil || !strings.Contains(err.Error(), "P0SSESS_LEAD") {
		t.Errorf("bad P0SSESS_LEAD: %v", err)
	}
	t.Setenv("P0SSESS_LEAD", "")
	writeEngine(t, e, "lead = \"gpt\"\n")
	if _, err := e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}}); err == nil {
		t.Error("lead = \"gpt\" accepted")
	}
}

func TestNoLeadSetting(t *testing.T) {
	e, _ := setup(t, "")
	if _, err := e.SetSetting("lead", "none"); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.Config()
	if err != nil || !cfg.NoLead() {
		t.Fatalf("lead none: %v %+v", err, cfg)
	}
	if _, err := e.SetSetting("lead", "someone"); err == nil {
		t.Error("lead someone accepted")
	}
	if _, err := e.SetSetting("lead", "claude"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := e.Config(); cfg.NoLead() {
		t.Error("lead claude still no-lead")
	}
}

func TestNoLeadOnTheWire(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 1\n")
	srv := tabletest.Serve(t, func(req remote.Request) (int, any) {
		resp := remote.Response{V: 1, UseCase: remote.UseCase{ID: "qa-factual", Kind: "text"}, Stakes: 2, LeadModel: "none", Pick: 0,
			Options: []remote.Option{
				{Rank: 1, Route: RouteClaude, Model: "anthropic/claude-sonnet-5.5", Total: 0.001},
				{Rank: 2, Route: RouteDirect, Model: "deepseek/deepseek-v4.1-flash", Total: 0.002, EstimateUSD: 0.001, CashMax: 0.0015},
			}}
		if !req.NoLead {
			resp.LeadModel = "anthropic/claude-sonnet-5.5"
		}
		return http.StatusOK, resp
	})
	r, err := e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if err != nil || srv.Last().NoLead || r.Pick.Route != RouteClaude {
		t.Fatalf("with a lead: %v no_lead %v pick %+v", err, srv.Last().NoLead, r.Pick)
	}
	writeEngine(t, e, "lead = \"none\"\n")
	r, err = e.Recommend(Request{UseCase: "qa-factual", Stakes: 2, Scope: Scope{Cwd: proj}})
	if err != nil || !srv.Last().NoLead || srv.Last().LeadModel != "" {
		t.Fatalf("no lead on the wire: %v %+v", err, srv.Last())
	}
	if r.Pick == nil || r.Pick.Model != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("a Claude option from the server is dropped: %+v", r.Options)
	}
	noClaude(t, "live", r)
	if _, ok := Tuning(e.Dirs.EngineFile())["lead"]; ok {
		t.Error("lead is sent as tuning")
	}
}

// A table signed before 1.0.2 has no no-lead routes: the delegated options of the Claude-lead routes
// are all there is.
func TestNoLeadWithAnOlderTable(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nper_task_usd = 1\n")
	pub, priv := tabletest.Key()
	e.Table = table.Open(e.Dirs.Cache, table.WithKeys(pub), table.WithBuiltIn(tabletest.Seal(priv, tabletest.PayloadBeforeNoLead())))
	writeEngine(t, e, "lead = \"none\"\n")
	r, err := e.Recommend(Request{UseCase: "log-digest", Scope: Scope{Cwd: proj}})
	if err != nil || r.Pick == nil || r.Pick.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("log-digest: %v %+v", err, r)
	}
	noClaude(t, "log-digest", r)
	r, _ = e.Recommend(Request{UseCase: "invoice-receipt-extract", Stakes: 3, Scope: Scope{Cwd: proj}})
	if r.Pick != nil || !strings.Contains(strings.Join(r.Notes, ";"), "engine refresh") {
		t.Errorf("invoice from an old table: %+v %v", r.Pick, r.Notes)
	}
}
