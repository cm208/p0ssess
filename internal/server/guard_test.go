package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/history"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/hook"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/mcpserver"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/relay"
	"p0ssess/internal/usage"
)

const testModel = "test/cheap"

// fakeRouter is an OpenRouter stand-in for the direct path.
type fakeRouter struct {
	mu     sync.Mutex
	chat   func() (int, string)
	gen    func() (int, string)
	bodies []string
}

func withFake(t *testing.T, f *fakeRouter) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		h := f.chat
		if strings.HasSuffix(r.URL.Path, "/generation") {
			h = f.gen
		}
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		code, body := h()
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	old := newClient
	newClient = func(key string) *direct.Client {
		c := direct.New(key)
		c.BaseURL = srv.URL
		c.Catalog = direct.NewCatalog(srv.URL, srv.Client())
		c.Catalog.NoLive = true
		c.Catalog.Add(direct.ModelInfo{ID: testModel, Input: []string{"text"}, Output: []string{"text"},
			Routes: []string{"/api/v1/chat/completions"}, InPerM: 1, OutPerM: 1})
		c.StateDir = t.TempDir()
		c.GenerationWait = 0
		return c
	}
	t.Cleanup(func() { newClient = old })
}

func directEnv(t *testing.T, budgetTOML string) (config.Config, *local.Engine, string) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", filepath.Join(home, "key"))
	os.WriteFile(filepath.Join(home, "key"), []byte("test-key\n"), 0o600)
	os.WriteFile(filepath.Join(home, "budget.toml"), []byte(budgetTOML), 0o600)
	proj := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(proj, 0o755)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	if out, err := exec.Command("git", "-C", proj, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", proj)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	tabletest.Down(t)
	eng, err := local.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	eng.Table = tabletest.Store(eng.Dirs.Cache)
	eng.Table.Checked(time.Now())
	return cfg, eng, proj
}

func ledger(t *testing.T, eng *local.Engine) (*history.Ledger, history.Spend) {
	es, err := history.Read(eng.Dirs.HistoryFile())
	if err != nil {
		t.Fatal(err)
	}
	l := history.NewLedger(es)
	return l, l.Spend(time.Now())
}

