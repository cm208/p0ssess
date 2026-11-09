package relay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/opencode"
)

// watch follows one prompt's execution: durable events from the session log (resuming after the last
// seen seq if the stream drops), pending permission requests, the cost cap and the maximum runtime.
type watch struct {
	e   *Engine
	c   *opencode.Client
	rec *SessionRecord

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	poke   chan struct{}
	ready  chan struct{}
	once   sync.Once

	mu        sync.Mutex
	seq       int64
	steps     int
	tools     []string
	toolNames map[string]string
	outcome   string
	errMsg    string
	note      string
	pending   *opencode.PermissionRequest
	seen      map[string]bool
	issued    map[string]bool
	rejected  []string
	escalated int
	cost      float64
	closed    bool

	tampered       bool
	tamperReason   string
	relayInterrupt bool
}

func (e *Engine) startWatch(c *opencode.Client, rec *SessionRecord) *watch {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.watches[rec.SessionID]; old != nil {
		old.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &watch{e: e, c: c, rec: rec, ctx: ctx, cancel: cancel, done: make(chan struct{}), poke: make(chan struct{}, 1), ready: make(chan struct{}),
		seq: rec.PromptSeq, toolNames: map[string]string{}, seen: map[string]bool{}, issued: map[string]bool{}}
	e.watches[rec.SessionID] = w
	go w.follow()
	go w.poll()
	go w.recordWhenDone()
	return w
}

// recordWhenDone records a run that ends while nobody is waiting on it (a caller got still_running and
// never called wait), so its cost reaches the ledger.
func (w *watch) recordWhenDone() {
	<-w.ctx.Done()
	if !w.finished() {
		return
	}
	time.Sleep(3 * time.Second)
	w.e.recMu.Lock()
	recorded := w.rec.Recorded
	w.e.recMu.Unlock()
	if !recorded {
		w.e.result(w, "")
	}
}

func (w *watch) stop() { w.cancel() }

func (w *watch) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *watch) signal() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

func (w *watch) finish(outcome, errMsg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	w.outcome = outcome
	if errMsg != "" {
		w.errMsg = errMsg
	}
	close(w.done)
	w.cancel()
}

func (w *watch) setNote(s string) {
	w.mu.Lock()
	w.note = s
	w.mu.Unlock()
}

func (w *watch) answered(requestID, decision string) {
	w.mu.Lock()
	if w.pending != nil && w.pending.ID == requestID {
		w.pending = nil
	}
	w.mu.Unlock()
}

// markIssued records a permission reply the relay sent, so the matching permission.replied event is not
// mistaken for tampering. It is called before the reply so the event cannot race ahead of it.
func (w *watch) markIssued(requestID string) {
	w.mu.Lock()
	w.issued[requestID] = true
	w.mu.Unlock()
}

// markRelayInterrupt records that the relay is the one interrupting the session, so the resulting
// interruption event is not treated as a foreign abort.
func (w *watch) markRelayInterrupt() {
	w.mu.Lock()
	w.relayInterrupt = true
	w.mu.Unlock()
}

// tamper records that something the relay did not issue acted on the session, then interrupts it. The run
// is reported with status "tampered". Only the first reason is kept.
func (w *watch) tamper(reason string) {
	w.mu.Lock()
	if w.tampered {
		w.mu.Unlock()
		return
	}
	w.tampered = true
	w.tamperReason = reason
	w.relayInterrupt = true
	w.mu.Unlock()
	fmt.Fprintf(w.e.log, "p0ssess: tamper on %s: %s\n", w.rec.SessionID, reason)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.c.Interrupt(ctx, w.rec.SessionID)
	w.signal()
}

var watchedEvents = []string{
	opencode.EvExecutionSucceeded, opencode.EvExecutionFailed, opencode.EvExecutionInterrupted,
	opencode.EvStepEnded, opencode.EvToolInputStarted, opencode.EvPermissionAsked,
	opencode.EvPermissionReplied,
}

