// Package relay runs delegated tasks on the jailed OpenCode server and exposes them as MCP tools.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

const rejectFeedback = "Not allowed in this delegated read-only task, and nobody is available to approve it. " +
	"Continue without it, and say in your final answer what you could not do."

type Engine struct {
	cfg     config.Config
	version string
	log     io.Writer

	mu      sync.Mutex
	watches map[string]*watch
	// jails are the sessions whose pods this process owns and removes.
	jails map[string]*sessionJail
	obs   Observer
	// pricer prices runs whose cost was not reported (cost.go).
	pricer Pricer
	// recMu serializes recording a finished run, which both a waiting caller and the watch may do.
	recMu sync.Mutex
	// Unguarded lets tests run without an observer; nothing else sets it. TestShellInternet is the
	// shell internet such a run gets.
	Unguarded         bool
	TestShellInternet bool
}

func NewEngine(cfg config.Config, version string, log io.Writer) *Engine {
	return &Engine{cfg: cfg, version: version, log: log, watches: map[string]*watch{}, jails: map[string]*sessionJail{}}
}

// sessionJail is a session's pod while this process owns it. idle removes it once the session has had no
// run for session_idle_s.
type sessionJail struct {
	j    *jail.Jail
	rec  *SessionRecord
	idle *time.Timer
}

type RunRequest struct {
	Task        string   `json:"task"`
	Model       string   `json:"model"`
	Mode        string   `json:"mode"`
	Cwd         string   `json:"cwd"`
	Attachments []string `json:"attachments"`
	Session     string   `json:"session"`
	Kind        string   `json:"kind"`
	WaitS       int      `json:"wait_s"`
	UseCase     string   `json:"use_case"`
	Stakes      int      `json:"stakes"`
	// MaxCostUSD caps this prompt's cost; the relay interrupts the run past it. 0 takes max_run_cost_usd
	// from config.json, which is also the ceiling.
	MaxCostUSD float64 `json:"max_cost_usd"`
}

// ParseModel accepts "openrouter/vendor/model", "vendor/model" or "vendor/model#variant".
func ParseModel(s string) (opencode.ModelRef, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "openrouter/")
	if s == "" || !strings.Contains(s, "/") || strings.ContainsAny(s, " \t\n") {
		return opencode.ModelRef{}, fmt.Errorf("model must be an OpenRouter id like deepseek/deepseek-v4.1-flash, got %q", s)
	}
	m := opencode.ModelRef{ProviderID: "openrouter", ID: s}
	if id, variant, ok := strings.Cut(s, "#"); ok {
		m.ID, m.Variant = id, variant
	}
	return m, nil
}

// defaultCwd is the directory a run without cwd works in: the Claude Code session's own working directory
// as its status line and hooks recorded it, else the project directory, else this process's directory.
// newRecord still refuses any of them that is not a repository.
func defaultCwd() string {
	if sid := os.Getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
		if st, err := usage.Load(paths.Default().UsageFile()); err == nil {
			for _, se := range st.Sessions {
				if se.ID == sid && se.Cwd != "" {
					return se.Cwd
				}
			}
		}
	}
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

