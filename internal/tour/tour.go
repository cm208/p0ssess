// Package tour is the first-run tour: four short steps Claude gives in chat in the first interactive
// session after install (what p0ssess does, the spending envelope, one live demo route, where to see
// spend). It starts once, never repeats on its own, and /p0ssess:tour runs it again. An install that
// used p0ssess before the tour existed gets one line offering it instead.
package tour

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/pme/paths"
)

const (
	Started = "started" // the tour was handed to Claude once
	Offered = "offered" // an existing user got the one-line offer
	Done    = "done"
	Skipped = "skipped"
)

type State struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

func file(d paths.Dirs) string { return filepath.Join(d.State, "tour.json") }

// Load returns the saved state; ok is false before the tour was ever started or offered.
func Load(d paths.Dirs) (State, bool) {
	b, err := os.ReadFile(file(d))
	if err != nil {
		return State{}, false
	}
	var s State
	if json.Unmarshal(b, &s) != nil || s.State == "" {
		return State{}, false
	}
	return s, true
}

// Set records a state, replacing any.
func Set(d paths.Dirs, state string, now time.Time) error {
	b, _ := json.Marshal(State{State: state, At: now.UTC()})
	return paths.WriteFileAtomic(file(d), b, 0o600)
}

// claim records the first state exactly once, so two sessions starting together cannot both run it.
func claim(d paths.Dirs, state string, now time.Time) bool {
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(file(d), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := json.Marshal(State{State: state, At: now.UTC()})
	_, err = f.Write(b)
	return err == nil
}

// Interactive reports whether a person is at this Claude Code session. Print mode, the SDK, CI,
// subagent child sessions and p0ssess's own calibration runs are not.
func Interactive(getenv func(string) string) bool {
	if v := strings.ToLower(getenv("P0SSESS_TOUR")); v == "off" || v == "0" || v == "false" {
		return false
	}
	if getenv("CI") != "" || getenv("CLAUDE_CODE_CHILD_SESSION") == "1" || getenv("P0SSESS_BASELINE") != "" {
		return false
	}
	if v := getenv("CLAUDE_CODE_SESSION_ATTENDED"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	switch e := getenv("CLAUDE_CODE_ENTRYPOINT"); {
	case e == "":
		return false
	case strings.HasPrefix(e, "sdk"), e == "mcp", strings.Contains(e, "headless"), strings.Contains(e, "action"):
		return false
	}
	return true
}

// Existing reports whether this install used p0ssess before: it has a budget file, or it ran work
// before 1.1 (a new install's own first runs, the demo among them, do not count).
func Existing(d paths.Dirs) bool {
	if _, err := os.Stat(d.BudgetFile()); err == nil {
		return true
	}
	if budget.NewInstall(d) {
		return false
	}
	st, err := os.Stat(d.HistoryFile())
	return err == nil && st.Size() > 0
}

// Command is the slash command that runs the tour.
const Command = "/p0ssess:tour"

const startLine = `p0ssess: p0ssess was just installed and this is the user's first session with it. Answer the user's message as usual; ` +
	`then, in the same reply, give the short p0ssess tour: call the Skill tool with skill "p0ssess:tour" and follow it. ` +
	`It runs once; if the user says to skip it, run ` + "`p0ssess tour skip`" + ` with Bash and say nothing more about it.`

const offerLine = `p0ssess: tell the user this once, in one line, as written: New in p0ssess: a one-minute tour of your spending envelope, ` +
	`the status line and the p0ssess page; ` + Command + ` runs it.`

// Hook is the tour's part of the UserPromptSubmit hook: the start of the tour once in the first
// interactive session of a new install, one offer line for an install that used p0ssess before, and ""
// otherwise. A slash command defers it to the next plain message.
func Hook(d paths.Dirs, prompt string, getenv func(string) string, now time.Time) string {
	if !Interactive(getenv) {
		return ""
	}
	if _, ok := Load(d); ok {
		return ""
	}
	p := strings.TrimSpace(prompt)
	if strings.HasPrefix(p, Command) {
		claim(d, Started, now)
		return ""
	}
	if strings.HasPrefix(p, "/") || p == "" {
		return ""
	}
	if Existing(d) {
		if claim(d, Offered, now) {
			return offerLine
		}
		return ""
	}
	if claim(d, Started, now) {
		return startLine
	}
	return ""
}

// Envelope is a choice of limits the tour offers.
type Envelope struct {
	Name    string
	Daily   float64
	Monthly float64
	Task    float64
}

// Defaults is a new Free user's envelope.
func Defaults() Envelope {
	b := budget.Default()
	return Envelope{Name: "keep", Daily: b.DailyUSD, Monthly: b.MonthlyUSD, Task: b.PerTaskUSD}
}

// Choices are the one-answer changes: keep the shown envelope, half of it, or about double.
func Choices(cur Envelope) []Envelope {
	cur.Name = "keep"
	lower := Envelope{Name: "lower", Daily: round(cur.Daily / 2), Monthly: round(cur.Monthly / 2), Task: round(cur.Task * 0.4)}
	higher := Envelope{Name: "higher", Daily: round(cur.Daily * 2), Monthly: round(cur.Monthly * 2.5), Task: round(cur.Task * 2)}
	if cur.Daily <= 0 {
		lower.Daily, higher.Daily = cur.Daily, cur.Daily
	}
	return []Envelope{cur, lower, higher}
}

func round(v float64) float64 {
	if v >= 1 {
		return float64(int(v*2+0.5)) / 2
	}
	return float64(int(v*100+0.5)) / 100
}

// Money is a limit in the tour's words: $1, $0.25, $12.50.
func Money(v float64) string {
	if v == float64(int(v)) {
		return "$" + strconv.Itoa(int(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

func (e Envelope) daily() string {
	switch {
	case e.Daily < 0:
		return "no daily limit"
	case e.Daily == 0:
		return "a daily limit paced from the month"
	}
	return Money(e.Daily) + " a day (warns)"
}

// Line is the envelope in one line.
func (e Envelope) Line() string {
	return e.daily() + " · " + Money(e.Monthly) + " a month (stops paid runs) · runs without asking up to " + Money(e.Task) + " a task"
}

// SetCommand is the command that saves this envelope.
func (e Envelope) SetCommand() string {
	return fmt.Sprintf("p0ssess tour envelope --daily %s --monthly %s --task %s", num(e.Daily), num(e.Monthly), num(e.Task))
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Facts is what the steps say about this install.
type Facts struct {
	Envelope   Envelope
	New        bool // a new install: the envelope shown is the new-user default
	KeyReady   bool
	KeyFile    string
	StatusLine bool // the p0ssess status line segment is set up
	Team       bool
}

// What is step one.
const What = "p0ssess hands heavy, bulk and media work from Claude to cheaper models on OpenRouter, inside limits you set, " +
	"and gives back the result with a one-line receipt (model, cost, saving)."

// DemoTask is the sample task of step three: a short summary of a paragraph about p0ssess, so the demo
// shows a real hand-off on something the user cares about. DemoAsk is how the tour names it.
const (
	DemoTask = "Summarize this for a busy developer in one sentence of at most 20 words, no preamble:\n\n" +
		"p0ssess is a Claude Code plugin. When a task is heavy, repetitive or about media, it hands the work to a " +
		"cheaper model on OpenRouter, keeps Claude in charge of the conversation, stays inside the spending limits " +
		"the user set, and returns the result with a one-line receipt that names the model, the cost and the saving."
	DemoAsk     = "summarize a paragraph about p0ssess in one sentence"
	DemoUseCase = "summarize-long-doc"
	DemoMaxUSD  = 0.01
	// demoFallbackUseCase is tried when the route table has nothing for the first one.
	demoFallbackUseCase = "qa-factual"
)

// Show is the text of the four steps, one keyed line each, for the tour command to present.
func Show(f Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "step 1 what      %s\n", What)
	who := "your limits"
	if f.New {
		who = "the defaults for a new install"
	}
	fmt.Fprintf(&b, "step 2 envelope  %s: %s\n", who, f.Envelope.Line())
	for _, c := range Choices(f.Envelope) {
		label := map[string]string{"keep": "Keep these", "lower": "Lower", "higher": "Higher"}[c.Name]
		if c.Name == "keep" && !f.New {
			fmt.Fprintf(&b, "       choice    %s: no change\n", label)
			continue
		}
		fmt.Fprintf(&b, "       choice    %s: %s, %s a month, %s a task -> %s\n", label, dailyShort(c), Money(c.Monthly), Money(c.Task), c.SetCommand())
	}
	if f.KeyReady {
		fmt.Fprintf(&b, "step 3 demo      one live route on a tiny sample task, capped at %s -> p0ssess tour demo\n", Money(DemoMaxUSD))
	} else {
		fmt.Fprintf(&b, "step 3 demo      needs an OpenRouter key first: put it in %s (mode 0600), then p0ssess tour demo\n", f.KeyFile)
	}
	see := "status line: p0ssess $0.42/$1 · 1 running"
	if !f.StatusLine {
		see = "status line: not set up yet (p0ssess setup statusline adds it; ask first)"
	}
	see += " · the p0ssess page: /p0ssess:p0ssess-me"
	if f.Team {
		see += " · your team: /p0ssess:team"
	}
	fmt.Fprintf(&b, "step 4 see       %s\n", see)
	return b.String()
}

func dailyShort(e Envelope) string {
	if e.Daily <= 0 {
		return "no fixed daily limit"
	}
	return Money(e.Daily) + " a day"
}

// DemoResult is what one demo run gave.
type DemoResult struct {
	Model   string
	Answer  string
	Receipt string
	CostUSD float64
}

// Runner runs the demo task on the direct path under every limit, capped at maxUSD.
type Runner func(ctx context.Context, task, useCase string, maxUSD float64) (DemoResult, error)

// ErrNoKey means there is no OpenRouter key to run the demo with.
var ErrNoKey = errors.New("no OpenRouter key")

// Demo runs step three and returns its lines.
func Demo(ctx context.Context, run Runner, keyReady bool, keyFile string) (string, error) {
	if !keyReady {
		return fmt.Sprintf("skip     no OpenRouter key yet: put it in %s (mode 0600), then run p0ssess tour demo\n", keyFile), nil
	}
	res, err := run(ctx, DemoTask, DemoUseCase, DemoMaxUSD)
	if err != nil && (strings.Contains(err.Error(), "no model fits") || strings.Contains(err.Error(), "unknown use case")) {
		res, err = run(ctx, DemoTask, demoFallbackUseCase, DemoMaxUSD)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "asked    %s\n", DemoAsk)
	if res.Model != "" {
		fmt.Fprintf(&b, "model    %s\n", res.Model)
	}
	fmt.Fprintf(&b, "answer   %s\n", strings.Join(strings.Fields(res.Answer), " "))
	fmt.Fprintf(&b, "receipt  %s\n", res.Receipt)
	fmt.Fprintf(&b, "cost     %s\n", DemoCost(res.CostUSD))
	return b.String(), nil
}

// DemoCost says what the demo cost in a few words.
func DemoCost(v float64) string {
	switch {
	case v <= 0:
		return "nothing (a free route)"
	case v < 0.0001:
		return "under a hundredth of a cent"
	case v < 0.01:
		return fmt.Sprintf("$%.4f, under a cent", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