// follow reads the live event feed for this session. After a reconnect anything missed is recovered by
// poll, which checks the session's outcome and pending permission requests directly.
func (w *watch) follow() {
	backoff := time.Second
	for w.ctx.Err() == nil && !w.finished() {
		err := w.c.Events(w.ctx, watchedEvents, func(f opencode.Frame) bool {
			ev, ok := opencode.ParseEvent(f)
			if !ok {
				return true
			}
			if ev.Type == opencode.EvServerConnected {
				w.once.Do(func() { close(w.ready) })
				return true
			}
			if ev.SessionID() != w.rec.SessionID {
				return true
			}
			w.handle(ev)
			return !w.finished()
		})
		if w.finished() || w.ctx.Err() != nil {
			return
		}
		if err != nil {
			fmt.Fprintf(w.e.log, "p0ssess: event feed: %v; reconnecting\n", err)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func (w *watch) handle(ev opencode.Event) {
	w.mu.Lock()
	if ev.Durable != nil {
		if ev.Durable.Seq <= w.seq {
			w.mu.Unlock()
			return
		}
		w.seq = ev.Durable.Seq
	}
	switch ev.Type {
	case opencode.EvStepEnded:
		var d opencode.StepEnded
		if ev.Decode(&d) == nil {
			w.steps++
		}
	case opencode.EvPermissionAsked:
		go func() {
			ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
			defer cancel()
			w.checkPermissions(ctx)
		}()
	case opencode.EvToolInputStarted:
		var d opencode.ToolInputStarted
		if ev.Decode(&d) == nil {
			w.toolNames[d.ID] = d.Name
			w.tools = append(w.tools, d.Name)
		}
	case opencode.EvPermissionReplied:
		// Only the relay answers permission requests. A reply the relay did not issue means something
		// else (a model that found the API password, say) answered one, so the run is tampered.
		var d opencode.PermissionReplied
		if ev.Decode(&d) == nil && d.RequestID != "" && !w.issued[d.RequestID] {
			w.mu.Unlock()
			w.tamper("a permission reply the relay did not issue (request " + d.RequestID + ")")
			return
		}
	}
	w.mu.Unlock()
	switch ev.Type {
	case opencode.EvExecutionSucceeded:
		w.finish("succeeded", "")
	case opencode.EvExecutionFailed:
		var d opencode.ExecutionFailed
		_ = ev.Decode(&d)
		msg := "execution failed"
		if d.Error != nil {
			msg = d.Error.Message
		}
		w.finish("failed", msg)
	case opencode.EvExecutionInterrupted:
		var d opencode.ExecutionInterrupted
		_ = ev.Decode(&d)
		w.mu.Lock()
		foreign := !w.relayInterrupt && !w.tampered
		w.mu.Unlock()
		if foreign {
			w.tamper("the session was interrupted by something other than the relay")
		}
		w.finish("interrupted", "interrupted ("+d.Reason+")")
	}
}

func (w *watch) poll() {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	maxRun := time.Duration(w.e.cfg.MaxRuntimeSec) * time.Second
	var lastInfo time.Time
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		w.checkPermissions(ctx)
		if time.Since(lastInfo) > 5*time.Second {
			lastInfo = time.Now()
			w.checkSession(ctx)
		}
		cancel()
		if maxRun > 0 && time.Since(w.rec.PromptAt) > maxRun {
			w.interrupt(fmt.Sprintf("stopped: ran longer than the %s maximum", maxRun))
		}
	}
}

func (w *watch) interrupt(note string) {
	w.setNote(note)
	w.markRelayInterrupt()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.c.Interrupt(ctx, w.rec.SessionID)
}

func (w *watch) checkPermissions(ctx context.Context) {
	reqs, err := w.c.Permissions(ctx, w.rec.SessionID)
	if err != nil {
		return
	}
	for _, r := range reqs {
		w.mu.Lock()
		seen := w.seen[r.ID]
		w.seen[r.ID] = true
		w.mu.Unlock()
		if seen {
			continue
		}
		desc := r.Action + " " + strings.Join(r.Resources, ", ")
		if w.rec.Mode != "edit" {
			w.markIssued(r.ID)
			if err := w.c.ReplyPermission(ctx, w.rec.SessionID, r.ID, "reject", rejectFeedback); err == nil {
				w.mu.Lock()
				w.rejected = append(w.rejected, desc)
				w.mu.Unlock()
			}
			continue
		}
		w.mu.Lock()
		if w.pending == nil {
			req := r
			w.pending = &req
			w.escalated++
		}
		w.mu.Unlock()
		w.signal()
	}
}

func (w *watch) checkSession(ctx context.Context) {
	s, err := w.c.GetSession(ctx, w.rec.SessionID)
	if err != nil {
		return
	}
	w.mu.Lock()
	w.cost = s.Cost - w.rec.CostBefore
	cost := w.cost
	w.mu.Unlock()
	capUSD := w.rec.Grant.CapUSD
	if capUSD <= 0 {
		capUSD = w.e.cfg.MaxRunCostUSD
	}
	if cap := capUSD; cap > 0 && cost > cap {
		w.interrupt(fmt.Sprintf("stopped: run cost $%.4f passed the $%.2f cap", cost, cap))
	}
	// Backstop for a missed terminal event: the session went idle after this prompt was sent.
	promptMs := float64(w.rec.PromptAt.UnixMilli())
	if s.Outcome != "" && s.Time.Idle >= promptMs && s.Time.Idle >= s.Time.Busy && s.Time.Busy >= promptMs-2000 {
		time.Sleep(2 * time.Second)
		if !w.finished() {
			w.finish(s.Outcome, "")
		}
	}
}

func (w *watch) progressLine() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	el := time.Since(w.rec.PromptAt).Round(time.Second)
	s := fmt.Sprintf("%s: %s, %d steps", w.rec.Model, el, w.steps)
	if n := len(w.tools); n > 0 {
		last := w.tools
		if n > 3 {
			last = last[n-3:]
		}
		s += ", tools " + strings.Join(last, " ")
	}
	if w.cost > 0 {
		s += fmt.Sprintf(", $%.4f", w.cost)
	}
	return s
}

// block waits for the watch to end, ask for a decision, or for waitFor to pass. Progress goes out every
// 15 s so Claude Code's idle timer never fires. A cancelled ctx interrupts the run.
func (e *Engine) block(ctx context.Context, w *watch, waitFor time.Duration, progress func(float64, string)) (*Result, error) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	limit := time.NewTimer(waitFor)
	defer limit.Stop()
	start := time.Now()
	for {
		w.mu.Lock()
		pending := w.pending
		w.mu.Unlock()
		if pending != nil && !w.finished() {
			return e.result(w, "needs_permission")
		}
		select {
		case <-w.done:
			return e.result(w, "")
		case <-w.poke:
		case <-tick.C:
			if progress != nil {
				progress(time.Since(start).Seconds(), w.progressLine())
			}
		case <-limit.C:
			return e.result(w, "still_running")
		case <-ctx.Done():
			w.interrupt("interrupted: the caller cancelled")
			select {
			case <-w.done:
			case <-time.After(15 * time.Second):
			}
			return e.result(w, "interrupted")
		}
	}
}
