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
	"p0ssess/internal/card"
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

// A DemoTask is one of the live demos step three offers.
type DemoTask struct {
	ID      string
	Label   string // the pop-up option
	Ask     string // what the result card says was asked
	Prompt  string
	UseCase string
}

// Demos are the step-three choices: each is a tiny text task, capped at DemoMaxUSD.
var Demos = []DemoTask{
	{ID: "summarize", Label: "Summarize", Ask: "summarize a paragraph about p0ssess",
		Prompt: "Summarize this for a busy developer in one sentence of at most 20 words, no preamble:\n\n" +
			"p0ssess is a Claude Code plugin. When a task is heavy, repetitive or about media, it hands the work to a " +
			"cheaper model on OpenRouter, keeps Claude in charge of the conversation, stays inside the spending limits " +
			"the user set, and returns the result with a one-line receipt that names the model, the cost and the saving.",
		UseCase: "summarize-long-doc"},
	{ID: "translate", Label: "Translate", Ask: `translate "It just works." into Japanese`,
		Prompt:  "Translate into natural Japanese. Answer with the translation only, no quotes or notes: It just works.",
		UseCase: "translate-text"},
	{ID: "brainstorm", Label: "Brainstorm", Ask: "name a coffee shop run by robots",
		Prompt:  "Give exactly three short, witty names for a coffee shop run by robots. One line, separated by \" · \", nothing else.",
		UseCase: "brainstorm"},
}

// DemoMaxUSD caps a demo run.
const DemoMaxUSD = 0.01

// demoFallbackUseCase is tried when the route table has nothing for a demo's own use case.
const demoFallbackUseCase = "summarize-long-doc"

// FindDemo returns the demo with this id, else the first.
func FindDemo(id string) DemoTask {
	for _, d := range Demos {
		if d.ID == id {
			return d
		}
	}
	return Demos[0]
}

// Show is what /p0ssess:tour reads: the first card to show verbatim, then the two pop-ups (envelope and
// demo) with each option's label, description, command and preview card.
func Show(f Facts) string {
	var b strings.Builder
	b.WriteString("=== CARD 1: your first message is exactly this code block ===\n")
	b.WriteString(card.Fenced(welcomeCard().String()))

	b.WriteString("\n=== QUESTION 2: Your spending envelope (2 / 4) ===\n")
	for _, c := range Choices(f.Envelope) {
		label := map[string]string{"keep": "Keep these", "lower": "Lower", "higher": "Higher"}[c.Name]
		cmd := c.SetCommand()
		desc := c.Short()
		if c.Name == "keep" {
			cmd = "none"
			if f.New {
				desc += " (recommended to start)"
			}
		}
		fmt.Fprintf(&b, "option  %s | %s | %s\n", label, desc, cmd)
		b.WriteString(envelopeCard(c).String())
	}
	b.WriteString("option  Skip the tour | stop here; /p0ssess:tour runs it again | p0ssess tour skip\n")

	b.WriteString("\n=== QUESTION 3: Pick a live demo (3 / 4) ===\n")
	if !f.KeyReady {
		fmt.Fprintf(&b, "nokey   put an OpenRouter key in %s (mode 0600), then run p0ssess tour demo\n", f.KeyFile)
	}
	for _, d := range Demos {
		fmt.Fprintf(&b, "option  %s | %s, capped at %s | p0ssess tour demo --task %s\n", d.Label, d.Ask, Money(DemoMaxUSD), d.ID)
		b.WriteString(card.Card{Title: "demo · " + strings.ToLower(d.Label), Rows: []string{
			card.Pair("you ask", d.Ask, 12),
			card.Pair("goes to", "the cheapest model that fits, capped at "+Money(DemoMaxUSD), 12),
			card.Pair("you get", "the answer, the time and the cost vs Claude", 12),
		}}.String())
	}
	return b.String()
}

func welcomeCard() card.Card {
	rows := []string{"Welcome to p0ssess. Glad you're here.", ""}
	rows = append(rows, card.Wrap("Claude stays in charge. Heavy, bulk and media work goes to cheaper models on OpenRouter, inside limits you set.", 60, "")...)
	rows = append(rows, "",
		"  you ─▶ Claude ─▶ p0ssess ─▶ cheaper model",
		"             ◀─ result + (model, cost, saved ~$)",
		"",
		"Three quick steps: your limits, a live demo, where to look.")
	return card.Card{Title: "ᗣ p0ssess tour", Right: "1 / 4", Rows: rows}
}