// Run starts a new session or continues one, then blocks until the run ends, needs a decision, or
// waitFor passes.
func (e *Engine) Run(ctx context.Context, req RunRequest, progress func(float64, string)) (*Result, error) {
	if strings.TrimSpace(req.Task) == "" {
		return nil, errors.New("task is empty")
	}
	if req.Mode == "" {
		req.Mode = "read"
	}
	if req.Mode != "read" && req.Mode != "edit" && req.Mode != "unsafe-allow-all" {
		return nil, fmt.Errorf("mode must be read or edit, got %q", req.Mode)
	}
	if req.Mode == "unsafe-allow-all" && os.Getenv("P0SSESS_ALLOW_UNSAFE_TEST_MODE") != "1" {
		return nil, errors.New("unsafe-allow-all is a test-only mode")
	}
	if req.Model != "" {
		m, err := e.ResolveModelID(req.Model)
		if err != nil {
			return nil, err
		}
		req.Model = m
	}
	var rec *SessionRecord
	var err error
	if req.Session != "" {
		if rec, err = loadRecord(req.Session); err != nil {
			return nil, fmt.Errorf("unknown session %s (only sessions started by this relay can be continued)", req.Session)
		}
		if err := e.continuable(rec); err != nil {
			return nil, err
		}
		if w := e.watch(rec.SessionID); w != nil && !w.finished() {
			return nil, fmt.Errorf("session %s is still running; call wait or abort first", rec.SessionID)
		}
	} else {
		cwd := req.Cwd
		if cwd == "" {
			cwd = defaultCwd()
		}
		if rec, err = e.newRecord(cwd, req); err != nil {
			return nil, err
		}
	}
	files, err := attachments(req.Attachments)
	if err != nil {
		return nil, err
	}
	if req.Kind != "" {
		rec.Kind = req.Kind
	}
	if req.UseCase != "" {
		rec.UseCase = req.UseCase
	}
	grant, err := e.authorize(rec, req)
	if err != nil {
		return nil, err
	}
	sent := false
	defer func() {
		if !sent && e.obs != nil {
			e.obs.Release(grant)
		}
	}()
	var j *jail.Jail
	var c *opencode.Client
	if req.Session == "" {
		if j, c, err = e.startSession(ctx, rec, req, grant); err != nil {
			return nil, err
		}
	} else {
		if j, c, err = e.jailFor(ctx, rec); err != nil {
			return nil, err
		}
		// Privacy and shell internet are fixed when a session's jail starts; a session may not continue
		// under looser settings than the user now has.
		if grant.ZDR && !j.ZDR {
			return nil, errors.New("zero-retention mode was turned on after this session started; start a new run")
		}
		if j.ShellInternet && !grant.ShellInternet {
			return nil, errors.New("shell internet was turned off for this project after this session started; start a new run")
		}
		if req.Model != "" {
			m, err := ParseModel(req.Model)
			if err != nil {
				return nil, err
			}
			if m.String() != "openrouter/"+rec.Model {
				if err := c.SwitchModel(ctx, rec.SessionID, m); err != nil {
					return nil, err
				}
				rec.Model = strings.TrimPrefix(m.String(), "openrouter/")
			}
		}
	}
	e.busy(rec.SessionID)
	maxRun := time.Duration(e.cfg.MaxRuntimeSec) * time.Second
	if maxRun <= 0 {
		maxRun = 6 * time.Hour
	}
	// The key proxy stops at the run's maximum runtime even if nothing watches the run any more.
	end := time.Now().Add(maxRun + time.Minute)
	if err := j.Keep(end.Add(e.idle()), end); err != nil {
		return nil, fmt.Errorf("jail: %w", err)
	}
	rec.MeterRun = newMeterRun()
	if err := j.StartRun(rec.MeterRun, grant.CapUSD, rec.Model); err != nil {
		return nil, err
	}
	rec.Grant = grant
	w, err := e.prompt(ctx, c, rec, req.Task, orient(rec, j, req.Task), files, &sent)
	if err != nil {
		e.idleAfter(rec.SessionID)
		var ae *opencode.APIError
		if errors.As(err, &ae) && ae.Status >= 500 {
			if tail := j.LogTail(ctx); tail != "" {
				err = fmt.Errorf("%w; server log: %s", err, tail)
			}
		}
		return nil, err
	}
	res, err := e.block(ctx, w, waitDuration(req.WaitS), progress)
	if res != nil && grant.Note != "" {
		res.Note = strings.TrimSpace(res.Note + " " + grant.Note)
	}
	if res != nil && req.Session == "" && j.StartupS > 0 {
		res.JailStartS = j.StartupS
	}
	if res != nil && req.Session == "" && j.Cloud {
		res.Note = strings.TrimSpace(res.Note + " " + cloudNote(j))
	}
	return res, err
}

// continuable reports why a recorded session cannot take another run.
func (e *Engine) continuable(rec *SessionRecord) error {
	if err := jail.CheckMountable(rec.Repo, e.cfg.OpenRouterKeyFile); err != nil {
		return fmt.Errorf("session %s cannot continue: %w", rec.SessionID, err)
	}
	if rec.Jail == "" {
		return fmt.Errorf("session %s was started by p0ssess 0.3 in a shared jail, which this version no longer runs; start a new run", rec.SessionID)
	}
	if rec.Closed {
		return fmt.Errorf("session %s has ended and its jail was removed; start a new run (its transcript is in %s)", rec.SessionID, RunDir(rec.SessionID))
	}
	return nil
}

