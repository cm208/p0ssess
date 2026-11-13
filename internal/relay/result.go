package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/direct"
	"p0ssess/internal/opencode"
)

// Result is what run, wait, abort and answer_permission return. The answer comes first: Claude Code
// spills tool results over 50k characters to a file, and the relay agent must still see the session id.
type Result struct {
	Answer         string           `json:"answer"`
	Status         string           `json:"status"`
	SessionID      string           `json:"session_id"`
	Model          string           `json:"model"`
	Mode           string           `json:"mode"`
	CostUSD        float64          `json:"cost_usd"`
	CostEstimated  bool             `json:"cost_estimated,omitempty"`
	Tokens         *opencode.Tokens `json:"tokens,omitempty"`
	DurationS      float64          `json:"duration_s"`
	TranscriptPath string           `json:"transcript_path,omitempty"`
	Permission     *Escalation      `json:"permission,omitempty"`
	Worktree       string           `json:"worktree,omitempty"`
	DiffStat       string           `json:"diff_stat,omitempty"`
	DiffPath       string           `json:"diff_path,omitempty"`
	// Files are the media and office files the run added or changed in its worktree, to present.
	Files        []direct.FileHint `json:"files,omitempty"`
	Diff         string            `json:"diff,omitempty"`
	AsksRejected []string          `json:"asks_rejected,omitempty"`
	Error        string            `json:"error,omitempty"`
	Note         string            `json:"note,omitempty"`
	// ShellInternet says the model's shell commands could reach the public internet in this run.
	ShellInternet bool `json:"shell_internet,omitempty"`
	// JailStartS is how long the session's jail took to start, on its first run.
	JailStartS float64 `json:"jail_start_s,omitempty"`
}

// ShellInternetNote is added to every result of a session with shell internet, so it is never on quietly.
const ShellInternetNote = "shell internet was ON for this run: the model's shell commands could reach public internet " +
	"addresses on ports 80 and 443 through the egress proxy (never the local network, the host, VPNs or the jail's own services)."

func saveFinal(r *Result) {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return
	}
	dir := RunDir(r.SessionID)
	if os.MkdirAll(dir, 0o700) == nil {
		os.WriteFile(filepath.Join(dir, "result.json"), b, 0o600)
	}
}

func loadFinal(sid string) (*Result, error) {
	b, err := os.ReadFile(filepath.Join(RunDir(sid), "result.json"))
	if err != nil {
		return nil, err
	}
	var r Result
	return &r, json.Unmarshal(b, &r)
}

