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
)

const rejectFeedback = "Not allowed in this delegated read-only task, and nobody is available to approve it. " +
	"Continue without it, and say in your final answer what you could not do."

type Engine struct {
	cfg     config.Config
	version string
	log     io.Writer

	mu      sync.Mutex
	watches map[string]*watch
}

func NewEngine(cfg config.Config, version string, log io.Writer) *Engine {
	return &Engine{cfg: cfg, version: version, log: log, watches: map[string]*watch{}}
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

func defaultCwd() string {
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
	var rec *SessionRecord
	var err error
	if req.Session != "" {
		if rec, err = loadRecord(req.Session); err != nil {
			return nil, fmt.Errorf("unknown session %s (only sessions started by this relay can be continued)", req.Session)
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
	j, c, err := jail.Ensure(ctx, rec.Repo, e.cfg, e.log)
	if err != nil {
		return nil, fmt.Errorf("jail: %w", err)
	}
	_ = j
	if req.Session == "" {
		if err := e.createSession(ctx, c, rec, req); err != nil {
			return nil, err
		}
	} else if req.Model != "" {
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
	if req.Kind != "" {
		rec.Kind = req.Kind
	}
	w, err := e.prompt(ctx, c, rec, req.Task, files)
	if err != nil {
		return nil, err
	}
	return e.block(ctx, w, waitDuration(req.WaitS), progress)
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
	if _, err := os.Stat(filepath.Join(repo, ".p0ssess-private")); err == nil {
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

func (e *Engine) createSession(ctx context.Context, c *opencode.Client, rec *SessionRecord, req RunRequest) error {
	if rec.Mode != "read" {
		wt, base, err := addWorktree(rec.Repo)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rec.Repo, rec.Cwd)
		rec.Worktree, rec.BaseRev = wt, base
		rec.Location = filepath.Join(wt, rel)
		if _, err := os.Stat(rec.Location); err != nil {
			rec.Location = wt
		}
	}
	m, _ := ParseModel(req.Model)
	s, err := c.CreateSession(ctx, opencode.CreateSession{
		Model:       &m,
		Location:    &opencode.Location{Directory: rec.Location},
		Metadata:    map[string]any{"p0ssess": e.version, "mode": rec.Mode, "kind": rec.Kind},
		Permissions: Rules(rec.Mode),
	})
	if err != nil {
		if rec.Worktree != "" {
			removeWorktree(rec.Repo, rec.Worktree)
		}
		return err
	}
	rec.SessionID = s.ID
	return rec.save()
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
func (e *Engine) prompt(ctx context.Context, c *opencode.Client, rec *SessionRecord, task string, files []opencode.FileAttachment) (*watch, error) {
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
	item, err := c.Prompt(ctx, rec.SessionID, opencode.Prompt{Text: task, Files: files})
	if err != nil {
		w.stop()
		return nil, err
	}
	rec.PromptID = item.ID
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
		_, c, err := jail.Ensure(ctx, rec.Repo, e.cfg, e.log)
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
	_, c, err := jail.Ensure(ctx, rec.Repo, e.cfg, e.log)
	if err != nil {
		return nil, err
	}
	w := e.watch(sid)
	if w == nil {
		w = e.startWatch(c, rec)
	}
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
	_, c, err := jail.Ensure(ctx, rec.Repo, e.cfg, e.log)
	if err != nil {
		return nil, err
	}
	if w := e.watch(sid); w != nil {
		w.setNote("aborted by the caller")
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

// Shutdown interrupts every run that is still active. Called when stdin closes.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	ws := make([]*watch, 0, len(e.watches))
	for _, w := range e.watches {
		ws = append(ws, w)
	}
	e.mu.Unlock()
	for _, w := range ws {
		if !w.finished() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.c.Interrupt(ctx, w.rec.SessionID)
			cancel()
		}
		w.stop()
	}
}

func (e *Engine) watch(sid string) *watch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watches[sid]
}
