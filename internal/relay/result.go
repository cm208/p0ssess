package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	Tokens         *opencode.Tokens `json:"tokens,omitempty"`
	DurationS      float64          `json:"duration_s"`
	TranscriptPath string           `json:"transcript_path,omitempty"`
	Permission     *Escalation      `json:"permission,omitempty"`
	Worktree       string           `json:"worktree,omitempty"`
	DiffStat       string           `json:"diff_stat,omitempty"`
	DiffPath       string           `json:"diff_path,omitempty"`
	Diff           string           `json:"diff,omitempty"`
	AsksRejected   []string         `json:"asks_rejected,omitempty"`
	Error          string           `json:"error,omitempty"`
	Note           string           `json:"note,omitempty"`
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
	r := &Result{Status: status, SessionID: rec.SessionID, Model: rec.Model, Mode: rec.Mode, Note: note,
		AsksRejected: rejected, DurationS: time.Since(rec.PromptAt).Round(100 * time.Millisecond).Seconds()}
	if errMsg != "" && status != "done" {
		r.Error, _ = clip(errMsg, errorCap, "…")
	}
	if s, err := w.c.GetSession(ctx, rec.SessionID); err == nil {
		r.CostUSD = s.Cost - rec.CostBefore
		t := subTokens(s.Tokens, rec.TokensBefore)
		r.Tokens = &t
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
		if stat, patch, err := worktreeDiff(rec.Worktree, rec.BaseRev); err == nil {
			r.DiffStat = stat
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
	if status != "still_running" && status != "needs_permission" && !rec.Recorded {
		rec.Recorded = true
		rec.save()
		appendHistory(HistoryEntry{Time: time.Now().UTC(), Kind: rec.Kind, Model: rec.Model, Mode: rec.Mode,
			Outcome: status, CostUSD: r.CostUSD, Tokens: r.Tokens, DurationS: r.DurationS, Session: rec.SessionID,
			Turn: rec.Turns, TaskHash: rec.PromptHash, Rejected: len(rejected), Escalated: escalated})
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
