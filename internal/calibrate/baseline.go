package calibrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Baseline modes.
const (
	ModeAlone = "alone" // Claude Code with every p0ssess tool denied and the p0ssess hooks quiet
	ModeWith  = "with"  // Claude Code with p0ssess as installed
)

// BaselineEnv is set for baseline runs; the p0ssess prompt hook prints nothing when it says alone.
const BaselineEnv = "P0SSESS_BASELINE"

// PriceTable is the Claude API price per million tokens (D38). Cache writes are charged at the
// input price, below what the API bills, so the Claude side is never overstated.
var PriceTable = []Price{
	{Model: "claude-opus-5.5", In: 4, Out: 20, CacheRead: 0.20},
	{Model: "claude-sonnet-5.5", In: 2, Out: 10, CacheRead: 0.10},
	{Model: "claude-haiku-5.5", In: 0.10, Out: 0.50, CacheRead: 0.005},
}

// PriceFor finds a Claude model's price from a Claude Code or OpenRouter model id.
func PriceFor(model string) (Price, bool) {
	m := strings.ToLower(model)
	m = strings.TrimPrefix(m, "anthropic/")
	m = strings.ReplaceAll(m, "-5-5", "-5.5")
	for _, p := range PriceTable {
		if strings.HasPrefix(m, p.Model) {
			return p, true
		}
	}
	return Price{}, false
}

// Tokens is one Claude model's use in a run.
type Tokens struct {
	In         int `json:"in"`
	Out        int `json:"out"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// USD prices the tokens; cache writes at the input price.
func (t Tokens) USD(p Price) float64 {
	return (float64(t.In+t.CacheWrite)*p.In + float64(t.Out)*p.Out + float64(t.CacheRead)*p.CacheRead) / 1e6
}

// BaselineRun is one headless Claude Code run on one sample.
type BaselineRun struct {
	Sample  string    `json:"sample"`
	UseCase string    `json:"use_case"`
	Mode    string    `json:"mode"`
	Model   string    `json:"model"`
	User    bool      `json:"user,omitempty"`
	At      time.Time `json:"at"`
	Seconds float64   `json:"seconds"`
	Correct bool      `json:"correct"`
	Turns   int       `json:"turns,omitempty"`
	// Tokens is per Claude model, as Claude Code's result reports it.
	Tokens map[string]Tokens `json:"tokens,omitempty"`
	// ClaudeUSD is the tokens at API prices; nil when a model in the run has no known price.
	ClaudeUSD *float64 `json:"claude_usd,omitempty"`
	// ReportedUSD is Claude Code's own cost figure, kept for comparison.
	ReportedUSD float64 `json:"reported_usd,omitempty"`
	// OpenRouterUSD is what p0ssess spent during the run (with mode), from the ledger.
	OpenRouterUSD float64 `json:"openrouter_usd,omitempty"`
	// WeeklyShare is how much of the weekly limit the run used (0.004 is 0.4%); nil when not read.
	WeeklyShare *float64 `json:"weekly_share,omitempty"`
	// ShareLowerBound means the reading before the run was missing, so the run's first request is not in WeeklyShare.
	ShareLowerBound bool   `json:"share_lower_bound,omitempty"`
	Session         string `json:"session,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Graded reports whether the run counts.
func (r BaselineRun) Graded() bool { return r.Error == "" }

// TotalUSD is the run's whole spend; ok is false when Claude's part could not be priced.
func (r BaselineRun) TotalUSD() (float64, bool) {
	if r.ClaudeUSD == nil {
		return 0, false
	}
	return *r.ClaudeUSD + r.OpenRouterUSD, true
}

// Exec runs claude with args in dir and returns its standard output.
type Exec func(ctx context.Context, dir string, args, env []string) ([]byte, error)

// Reading is one weekly-limit reading.
type Reading struct {
	Used     float64 // fraction
	ResetsAt int64
	At       time.Time
}

// BaselineOptions configure a baseline run.
type BaselineOptions struct {
	Modes []string
	// Model is the Claude model, as `claude --model` takes it.
	Model string
	// MaxUSD caps each run (claude --max-budget-usd).
	MaxUSD float64
	// Timeout ends a run that takes longer.
	Timeout time.Duration
	// Work is a scratch folder; each run gets a fresh subfolder with a copy of its sample file.
	Work string
	Now  func() time.Time
	// Spent sums what the ledger recorded for a Claude session between two times (with mode).
	Spent func(session string, from, to time.Time) float64
	// Reference is the status line's latest weekly reading, if any.
	Reference func() (Reading, bool)
}

// ClaudeArgs is the command line for one baseline run (after the claude executable).
func ClaudeArgs(mode, model, prompt string, maxUSD float64) []string {
	tools := "Bash,Read,Write,Edit,Glob,Grep"
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if maxUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", maxUSD))
	}
	if mode == ModeAlone {
		args = append(args, "--allowedTools", tools, "--disallowedTools", "mcp__plugin_p0ssess_p0ssess")
	} else {
		args = append(args, "--allowedTools", tools+",Task,Agent,mcp__plugin_p0ssess_p0ssess")
	}
	return args
}

