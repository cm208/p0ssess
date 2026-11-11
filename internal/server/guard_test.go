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
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/hook"
	"p0ssess/internal/pme/mcpserver"
	"p0ssess/internal/pme/paths"
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

func directEnv(t *testing.T, budgetTOML string) (config.Config, *engine.Engine, string) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", filepath.Join(home, "key"))
	os.WriteFile(filepath.Join(home, "key"), []byte("test-key\n"), 0o600)
	os.WriteFile(filepath.Join(home, "budget.toml"), []byte(budgetTOML), 0o600)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	t.Setenv("CLAUDE_PROJECT_DIR", proj)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	return cfg, eng, proj
}

func ledger(t *testing.T, eng *engine.Engine) (*history.Ledger, history.Spend) {
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
	var r *engine.Refusal
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
	if m := eng.Reg.Get(res.Model); m == nil || !m.HasInput("video") {
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
			saved := out.(map[string]any)["rule"].(engine.RuleView)
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