func (e *Engine) idle() time.Duration { return time.Duration(e.cfg.SessionIdleSec) * time.Second }

// startSession creates the session's worktree (edit mode), its jail and the OpenCode session. Anything
// it made is removed again if a later step fails.
func (e *Engine) startSession(ctx context.Context, rec *SessionRecord, req RunRequest, grant Grant) (*jail.Jail, *opencode.Client, error) {
	if rec.Mode != "read" {
		if err := jail.PrepareGit(rec.Repo); err != nil {
			return nil, nil, err
		}
		wt, base, err := addWorktree(rec.Repo)
		if err != nil {
			return nil, nil, err
		}
		rel, _ := filepath.Rel(rec.Repo, rec.Cwd)
		rec.Worktree, rec.BaseRev = wt, base
		rec.Location = filepath.Join(wt, rel)
		if _, err := os.Stat(rec.Location); err != nil {
			rec.Location = wt
		}
	}
	cleanup := func() {
		if rec.Worktree != "" {
			removeWorktree(rec.Repo, rec.Worktree)
		}
	}
	rec.ShellInternet = grant.ShellInternet
	cfg := e.cfg
	cfg.ZDR = grant.ZDR
	j, err := jail.Start(ctx, jail.Spec{Repo: rec.Repo, Worktree: rec.Worktree, ShellInternet: grant.ShellInternet, ZDR: grant.ZDR}, cfg, e.log)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("jail: %w", err)
	}
	c, err := j.Client()
	if err == nil {
		err = e.createSession(ctx, c, rec, req, j.RemotePath(rec.Location))
	}
	if err != nil {
		j.Remove(context.Background())
		cleanup()
		return nil, nil, err
	}
	rec.Jail = j.Name
	j.Session = rec.SessionID
	if err := rec.save(); err != nil {
		j.Remove(context.Background())
		cleanup()
		return nil, nil, err
	}
	e.mu.Lock()
	e.jails[rec.SessionID] = &sessionJail{j: j, rec: rec}
	e.mu.Unlock()
	return j, c, nil
}

// jailFor returns a running session's jail and a client for it. A jail whose owner is gone, or that was
// detached, is adopted by this process.
func (e *Engine) jailFor(ctx context.Context, rec *SessionRecord) (*jail.Jail, *opencode.Client, error) {
	if err := e.continuable(rec); err != nil {
		return nil, nil, err
	}
	e.mu.Lock()
	sj := e.jails[rec.SessionID]
	e.mu.Unlock()
	var j *jail.Jail
	if sj != nil {
		j = sj.j
	} else {
		var err error
		if j, err = jail.Load(rec.Jail); err != nil {
			return nil, nil, fmt.Errorf("session %s has no jail any more (it idled out or was cleaned up); start a new run", rec.SessionID)
		}
	}
	if err := j.Verify(ctx); err != nil {
		return nil, nil, fmt.Errorf("session %s's jail is not usable (%v); start a new run", rec.SessionID, err)
	}
	if sj == nil && (j.Detached || !j.Owner.Alive()) {
		if err := j.Adopt(); err != nil {
			return nil, nil, err
		}
		e.mu.Lock()
		e.jails[rec.SessionID] = &sessionJail{j: j, rec: rec}
		e.mu.Unlock()
	}
	c, err := j.Client()
	return j, c, err
}

// busy stops a session's idle timer: a run is starting.
func (e *Engine) busy(sid string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sj := e.jails[sid]; sj != nil && sj.idle != nil {
		sj.idle.Stop()
		sj.idle = nil
	}
}

// idleAfter starts a session's idle timer once its run has ended. When it fires with no new run, the
// jail is removed. The key proxy refuses model calls from a minute after the run until the next prompt.
func (e *Engine) idleAfter(sid string) {
	e.mu.Lock()
	sj := e.jails[sid]
	if sj == nil {
		e.mu.Unlock()
		return
	}
	if sj.idle != nil {
		sj.idle.Stop()
	}
	now := time.Now()
	sj.idle = time.AfterFunc(e.idle(), func() { e.closeSession(sid) })
	e.mu.Unlock()
	if err := sj.j.Keep(now.Add(e.idle()), now.Add(time.Minute)); err != nil {
		fmt.Fprintf(e.log, "p0ssess: jail %s: %v\n", sj.j.Name, err)
	}
}