// ClaudePrompt is the prompt both modes get: the sample's prompt, where its file is, and the answer line.
func ClaudePrompt(s Sample) string {
	p := strings.TrimSpace(s.Prompt)
	if s.Path != "" {
		p += "\n\nThe file is ./" + filepath.Base(s.Path) + " in the current folder."
	}
	return p + Instruction
}

// RunBaselines runs every sample in every mode, one at a time (concurrent runs would blur the weekly
// readings), and grades the replies.
func RunBaselines(ctx context.Context, samples []Sample, o BaselineOptions, run Exec, progress func(BaselineRun)) []BaselineRun {
	if o.Now == nil {
		o.Now = time.Now
	}
	var out []BaselineRun
	var prev *Reading
	if o.Reference != nil {
		if r, ok := o.Reference(); ok && o.Now().Sub(r.At) < 10*time.Minute {
			prev = &r
		}
	}
	for _, s := range samples {
		for _, mode := range o.Modes {
			if ctx.Err() != nil {
				return out
			}
			r, last := baselineOne(ctx, s, mode, o, run, prev)
			if last != nil {
				prev = last
			}
			out = append(out, r)
			if progress != nil {
				progress(r)
			}
		}
	}
	return out
}

func baselineOne(ctx context.Context, s Sample, mode string, o BaselineOptions, run Exec, prev *Reading) (BaselineRun, *Reading) {
	r := BaselineRun{Sample: s.Key(), UseCase: s.UseCase, Mode: mode, Model: o.Model, User: !s.Builtin, At: o.Now().UTC()}
	dir, err := os.MkdirTemp(o.Work, "run-")
	if err != nil {
		r.Error = err.Error()
		return r, nil
	}
	defer os.RemoveAll(dir)
	if s.Path != "" {
		b, err := os.ReadFile(s.Path)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, filepath.Base(s.Path)), b, 0o600)
		}
		if err != nil {
			r.Error = err.Error()
			return r, nil
		}
	}
	env := []string{BaselineEnv + "=" + mode}
	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	start := o.Now()
	stdout, err := run(cctx, dir, ClaudeArgs(mode, o.Model, ClaudePrompt(s), o.MaxUSD), env)
	end := o.Now()
	r.Seconds = end.Sub(start).Seconds()
	st := ParseStream(bytes.NewReader(stdout))
	r.Session, r.Turns, r.Tokens, r.ReportedUSD = st.Session, st.Turns, st.Tokens, st.CostUSD
	if st.Model != "" && r.Model == "" {
		r.Model = st.Model
	}
	if usd, ok := priceTokens(st.Tokens); ok {
		r.ClaudeUSD = &usd
	}
	if mode == ModeWith && o.Spent != nil {
		r.OpenRouterUSD = o.Spent(st.Session, start, end)
	}
	var last *Reading
	if n := len(st.Readings); n > 0 {
		l := st.Readings[n-1]
		last = &l
		ref, lower := st.Readings[0], true
		if prev != nil && prev.ResetsAt == l.ResetsAt {
			ref, lower = *prev, false
		}
		if ref.ResetsAt == l.ResetsAt {
			share := math.Max(0, l.Used-ref.Used)
			r.WeeklyShare, r.ShareLowerBound = &share, lower
		}
	}
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		// Out of time is a wrong answer, not a missing one: Claude had the task and did not finish it.
		r.Correct = false
	case st.Result == "" && err != nil:
		r.Error = oneLine(err.Error())
	case st.IsError && st.Result == "":
		r.Error = "claude reported an error: " + st.Subtype
	default:
		r.Correct = s.Grade(st.Result)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		r.Error = "canceled"
	}
	return r, last
}