type Escalation struct {
	RequestID string   `json:"request_id"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
	Hint      string   `json:"hint"`
}

const (
	answerCap = 30000
	diffCap   = 8000
	errorCap  = 1500
)

func clip(s string, n int, tail string) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + tail, true
}

func (e *Engine) result(w *watch, status string) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rec := w.rec
	w.mu.Lock()
	outcome, errMsg, note := w.outcome, w.errMsg, w.note
	pending := w.pending
	rejected := append([]string(nil), w.rejected...)
	escalated := w.escalated
	tampered, tamperReason := w.tampered, w.tamperReason
	w.mu.Unlock()
	if status == "" {
		switch outcome {
		case "succeeded":
			status = "done"
		case "interrupted":
			status = "interrupted"
		default:
			status = "failed"
		}
	}
	if tampered {
		// Tampering overrides every other outcome: the session acted on by something other than the relay
		// cannot be trusted, so the run is reported as tampered and (above) has been interrupted.
		status = "tampered"
		pending = nil
	}
	r := &Result{Status: status, SessionID: rec.SessionID, Model: rec.Model, Mode: rec.Mode, Note: note,
		AsksRejected: rejected, DurationS: time.Since(rec.PromptAt).Round(100 * time.Millisecond).Seconds(),
		ShellInternet: rec.ShellInternet}
	if errMsg != "" && status != "done" {
		r.Error, _ = clip(errMsg, errorCap, "…")
	}
	if tampered {
		r.Error = "tampered: " + tamperReason
	}
	if s, err := w.c.GetSession(ctx, rec.SessionID); err == nil {
		r.CostUSD = math.Round((s.Cost-rec.CostBefore)*1e6) / 1e6
		t := subTokens(s.Tokens, rec.TokensBefore)
		r.Tokens = &t
	} else {
		w.mu.Lock()
		r.CostUSD = math.Round(w.cost*1e6) / 1e6
		w.mu.Unlock()
		r.CostEstimated = true
	}
	msgs, msgErr := w.c.AllMessages(ctx, rec.SessionID)
	if msgErr == nil {
		r.Answer = finalAnswer(msgs, rec.PromptID)
		if r.Error == "" && status != "done" && status != "still_running" && status != "needs_permission" {
			r.Error = lastError(msgs, rec.PromptID)
		}
		dir := RunDir(rec.SessionID)
		if os.MkdirAll(dir, 0o700) == nil {
			p := filepath.Join(dir, "transcript.md")
			if os.WriteFile(p, []byte(renderTranscript(rec, msgs)), 0o600) == nil {
				r.TranscriptPath = p
			}
		}
	}
	if full := r.Answer; full != "" {
		var cut bool
		r.Answer, cut = clip(full, answerCap, "\n\n[answer truncated; the full text is in transcript_path]")
		if cut && r.TranscriptPath != "" {
			ap := filepath.Join(RunDir(rec.SessionID), fmt.Sprintf("answer-%d.md", rec.Turns))
			os.WriteFile(ap, []byte(full), 0o600)
		}
	}
	if pending != nil && status == "needs_permission" {
		r.Permission = &Escalation{RequestID: pending.ID, Action: pending.Action, Resources: pending.Resources,
			Hint: "The run is paused on this request. Ask the caller; then call answer_permission with decision once or reject."}
	}
	if rec.Worktree != "" {
		r.Worktree = rec.Worktree
		if err := e.syncCloud(rec); err != nil {
			r.Note = strings.TrimSpace(r.Note + " fetching the cloud sandbox's changes failed: " + err.Error())
		}
		if stat, patch, err := worktreeDiff(rec.Repo, rec.Worktree, rec.BaseRev); err == nil {
			r.DiffStat = stat
			r.Files = worktreeFiles(rec.Repo, rec.Worktree, rec.BaseRev)
			if patch != "" {
				p := filepath.Join(RunDir(rec.SessionID), "diff.patch")
				os.MkdirAll(filepath.Dir(p), 0o700)
				if os.WriteFile(p, []byte(patch), 0o600) == nil {
					r.DiffPath = p
				}
				r.Diff, _ = clip(patch, diffCap, "\n[diff truncated; the full patch is in diff_path]\n")
			}
		} else {
			r.Note = strings.TrimSpace(r.Note + " diff failed: " + err.Error())
		}
	}
	e.recMu.Lock()
	defer e.recMu.Unlock()
	if status != "still_running" && status != "needs_permission" && !rec.Recorded {
		rec.Recorded = true
		rec.save()
		appendHistory(HistoryEntry{Time: time.Now().UTC(), Kind: rec.Kind, Model: rec.Model, Mode: rec.Mode,
			Outcome: status, CostUSD: r.CostUSD, Tokens: r.Tokens, DurationS: r.DurationS, Session: rec.SessionID,
			Turn: rec.Turns, TaskHash: rec.PromptHash, Rejected: len(rejected), Escalated: escalated})
		if e.obs != nil {
			if err := e.obs.RunFinished(e.runInfo(rec), r); err != nil {
				r.Note = strings.TrimSpace(r.Note + " recording the cost failed (" + err.Error() + "); the reservation counts as spent")
			}
		}
	}
	if r.CostEstimated {
		r.Note = strings.TrimSpace(r.Note + " the run's cost was not measured (the session cost could not be read, or no price is known for the model); it is counted at its cap")
	}
	if r.ShellInternet {
		r.Note = strings.TrimSpace(ShellInternetNote + " " + r.Note)
	}
	if status != "still_running" && status != "needs_permission" {
		saveFinal(r)
	}
	return r, nil
}

// afterPrompt returns the messages that follow the user message with id promptID (all, if not found).
func afterPrompt(msgs []opencode.Message, promptID string) []opencode.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == "user" && msgs[i].ID == promptID {
			return msgs[i+1:]
		}
	}
	return msgs
}

func finalAnswer(msgs []opencode.Message, promptID string) string {
	tail := afterPrompt(msgs, promptID)
	for i := len(tail) - 1; i >= 0; i-- {
		if tail[i].Type == "assistant" {
			if t := strings.TrimSpace(tail[i].FinalText()); t != "" {
				return t
			}
		}
	}
	return ""
}

func lastError(msgs []opencode.Message, promptID string) string {
	tail := afterPrompt(msgs, promptID)
	for i := len(tail) - 1; i >= 0; i-- {
		if tail[i].Error != nil {
			s, _ := clip(tail[i].Error.Message, errorCap, "…")
			return s
		}
	}
	return ""
}

func renderTranscript(rec *SessionRecord, msgs []opencode.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# p0ssess session %s\n\n- model: %s\n- mode: %s\n- directory: %s\n", rec.SessionID, rec.Model, rec.Mode, rec.Location)
	if rec.Worktree != "" {
		fmt.Fprintf(&b, "- worktree: %s (base %s)\n", rec.Worktree, rec.BaseRev)
	}
	for _, m := range msgs {
		switch m.Type {
		case "user":
			fmt.Fprintf(&b, "\n## User\n\n%s\n", m.Text)
		case "assistant":
			b.WriteString("\n## Assistant")
			if m.Model != nil {
				fmt.Fprintf(&b, " (%s)", m.Model.ID)
			}
			b.WriteString("\n\n")
			for _, c := range m.Content {
				switch c.Type {
				case "text":
					b.WriteString(c.Text + "\n\n")
				case "tool":
					renderTool(&b, c)
				}
			}
			if m.Error != nil {
				fmt.Fprintf(&b, "**error:** %s\n\n", m.Error.Message)
			}
		case "model-switched", "agent-switched", "location-switched":
			fmt.Fprintf(&b, "\n_%s_\n", m.Type)
		}
	}
	return b.String()
}

func renderTool(b *strings.Builder, c opencode.ContentPart) {
	status := ""
	var input json.RawMessage
	var out []string
	if c.State != nil {
		status = c.State.Status
		input = c.State.Input
		for _, tc := range c.State.Content {
			if tc.Type == "text" {
				out = append(out, tc.Text)
			}
		}
		if c.State.Error != nil {
			out = append(out, "error: "+c.State.Error.Message)
		}
	}
	in, _ := clip(string(input), 2000, "…")
	fmt.Fprintf(b, "**tool %s** (%s)\n\n```json\n%s\n```\n\n", c.Name, status, in)
	if len(out) > 0 {
		o, _ := clip(strings.Join(out, "\n"), 4000, "\n…")
		fmt.Fprintf(b, "```\n%s\n```\n\n", o)
	}
}