// closeSession records the last run if needed, removes the session's jail and marks the session ended.
// It does nothing while a run is active.
func (e *Engine) closeSession(sid string) {
	e.mu.Lock()
	sj := e.jails[sid]
	w := e.watches[sid]
	if sj == nil || (w != nil && !w.finished()) {
		e.mu.Unlock()
		return
	}
	delete(e.jails, sid)
	if w != nil {
		delete(e.watches, sid)
	}
	if sj.idle != nil {
		sj.idle.Stop()
	}
	e.mu.Unlock()
	if w != nil {
		e.recMu.Lock()
		recorded := w.rec.Recorded
		e.recMu.Unlock()
		if !recorded {
			e.result(w, "")
		}
		w.stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sj.j.Remove(ctx); err != nil {
		fmt.Fprintf(e.log, "p0ssess: removing jail %s: %v\n", sj.j.Name, err)
	}
	rec := sj.rec
	if w != nil {
		rec = w.rec
	}
	e.recMu.Lock()
	rec.Closed = true
	rec.save()
	e.recMu.Unlock()
}

// Release is for one-shot processes (the command line) when they are done with a session: a finished
// run's jail is removed unless keep is set; otherwise the jail is detached so a later `p0ssess wait` or
// `run --session` can pick it up, and a sweep removes it after its keep time.
func (e *Engine) Release(res *Result, keep bool) {
	if res == nil || res.SessionID == "" {
		return
	}
	sid := res.SessionID
	terminal := res.Status != "still_running" && res.Status != "needs_permission"
	if terminal && !keep {
		e.closeSession(sid)
		return
	}
	e.mu.Lock()
	sj := e.jails[sid]
	if sj != nil {
		if sj.idle != nil {
			sj.idle.Stop()
		}
		delete(e.jails, sid)
	}
	e.mu.Unlock()
	if sj == nil {
		return
	}
	until := time.Now().Add(e.idle())
	if !terminal {
		until = sj.j.KeepUntil
	}
	if err := sj.j.Detach(until); err != nil {
		fmt.Fprintf(e.log, "p0ssess: jail %s: %v\n", sj.j.Name, err)
	}
}

// Sweep removes jails whose owners died, at start-up.
func (e *Engine) Sweep(ctx context.Context) {
	removed, err := jail.Sweep(ctx, e.log)
	if err != nil {
		fmt.Fprintf(e.log, "p0ssess: jail sweep: %v\n", err)
	}
	if len(removed) > 0 {
		fmt.Fprintf(e.log, "p0ssess: removed stale jails: %s\n", strings.Join(removed, ", "))
	}
}

// authorize asks the observer to apply the user's limits to the prompt and reserve its cap. Without an
// observer the relay refuses, unless a test said otherwise.
func (e *Engine) authorize(rec *SessionRecord, req RunRequest) (Grant, error) {
	ceiling := e.cfg.MaxRunCostUSD
	capUSD := req.MaxCostUSD
	if capUSD < 0 || capUSD != capUSD {
		return Grant{}, errors.New("max_cost_usd must be a number, 0 or more")
	}
	explicit := capUSD > 0
	if capUSD == 0 || (ceiling > 0 && capUSD > ceiling) {
		capUSD = ceiling
	}
	if e.obs == nil {
		if e.Unguarded {
			return Grant{CapUSD: capUSD, ShellInternet: e.TestShellInternet}, nil
		}
		return Grant{}, ErrNoObserver
	}
	model := rec.Model
	if req.Model != "" {
		m, err := ParseModel(req.Model)
		if err != nil {
			return Grant{}, err
		}
		model = strings.TrimPrefix(m.String(), "openrouter/")
	}
	if i := strings.Index(model, "#"); i >= 0 {
		model = model[:i]
	}
	return e.obs.Authorize(RunAuth{Model: model, Mode: rec.Mode, Kind: rec.Kind, UseCase: rec.UseCase, Stakes: req.Stakes,
		MaxCostUSD: capUSD, ExplicitMax: explicit, MaxRuntime: time.Duration(e.cfg.MaxRuntimeSec) * time.Second, Repo: rec.Repo, Cwd: rec.Cwd})
}

func waitDuration(s int) time.Duration {
	if s <= 0 {
		return 25 * time.Minute
	}
	if s > 6*3600 {
		s = 6 * 3600
	}
	return time.Duration(s) * time.Second
}

func (e *Engine) newRecord(cwd string, req RunRequest) (*SessionRecord, error) {
	cwd, err := jail.Canonical(cwd)
	if err != nil {
		return nil, fmt.Errorf("cwd: %w", err)
	}
	repo, err := jail.RepoRoot(cwd)
	if err != nil {
		return nil, err
	}
	if err := jail.CheckMountable(repo, e.cfg.OpenRouterKeyFile); err != nil {
		return nil, err
	}
	if paths.Private(cwd) || paths.Private(repo) {
		return nil, errors.New("this repository has a .p0ssess-private file; delegation is blocked here")
	}
	m, err := ParseModel(req.Model)
	if err != nil {
		return nil, err
	}
	rec := &SessionRecord{Repo: repo, Cwd: cwd, Location: cwd, Mode: req.Mode, Kind: req.Kind,
		Model: strings.TrimPrefix(m.String(), "openrouter/"), Created: time.Now().UTC()}
	if req.Mode != "read" {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			return nil, errors.New("edit mode needs a git repository (it works in a separate worktree)")
		}
	}
	return rec, nil
}