func priceTokens(t map[string]Tokens) (float64, bool) {
	if len(t) == 0 {
		return 0, false
	}
	sum := 0.0
	for m, tok := range t {
		p, ok := PriceFor(m)
		if !ok {
			return 0, false
		}
		sum += tok.USD(p)
	}
	return sum, true
}

// Stream is what a `claude -p --output-format stream-json --verbose` run printed.
type Stream struct {
	Session  string
	Model    string
	Result   string
	IsError  bool
	Subtype  string
	Turns    int
	CostUSD  float64
	Tokens   map[string]Tokens
	Readings []Reading
}

type window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    int64    `json:"resetsAt"`
}

type streamLine struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	SessionID  string  `json:"session_id"`
	Model      string  `json:"model"`
	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	TotalCost  float64 `json:"total_cost_usd"`
	ModelUsage map[string]struct {
		InputTokens              int     `json:"inputTokens"`
		OutputTokens             int     `json:"outputTokens"`
		CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
		CostUSD                  float64 `json:"costUSD"`
	} `json:"modelUsage"`
	RateLimitInfo *struct {
		RateLimitType  string   `json:"rateLimitType"`
		Utilization    *float64 `json:"utilization"`
		ResetsAt       int64    `json:"resetsAt"`
		UnifiedWindows *struct {
			SevenDay *window `json:"seven_day"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
}

// ParseStream reads stream-json output line by line; lines it does not know are skipped.
func ParseStream(rd io.Reader) Stream {
	var st Stream
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var l streamLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.SessionID != "" && st.Session == "" {
			st.Session = l.SessionID
		}
		switch l.Type {
		case "system":
			if l.Subtype == "init" && l.Model != "" {
				st.Model = l.Model
			}
		case "rate_limit_event":
			if rd, ok := sevenDay(l); ok {
				st.Readings = append(st.Readings, rd)
			}
		case "result":
			st.Result, st.IsError, st.Subtype, st.Turns, st.CostUSD = l.Result, l.IsError, l.Subtype, l.NumTurns, l.TotalCost
			if len(l.ModelUsage) > 0 {
				st.Tokens = map[string]Tokens{}
				for m, u := range l.ModelUsage {
					st.Tokens[m] = Tokens{In: u.InputTokens, Out: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
				}
			}
		}
	}
	return st
}

// sevenDay reads the weekly window from a rate_limit_event: the unified windows when present, else the
// event's own utilization when it is about the weekly limit. Utilization is a fraction; a value above 1
// is read as a percentage.
func sevenDay(l streamLine) (Reading, bool) {
	ri := l.RateLimitInfo
	if ri == nil {
		return Reading{}, false
	}
	var u *float64
	var reset int64
	if ri.UnifiedWindows != nil && ri.UnifiedWindows.SevenDay != nil && ri.UnifiedWindows.SevenDay.Utilization != nil {
		u, reset = ri.UnifiedWindows.SevenDay.Utilization, ri.UnifiedWindows.SevenDay.ResetsAt
	} else if ri.RateLimitType == "seven_day" && ri.Utilization != nil {
		u, reset = ri.Utilization, ri.ResetsAt
	}
	if u == nil || math.IsNaN(*u) || *u < 0 {
		return Reading{}, false
	}
	v := *u
	if v > 1 {
		v /= 100
	}
	return Reading{Used: v, ResetsAt: reset}, true
}