func envelopeCard(e Envelope) card.Card {
	day := Money(e.Daily) + " a day (warns)"
	switch {
	case e.Daily < 0:
		day = "no daily limit"
	case e.Daily == 0:
		day = "paced from the month (warns)"
	}
	return card.Card{Title: "envelope · " + e.Name, W: 48, Rows: []string{
		card.Pair("daily", day, 9),
		card.Pair("month", Money(e.Monthly)+" (stops paid runs)", 9),
		card.Pair("asks", "above "+Money(e.Task)+" a task", 9),
	}}
}

// Short is the envelope in a few words for a pop-up option.
func (e Envelope) Short() string {
	d := Money(e.Daily) + "/day"
	switch {
	case e.Daily < 0:
		d = "no daily limit"
	case e.Daily == 0:
		d = "paced daily"
	}
	return d + " · " + Money(e.Monthly) + "/month · asks above " + Money(e.Task)
}

// DemoResult is what one demo run gave.
type DemoResult struct {
	Model    string
	Answer   string
	Receipt  string
	CostUSD  float64
	SavedUSD float64
	Took     time.Duration
}

// Runner runs a demo prompt on the direct path under every limit, capped at maxUSD.
type Runner func(ctx context.Context, task, useCase string, maxUSD float64) (DemoResult, error)

// ErrNoKey means there is no OpenRouter key to run the demo with.
var ErrNoKey = errors.New("no OpenRouter key")

// Demo runs step three and returns its result card.
func Demo(ctx context.Context, run Runner, d DemoTask, keyReady bool, keyFile string) (string, error) {
	if !keyReady {
		return card.Fenced(card.Card{Title: "3 / 4 · live demo", Rows: append([]string{"No OpenRouter key yet."},
			card.Wrap("Put it in "+keyFile+" (mode 0600), then run p0ssess tour demo.", 60, "")...)}.String()), nil
	}
	t0 := time.Now()
	res, err := run(ctx, d.Prompt, d.UseCase, DemoMaxUSD)
	if err != nil && (strings.Contains(err.Error(), "no model fits") || strings.Contains(err.Error(), "unknown use case")) {
		res, err = run(ctx, d.Prompt, demoFallbackUseCase, DemoMaxUSD)
	}
	if err != nil {
		return "", err
	}
	if res.Took == 0 {
		res.Took = time.Since(t0)
	}
	model := res.Model
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	rows := []string{
		card.Spread(card.Pair("model", model, 9), fmt.Sprintf("%.1f s", res.Took.Seconds()), 60),
		card.Pair("asked", d.Ask, 9),
		"",
	}
	rows = append(rows, card.Wrap("“"+strings.Join(strings.Fields(res.Answer), " ")+"”", 58, "  ")...)
	rows = append(rows, "")
	cost := card.Pair("cost", DemoCost(res.CostUSD), 9)
	if res.SavedUSD > 0 {
		claude := res.CostUSD + res.SavedUSD
		cost += "  ·  ~" + DemoCost(claude) + " on Claude"
		rows = append(rows, cost, card.Pair("saved", fmt.Sprintf("%d%%  (~%s)", SavedPct(res.SavedUSD, claude), DemoCost(res.SavedUSD)), 9))
	} else {
		rows = append(rows, cost)
	}
	return card.Fenced(card.Card{Title: "3 / 4 · live demo", Right: "done", Rows: rows}.String()), nil
}

// DemoCost is a small amount in a few characters.
func DemoCost(v float64) string {
	switch {
	case v <= 0:
		return "$0 (free route)"
	case v < 0.0001:
		return "<$0.0001"
	case v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// DoneFacts is what the last card shows.
type DoneFacts struct {
	StatusLine string // the status line as it reads now, or "" when it is not set up
	Team       bool
}

// DoneCard is step four.
func DoneCard(f DoneFacts) string {
	sl := f.StatusLine
	if sl == "" {
		sl = "off · say \"add the p0ssess status line\""
	}
	rows := []string{
		card.Pair("status line", sl, 14),
		card.Pair("your page", "/p0ssess:p0ssess-me", 14),
	}
	if f.Team {
		rows = append(rows, card.Pair("your team", "/p0ssess:team", 14))
	}
	rows = append(rows,
		card.Pair("limits", "say it: \"cap p0ssess at $5 a month\"", 14),
		"",
		card.Pair("try now", "\"summarize the README with p0ssess\"", 14))
	return Farewell + "\n\n" + card.Fenced(card.Card{Title: "4 / 4 · you're set", Right: "ᗣ", Rows: rows}.String())
}

// Farewell is the line above the last card.
const Farewell = "That's the tour. Thanks for giving p0ssess a try. If anything feels off, write to support@p0ssess.me " +
	"or open an issue at github.com/cm208/p0ssess/issues."

// SavedPct is the saving as a whole percentage of what Claude would have cost.
func SavedPct(saved, claude float64) int {
	if claude <= 0 {
		return 0
	}
	return int(saved/claude*100 + 0.5)
}