func (e *Engine) createSession(ctx context.Context, c *opencode.Client, rec *SessionRecord, req RunRequest, dir string) error {
	m, _ := ParseModel(req.Model)
	s, err := c.CreateSession(ctx, opencode.CreateSession{
		Model:       &m,
		Location:    &opencode.Location{Directory: dir},
		Metadata:    map[string]any{"p0ssess": e.version, "mode": rec.Mode, "kind": rec.Kind},
		Permissions: Rules(rec.Mode, rec.ShellInternet),
	})
	if err != nil {
		return err
	}
	rec.SessionID = s.ID
	return nil
}

// lastSeq returns the newest durable sequence number of a session, or -1 when it has none.
func lastSeq(ctx context.Context, c *opencode.Client, sid string) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	seq := int64(-1)
	err := c.Log(cctx, sid, -1, false, func(f opencode.Frame) bool {
		ev, ok := opencode.ParseEvent(f)
		switch {
		case !ok:
		case ev.Type == opencode.EvLogSynced && ev.Seq != nil && *ev.Seq > seq:
			seq = *ev.Seq
		case ev.Durable != nil && ev.Durable.Seq > seq:
			seq = ev.Durable.Seq
		}
		return true
	})
	return seq, err
}

// prompt records the session totals, subscribes to events, then sends the task. The global event feed has
// no replay, so the subscription must be live before the prompt is admitted.
// task is the caller's text (hashed for the history); send is what the model receives.
func (e *Engine) prompt(ctx context.Context, c *opencode.Client, rec *SessionRecord, task, send string, files []opencode.FileAttachment, sent *bool) (*watch, error) {
	seq, err := lastSeq(ctx, c, rec.SessionID)
	if err != nil {
		return nil, fmt.Errorf("reading session log: %w", err)
	}
	info, err := c.GetSession(ctx, rec.SessionID)
	if err != nil {
		return nil, err
	}
	rec.PromptSeq, rec.CostBefore, rec.TokensBefore = seq, info.Cost, info.Tokens
	rec.PromptAt, rec.PromptHash, rec.Recorded = time.Now().UTC(), shortHash(task), false
	rec.Turns++
	w := e.startWatch(c, rec)
	select {
	case <-w.ready:
	case <-time.After(15 * time.Second):
		w.stop()
		return nil, errors.New("could not subscribe to OpenCode events")
	}
	item, err := c.Prompt(ctx, rec.SessionID, opencode.Prompt{Text: send, Files: files})
	if err != nil {
		w.stop()
		var ae *opencode.APIError
		if !errors.As(err, &ae) {
			// The prompt may have been admitted even though the answer was lost: keep the reservation,
			// which counts as spent if nothing settles it.
			*sent = true
		}
		return nil, err
	}
	*sent = true
	rec.PromptID = item.ID
	if e.obs != nil {
		e.obs.RunStarted(e.runInfo(rec))
	}
	return w, rec.save()
}