const okChat = `{"id":"gen-1","model":"test/cheap","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"cost":0.00002}}`
const noCostChat = `{"id":"gen-9","model":"test/cheap","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

func TestRunDirectMeasuredEstimatedAndReleased(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	req := direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.05, MaxGiven: true, MaxTokens: 10}

	if _, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil || id == "" {
		t.Fatalf("measured run: %q %v", id, err)
	}
	l, sp := ledger(t, eng)
	if sp.Month != 0.00002 || sp.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("measured: %+v open %d", sp, len(l.Open()))
	}

	// No cost in the answer and the generation lookup fails: the reserved maximum is recorded.
	f.chat = func() (int, string) { return 200, noCostChat }
	res, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if !strings.Contains(direct.Format(res), "cost not measured") {
		t.Errorf("no warning about the estimate:\n%s", direct.Format(res))
	}
	_, sp = ledger(t, eng)
	if sp.Estimated != 0.05 || sp.Month < 0.05 {
		t.Errorf("unknown cost must count at the reserved maximum: %+v", sp)
	}
	// Later the generation lookup works and the estimate is replaced.
	f.gen = func() (int, string) { return 200, `{"data":{"total_cost":0.00003}}` }
	Reconcile(context.Background(), cfg, eng)
	_, sp = ledger(t, eng)
	if sp.Estimated != 0 || sp.Month != 0.00005 {
		t.Errorf("after reconcile: %+v", sp)
	}

	// An error answer is not billed: the reservation is released and nothing is spent.
	f.chat = func() (int, string) { return 400, `{"error":{"code":400,"message":"bad"}}` }
	if _, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err == nil || id != "" {
		t.Errorf("error answer: id %q err %v", id, err)
	}
	l, sp2 := ledger(t, eng)
	if sp2.Month != sp.Month || sp2.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("an unbilled call changed spend: %+v -> %+v", sp, sp2)
	}
}

func TestRunDirectEnforcesAtTheTool(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0\ndaily_usd = -1\n[privacy]\nmode = \"zdr\"\n")
	req := direct.Request{Model: testModel, Prompt: "hi", MaxTokens: 10}
	_, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
	var r *local.Refusal
	if !errors.As(err, &r) || !r.NeedsApproval || !strings.Contains(err.Error(), `"max_count":1`) {
		t.Errorf("an estimate above the per-task limit: %v", err)
	}
	if _, err := eng.SetSetting("per_task_usd", "0.02"); err != nil {
		t.Fatal(err)
	}
	// The estimate decides whether to ask; a ceiling the caller set above the limit is reserved in full.
	req.MaxCostUSD, req.MaxGiven = 0.05, true
	if _, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil {
		t.Fatal(err)
	}
	l, _ := ledger(t, eng)
	var reserved float64
	for _, e := range l.Entries {
		if e.Type == history.TypeReserve {
			reserved = e.CashUSD
		}
	}
	if reserved != 0.05 {
		t.Errorf("reserved %v, want the caller's 0.05", reserved)
	}
	if !strings.Contains(f.bodies[len(f.bodies)-1], `"zdr":true`) {
		t.Errorf("zdr mode did not force zero-retention routing: %s", f.bodies[len(f.bodies)-1])
	}
	_, _, err = RunDirect(context.Background(), cfg, eng, direct.Request{Model: testModel, Prompt: "hi", MaxTokens: 10}, DirectJob{Stakes: 4})
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "stakes 4") {
		t.Errorf("stakes 4: %v", err)
	}
	// A prompt-only call from a private repository is refused before anything is sent.
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	n := len(f.bodies)
	_, _, err = RunDirect(context.Background(), cfg, eng, direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.01, MaxTokens: 10}, DirectJob{})
	if err == nil || !strings.Contains(err.Error(), ".p0ssess-private") || len(f.bodies) != n {
		t.Errorf("private repo: %v (sent %d)", err, len(f.bodies)-n)
	}
}

type recordingObserver struct {
	relay.Observer
	released []string
}

func (o *recordingObserver) Release(g relay.Grant) {
	o.released = append(o.released, g.ReserveID)
	o.Observer.Release(g)
}

// TestRelayRunEnforcesLimits checks the agent path: the relay refuses without a guard, the guard
// refuses over the limits before any jail work, and a run that fails before its prompt is sent gives
// its reservation back.
func TestRelayRunEnforcesLimits(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.0001\ndaily_usd = -1\n")
	os.Remove(filepath.Join(os.Getenv("P0SSESS_HOME"), "key")) // the jail cannot start without a key
	cfg, _ := config.Load()
	re := relay.NewEngine(cfg, "test", io.Discard)
	req := relay.RunRequest{Task: "x", Model: "deepseek/deepseek-v4.1-flash", Cwd: proj}
	if _, err := re.Run(context.Background(), req, nil); !errors.Is(err, relay.ErrNoObserver) {
		t.Errorf("unguarded relay ran: %v", err)
	}
	obs := &recordingObserver{Observer: NewObserver(eng)}
	re.SetObserver(obs)
	_, err := re.Run(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "needs approval") {
		t.Errorf("an agent run estimated above a $0.0001 per-task limit: %v", err)
	}
	if _, err := eng.SetSetting("per_task_usd", "0.5"); err != nil {
		t.Fatal(err)
	}
	_, err = re.Run(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "jail") {
		t.Fatalf("want a jail error after authorizing, got %v", err)
	}
	l, sp := ledger(t, eng)
	if len(obs.released) != 1 || len(l.Open()) != 0 || sp.Reserved != 0 {
		t.Errorf("reservation not released: released %v, open %d, %+v", obs.released, len(l.Open()), sp)
	}
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	if _, err := re.Run(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "p0ssess-private") {
		t.Errorf("private repo: %v", err)
	}
}

// TestRelayUnknownPriceCountsAtCap covers M6 on the agent path: a model with no known price needs an
// explicit max_cost_usd, and a run OpenCode reports at $0 despite billed tokens is counted at its cap.
func TestRelayUnknownPriceCountsAtCap(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	obs := NewObserver(eng)
	auth := relay.RunAuth{Model: "newco/unpriced", MaxCostUSD: 0.05, MaxRuntime: time.Minute, Cwd: proj}
	if _, err := obs.Authorize(auth); err == nil || !strings.Contains(err.Error(), "no price is known") {
		t.Fatalf("default cap on an unpriced model: %v", err)
	}
	auth.ExplicitMax, auth.MaxCostUSD = true, 0.03
	g, err := obs.Authorize(auth)
	if err != nil || !g.PriceUnknown {
		t.Fatalf("explicit cap: %+v %v", g, err)
	}
	res := &relay.Result{Status: "done", Tokens: &opencode.Tokens{Input: 1200, Output: 300}}
	if err := obs.RunFinished(relay.RunInfo{SessionID: "ses-1", Turn: 1, Model: auth.Model, Grant: g}, res); err != nil {
		t.Fatal(err)
	}
	l, sp := ledger(t, eng)
	if !res.CostEstimated || sp.Estimated != 0.03 || sp.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("a $0 report for billed tokens must count at the cap: estimated %v, %+v", res.CostEstimated, sp)
	}
}

// TestRelayCostIsNeverZeroForBilledTokens is the gemini-3.8-flash case: p0ssess knows the model's price,
// OpenCode does not and reports $0. The relay's measured (or token-priced) cost settles the ledger, and
// counts against the budget.
func TestRelayCostIsNeverZeroForBilledTokens(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.5\ndaily_usd = -1\n")
	obs := NewObserver(eng)
	auth := relay.RunAuth{Model: "google/gemini-3.8-flash", MaxCostUSD: 0.2, MaxRuntime: time.Minute, Cwd: proj}
	toks := &opencode.Tokens{Input: 10000, Output: 2000}

	// The table's price: 0.3 in, 2.5 out per million.
	p, ok := Pricer(eng)("google/gemini-3.8-flash", *toks)
	if !ok || p < 0.00799 || p > 0.00801 {
		t.Fatalf("table price %v %v", p, ok)
	}
	if _, ok := Pricer(eng)("newco/unpriced", *toks); ok {
		t.Fatal("an unknown model has no price")
	}

	g, err := obs.Authorize(auth)
	if err != nil || g.PriceUnknown {
		t.Fatalf("%+v %v", g, err)
	}
	res := &relay.Result{Status: "done", CostUSD: 0.0051, CostBasis: relay.CostMeasured, Tokens: toks}
	if err := obs.RunFinished(relay.RunInfo{SessionID: "ses-1", Turn: 1, Model: auth.Model, Grant: g}, res); err != nil {
		t.Fatal(err)
	}
	_, sp := ledger(t, eng)
	if sp.Month < 0.0050 || sp.Month > 0.0052 || sp.Estimated != 0 || res.CostEstimated {
		t.Fatalf("measured cost must settle as measured: %+v", sp)
	}

	g, _ = obs.Authorize(auth)
	res = &relay.Result{Status: "done", CostUSD: 0.008, CostBasis: relay.CostTokens, Tokens: toks}
	obs.RunFinished(relay.RunInfo{SessionID: "ses-1", Turn: 2, Model: auth.Model, Grant: g}, res)
	_, sp = ledger(t, eng)
	if sp.Month < 0.0130 || sp.Month > 0.0132 || sp.Estimated < 0.0079 || sp.Estimated > 0.0081 {
		t.Fatalf("a token-priced run counts at its price, marked as not measured: %+v", sp)
	}

	g, _ = obs.Authorize(auth)
	res = &relay.Result{Status: "done", CostUSD: 0, CostBasis: relay.CostCap, CostEstimated: true, Tokens: toks}
	obs.RunFinished(relay.RunInfo{SessionID: "ses-1", Turn: 3, Model: auth.Model, Grant: g}, res)
	_, sp = ledger(t, eng)
	if want := 0.0131 + g.CapUSD; g.CapUSD <= 0 || sp.Month < want-0.0001 || sp.Month > want+0.0001 {
		t.Fatalf("an unmeasured, unpriced run counts at its $%v cap: %+v", g.CapUSD, sp)
	}
}

// TestShellInternetIsTheUsersChoice: only the user's own budget.toml turns shell internet on, per
// project; a repository cannot turn it on for itself.
func TestShellInternetIsTheUsersChoice(t *testing.T) {
	home := t.TempDir()
	proj, other := filepath.Join(home, "proj"), filepath.Join(home, "other")
	os.MkdirAll(filepath.Join(proj, "sub"), 0o755)
	os.MkdirAll(filepath.Join(other, ".p0ssess"), 0o755)
	// A repository's own files may say anything; none of them is read for this.
	os.WriteFile(filepath.Join(other, ".p0ssess", "rules.toml"), []byte("[sandbox]\nshell_internet = [\""+other+"\"]\n"), 0o644)
	os.WriteFile(filepath.Join(other, ".p0ssess", "budget.toml"), []byte("[sandbox]\nshell_internet = [\""+other+"\"]\n"), 0o644)
	_, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n[sandbox]\nshell_internet = [\""+proj+"\"]\n")
	obs := NewObserver(eng)
	for repo, want := range map[string]bool{proj: true, filepath.Join(proj, "sub"): true, other: false, home: false} {
		g, err := obs.Authorize(relay.RunAuth{Model: "deepseek/deepseek-v4.1-flash", MaxCostUSD: 0.01, MaxRuntime: time.Minute, Repo: repo, Cwd: repo})
		if err != nil {
			t.Fatal(err)
		}
		obs.Release(g)
		if g.ShellInternet != want {
			t.Errorf("%s: shell internet %v, want %v", repo, g.ShellInternet, want)
		}
	}
}

// TestAgentCeilingFitsTheRoomLeft: an agent run is asked about from its estimate, its default ceiling
// (max_run_cost_usd) is kept when it fits, and shrinks to the daily room or a standing approval's
// per-run limit when the estimate still fits; a caller's own maximum is never shrunk.
func TestAgentCeilingFitsTheRoomLeft(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 10\nper_task_usd = 0.10\ndaily_usd = -1\n")
	obs := NewObserver(eng)
	auth := relay.RunAuth{Model: "deepseek/deepseek-v4.1-flash", MaxCostUSD: 0.5, MaxRuntime: time.Minute, Cwd: proj, Mode: "edit"}
	g, err := obs.Authorize(auth)
	if err != nil || g.CapUSD != 0.5 {
		t.Fatalf("under the limit, the default ceiling holds: %+v %v", g, err)
	}
	obs.Release(g)
	if _, err := eng.SetSetting("daily_usd", "0.3"); err != nil {
		t.Fatal(err)
	}
	if g, err = obs.Authorize(auth); err != nil || g.CapUSD != 0.3 {
		t.Fatalf("the default ceiling fits the daily room: %+v %v", g, err)
	}
	obs.Release(g)
	auth.ExplicitMax = true
	if _, err = obs.Authorize(auth); err == nil || !strings.Contains(err.Error(), "daily soft limit") {
		t.Errorf("a caller's own $0.50 is not shrunk: %v", err)
	}
	if _, err := eng.SetSetting("ask.code_edits", "true"); err != nil {
		t.Fatal(err)
	}
	auth.ExplicitMax, auth.MaxCostUSD = false, 0.5
	if _, err = obs.Authorize(auth); err == nil || !strings.Contains(err.Error(), "ask.code_edits") {
		t.Errorf("ask.code_edits asks before an edit run: %v", err)
	}
}

// TestDirectPicksTheModel: with no model the direct tool picks one for the use case, given or read
// from the files and output, and records the offer.
func TestDirectPicksTheModel(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.10\ndaily_usd = -1\n")
	res, _, err := RunDirect(context.Background(), cfg, eng, direct.Request{Prompt: "what happens", DryRun: true}, DirectJob{UseCase: "video-understand"})
	if err != nil || res == nil || res.Model == "" {
		t.Fatalf("picked: %+v %v", res, err)
	}
	if m := eng.Table.Table().Model(res.Model); m == nil || !m.HasInput("video") {
		t.Errorf("picked %s, which does not take video", res.Model)
	}
	l, _ := ledger(t, eng)
	if len(l.Entries) == 0 || l.Entries[0].Type != history.TypeOffer || l.Entries[0].UseCase != "video-understand" {
		t.Errorf("the pick is recorded as an offer: %+v", l.Entries)
	}
	if _, _, err := RunDirect(context.Background(), cfg, eng, direct.Request{Prompt: "hi", DryRun: true}, DirectJob{}); err == nil || !strings.Contains(err.Error(), "use_case") {
		t.Errorf("no model, no use case, no files: %v", err)
	}
	for _, tc := range []struct {
		out   direct.Output
		kinds map[direct.Kind]int
		want  string
	}{
		{direct.OutImage, nil, "image-generate"},
		{direct.OutImage, map[direct.Kind]int{direct.KindImage: 1}, "image-edit"},
		{"", map[direct.Kind]int{direct.KindVideo: 1, direct.KindAudio: 1}, "video-understand"},
		{"", map[direct.Kind]int{direct.KindAudio: 1}, "audio-transcribe"},
		{"", map[direct.Kind]int{direct.KindPDF: 1}, ""},
	} {
		if got := inferUseCase(tc.out, tc.kinds); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.out, tc.kinds, got, tc.want)
		}
	}
}

// TestApprovalFromTheMessageCoversTheCall replays a needs-approval answer end to end, as it happened
// live: Claude Code was started in a directory that is not a repository and the session has moved to a
// repository below it, so the server's directory is not the session's. The approvals_add arguments
// are copied from the message verbatim, and the retried call must run under that approval. The
// session comes from Claude Code's environment, or, without it, from the status line and hook records.
func TestApprovalFromTheMessageCoversTheCall(t *testing.T) {
	const model, sid = "openai/gpt-6.1-sol", "ed59eeff-0000-4000-8000-000000000001"
	for name, env := range map[string]string{"environment": sid, "records": ""} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
			withFake(t, f)
			priced := newClient
			newClient = func(key string) *direct.Client {
				c := priced(key)
				c.Catalog.Add(direct.ModelInfo{ID: model, Input: []string{"text"}, Output: []string{"text"},
					Routes: []string{"/api/v1/chat/completions"}, InPerM: 2, OutPerM: 10})
				return c
			}
			cfg, eng, _ := directEnv(t, "monthly_usd = 10\nper_task_usd = 0.10\ndaily_usd = -1\n")
			launch := filepath.Join(t.TempDir(), "home")
			sub := filepath.Join(launch, "repo")
			os.MkdirAll(filepath.Join(sub, ".git"), 0o755)
			t.Setenv("CLAUDE_PROJECT_DIR", launch)
			t.Setenv("CLAUDE_CODE_SESSION_ID", env)
			var in usage.StatusInput
			in.SessionID, in.Workspace.CurrentDir, in.Workspace.ProjectDir = sid, sub, launch
			usage.Update(eng.Dirs.UsageFile(), func(s *usage.State) { s.Record(in, time.Now()) })
			hook.Run([]byte(`{"session_id":"`+sid+`","cwd":"`+sub+`","hook_event_name":"PreToolUse"}`), eng.Dirs, time.Now())

			req := direct.Request{Model: model, Prompt: "hi", MaxTokens: 21000, MaxCostUSD: 0.469612, MaxGiven: true}
			_, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
			m := regexp.MustCompile(`call approvals_add with (\{.*?\}), then`).FindStringSubmatch(fmt.Sprint(err))
			if m == nil || !strings.Contains(m[1], `"max_per_run_usd":0.4697`) || !strings.Contains(m[1], `"session_id":"`+sid+`"`) {
				t.Fatalf("want an approval ask for this session covering $0.469612, got %v", err)
			}
			out, err := (&mcpserver.Server{E: eng}).Call("approvals_add", json.RawMessage(m[1]))
			if err != nil {
				t.Fatalf("approvals_add %s: %v", m[1], err)
			}
			saved := out.(map[string]any)["rule"].(local.RuleView)
			if !strings.HasSuffix(saved.Text, ", 1 run (session, until "+saved.Expires.Local().Format("Jan 2 15:04")+")") {
				t.Errorf("rule text: %q", saved.Text)
			}
			if _, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil {
				t.Fatalf("retry under the approval: %v", err)
			}
			l, _ := ledger(t, eng)
			var tagged string
			for _, e := range l.Entries {
				if e.Type == history.TypeReserve {
					tagged = e.ApprovalID
				}
			}
			if tagged != saved.ID {
				t.Errorf("reservation approval %q, want %q", tagged, saved.ID)
			}
			if _, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err == nil || !strings.Contains(err.Error(), "needs approval") {
				t.Errorf("a one-run approval is used up: %v", err)
			}
		})
	}
}

const timeout200 = `{"error":{"message":"A Timeout Occurred","code":504,"metadata":{"error_type":"timeout"}}}`

// TestDirectFallsBackOnProviderFailure: when the model p0ssess picked times out at its provider, the
// next-ranked option answers once, under the same limits, and only billed calls are recorded (MEDIA.md,
// run B on 0.5.2: no fallback, and the unbilled failure was recorded at its reserved maximum).
func TestDirectFallsBackOnProviderFailure(t *testing.T) {
	var models []string
	f := &fakeRouter{}
	f.chat = func() (int, string) {
		var b struct {
			Model string `json:"model"`
		}
		json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &b)
		models = append(models, b.Model)
		if len(models) == 1 {
			return 200, timeout200
		}
		return 200, strings.Replace(okChat, `"model":"test/cheap"`, `"model":"`+b.Model+`"`, 1)
	}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.50\ndaily_usd = -1\n")
	req := direct.Request{Prompt: "summarize the plot", MaxTokens: 200}
	res, runID, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{UseCase: "video-understand", Stakes: 1})
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if len(models) != 2 || models[0] == models[1] || !strings.HasSuffix(res.Model, history.Short(models[1])) {
		t.Fatalf("models %v, answered by %s", models, res.Model)
	}
	note := strings.Join(res.Warnings, "|")
	if !strings.Contains(note, history.Short(models[0])+"'s provider failed (openrouter: provider error 504: A Timeout Occurred); "+history.Short(models[1])+" answered") {
		t.Errorf("warnings: %v", res.Warnings)
	}
	l, sp := ledger(t, eng)
	var runs []history.Entry
	for _, e := range l.Entries {
		if e.Type == history.TypeRun {
			runs = append(runs, e)
		}
	}
	if len(runs) != 1 || runs[0].Model != models[1] || runs[0].Estimated || runs[0].RunID != runID || sp.Month != 0.00002 || sp.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("ledger: runs %+v, spend %+v, open %d", runs, sp, len(l.Open()))
	}

	// A model the caller named is not swapped, and the unbilled failure is not recorded.
	models = nil
	_, _, err = RunDirect(context.Background(), cfg, eng, direct.Request{Model: testModel, Prompt: "hi", MaxTokens: 10}, DirectJob{})
	if !direct.ProviderFailed(err) || len(models) != 1 {
		t.Errorf("named model: %v, %d calls", err, len(models))
	}
	if _, sp2 := ledger(t, eng); sp2.Month != sp.Month || sp2.Estimated != 0 {
		t.Errorf("an unbilled failure changed spend: %+v -> %+v", sp, sp2)
	}

	// The fallback stays within the caller's ceiling: it reserves at most what is left of it.
	models = nil
	capped := req
	capped.MaxCostUSD, capped.MaxGiven = 0.002, true
	res, _, err = RunDirect(context.Background(), cfg, eng, capped, DirectJob{UseCase: "video-understand", Stakes: 1})
	l, _ = ledger(t, eng)
	var last history.Entry
	for _, e := range l.Entries {
		if e.Type == history.TypeReserve {
			last = e
		}
	}
	if err != nil || len(models) != 2 || last.Model != models[1] || last.CashUSD > 0.002 {
		t.Errorf("under a ceiling: %v, models %v, reserved %+v", err, models, last)
	}
	// With no other option that runs without asking, nothing else is tried, and the note says so.
	if _, err := eng.SetSetting("per_task_usd", "0.0001"); err != nil {
		t.Fatal(err)
	}
	models = nil
	res, _, err = RunDirect(context.Background(), cfg, eng, req, DirectJob{UseCase: "video-understand", Stakes: 1})
	if !direct.ProviderFailed(err) || len(models) != 1 || !strings.Contains(strings.Join(res.Warnings, "|"), "none was tried") {
		t.Errorf("no alternative: %v, models %v, warnings %v", err, models, res.Warnings)
	}
}

// TestDirectPartialIsAnAnswer: a split video whose second part fails comes back as the first part's
// answer with a note, not as an error, and is recorded as partial at its measured cost.
func TestDirectPartialIsAnAnswer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	v := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=red:s=160x90:r=10:d=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", v).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	n := 0
	f := &fakeRouter{chat: func() (int, string) {
		if n++; n == 1 {
			return 200, okChat
		}
		return 200, timeout200
	}}
	withFake(t, f)
	plain := newClient
	newClient = func(key string) *direct.Client {
		c := plain(key)
		c.Limits = direct.Limits{VideoSeconds: 2, SegmentSeconds: 2, DownscaleHeight: 60}
		c.Catalog.Add(direct.ModelInfo{ID: "test/video", Input: []string{"text", "video", "audio"}, Output: []string{"text"},
			Routes: []string{"/api/v1/chat/completions"}, InPerM: 1, OutPerM: 1})
		return c
	}
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.50\ndaily_usd = -1\n")
	args, _ := json.Marshal(map[string]any{"model": "test/video", "prompt": "describe", "files": []string{v}, "max_tokens": 50})
	out := (&directTool{cfg: cfg, eng: eng}).call(context.Background(), args, nil)
	text := out.Content[0].Text
	if out.IsError || !strings.Contains(text, "[segment 1 of 2, 0:00 to 0:02]\nhi") || !strings.Contains(text, "(not answered: openrouter: provider error 504") ||
		!strings.Contains(text, "note: ") || !strings.Contains(text, "partial answer: segment 2 of 2") || !strings.Contains(text, "video_prep segment, chosen automatically") {
		t.Fatalf("error %v:\n%s", out.IsError, text)
	}
	l, sp := ledger(t, eng)
	var run history.Entry
	for _, e := range l.Entries {
		if e.Type == history.TypeRun {
			run = e
		}
	}
	if run.Status != "partial" || run.Estimated || sp.Month != 0.00002 || sp.Reserved != 0 {
		t.Errorf("run %+v, spend %+v", run, sp)
	}
}

func recorded(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", "byok", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lastRun is the newest run entry in the ledger.
func lastRun(t *testing.T, eng *local.Engine) history.Entry {
	l, _ := ledger(t, eng)
	runs := l.Runs(1)
	if len(runs) != 1 {
		t.Fatalf("no run in the ledger")
	}
	return runs[0]
}

// TestRunDirectCostBasis: each way a direct call's cost is settled is recorded in the ledger, so a call on
// the user's own provider key (which OpenRouter reports at $0 or its fee) is not recorded as free.
func TestRunDirectCostBasis(t *testing.T) {
	const unpriced = "test/unpriced"
	for _, c := range []struct {
		name      string
		model     string
		chat, gen string
		cash      float64
		basis     string
		estimated bool
	}{
		{"normal paid call", testModel, "paid.json", "", 0.0004, "reported", false},
		{"byok with the upstream cost", testModel, "byok-upstream.json", "", 0.0031, "byok-upstream", false},
		{"byok, upstream only in the generation record", testModel, "byok-no-upstream.json", "generation-byok.json", 0.0042, "byok-upstream", false},
		// 1000 prompt + 200 completion tokens at the test model's $1 per million each.
		{"byok without the upstream cost: priced from tokens", testModel, "byok-no-upstream.json", "", 0.0012, "tokens", true},
		// No price is known: the reserved maximum (the caller's $0.05 ceiling).
		{"byok, price unknown: reserved maximum", unpriced, "byok-no-upstream.json", "", 0.05, "reserved", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			chat := recorded(t, c.chat)
			f := &fakeRouter{chat: func() (int, string) { return 200, chat }}
			if c.gen != "" {
				gen := recorded(t, c.gen)
				f.gen = func() (int, string) { return 200, gen }
			}
			withFake(t, f)
			prev := newClient
			newClient = func(key string) *direct.Client {
				cl := prev(key)
				cl.Catalog.Add(direct.ModelInfo{ID: unpriced, Input: []string{"text"}, Output: []string{"text"},
					Routes: []string{"/api/v1/chat/completions"}})
				return cl
			}
			t.Cleanup(func() { newClient = prev })
			cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
			req := direct.Request{Model: c.model, Prompt: "hi", MaxCostUSD: 0.05, MaxGiven: true, MaxTokens: 10}
			res, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
			if err != nil || id == "" {
				t.Fatalf("%q %v", id, err)
			}
			run := lastRun(t, eng)
			if run.CostBasis != c.basis || run.Estimated != c.estimated || run.CashUSD < c.cash-1e-9 || run.CashUSD > c.cash+1e-9 {
				t.Errorf("ledger run = basis %q estimated %v cash %v; want %q %v %v", run.CostBasis, run.Estimated, run.CashUSD, c.basis, c.estimated, c.cash)
			}
			if res.Usage.CostBasis != c.basis {
				t.Errorf("result basis %q, want %q", res.Usage.CostBasis, c.basis)
			}
			_, sp := ledger(t, eng)
			if sp.Month < c.cash-1e-9 {
				t.Errorf("month spend %v is below the call's cost %v", sp.Month, c.cash)
			}
		})
	}
}

// A token-priced BYOK call is replaced by the real figure once the generation record reports the
// provider's charge, and its basis follows.
func TestRunDirectBYOKPricedThenReconciled(t *testing.T) {
	chat := recorded(t, "byok-no-upstream.json")
	f := &fakeRouter{chat: func() (int, string) { return 200, chat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	req := direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.05, MaxGiven: true, MaxTokens: 10}
	if _, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil {
		t.Fatal(err)
	}
	gen := recorded(t, "generation-byok.json")
	f.gen = func() (int, string) { return 200, gen }
	Reconcile(context.Background(), cfg, eng)
	run := lastRun(t, eng)
	if run.CostBasis != "byok-upstream" || run.Estimated || run.CashUSD != 0.0042 {
		t.Errorf("after reconcile: %+v", run)
	}
	if _, sp := ledger(t, eng); sp.Estimated != 0 || sp.Month != 0.0042 {
		t.Errorf("spend after reconcile: %+v", sp)
	}
}