// Wait resumes blocking on a session that returned still_running.
func (e *Engine) Wait(ctx context.Context, sid string, waitS int, progress func(float64, string)) (*Result, error) {
	rec, err := loadRecord(sid)
	if err != nil {
		return nil, fmt.Errorf("unknown session %s", sid)
	}
	w := e.watch(sid)
	if w == nil {
		if rec.Closed {
			if res, err := loadFinal(sid); err == nil {
				res.Note = strings.TrimSpace(res.Note + " the session has ended and its jail was removed")
				return res, nil
			}
		}
		_, c, err := e.jailFor(ctx, rec)
		if err != nil {
			return nil, err
		}
		w = e.startWatch(c, rec)
	}
	return e.block(ctx, w, waitDuration(waitS), progress)
}

// Answer replies to an escalated permission request, then keeps blocking like Wait.
func (e *Engine) Answer(ctx context.Context, sid, requestID, decision, message string, waitS int, progress func(float64, string)) (*Result, error) {
	if decision == "always" {
		return nil, opencode.ErrAlways
	}
	if decision != "once" && decision != "reject" {
		return nil, errors.New(`decision must be "once" or "reject"`)
	}
	rec, err := loadRecord(sid)
	if err != nil {
		return nil, fmt.Errorf("unknown session %s", sid)
	}
	_, c, err := e.jailFor(ctx, rec)
	if err != nil {
		return nil, err
	}
	w := e.watch(sid)
	if w == nil {
		w = e.startWatch(c, rec)
	}
	w.mu.Lock()
	w.seen[requestID] = true
	w.issued[requestID] = true
	w.mu.Unlock()
	if err := c.ReplyPermission(ctx, sid, requestID, decision, message); err != nil {
		return nil, err
	}
	w.answered(requestID, decision)
	return e.block(ctx, w, waitDuration(waitS), progress)
}

// Abort interrupts a running session.
func (e *Engine) Abort(ctx context.Context, sid string) (*Result, error) {
	rec, err := loadRecord(sid)
	if err != nil {
		return nil, fmt.Errorf("unknown session %s", sid)
	}
	_, c, err := e.jailFor(ctx, rec)
	if err != nil {
		return nil, err
	}
	if w := e.watch(sid); w != nil {
		w.setNote("aborted by the caller")
		w.markRelayInterrupt()
	}
	if _, err := c.Interrupt(ctx, sid); err != nil {
		return nil, err
	}
	w := e.watch(sid)
	if w == nil {
		w = e.startWatch(c, rec)
	}
	return e.block(ctx, w, 20*time.Second, nil)
}

// Shutdown ends every session this process owns: it interrupts runs that are still active, records
// them, and removes their jails. Called when the MCP server's stdin closes (the Claude Code session ended).
func (e *Engine) Shutdown() {
	e.mu.Lock()
	ws := make([]*watch, 0, len(e.watches))
	for _, w := range e.watches {
		ws = append(ws, w)
	}
	sids := make([]string, 0, len(e.jails))
	for sid := range e.jails {
		sids = append(sids, sid)
	}
	e.mu.Unlock()
	for _, w := range ws {
		if !w.finished() {
			w.markRelayInterrupt()
			w.setNote("interrupted: the Claude Code session ended")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.c.Interrupt(ctx, w.rec.SessionID)
			cancel()
		}
	}
	deadline := time.After(10 * time.Second)
	for _, w := range ws {
		select {
		case <-w.done:
		case <-deadline:
		}
		if !w.finished() {
			w.finish("interrupted", "interrupted (the Claude Code session ended)")
		}
	}
	var wg sync.WaitGroup
	for _, sid := range sids {
		wg.Add(1)
		go func() { defer wg.Done(); e.closeSession(sid) }()
	}
	wg.Wait()
	for _, w := range ws {
		w.stop()
	}
}

func (e *Engine) watch(sid string) *watch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watches[sid]
}
