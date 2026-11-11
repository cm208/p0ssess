// Package cli is the engine's command line, mounted under the main command as
// "p0ssess engine ..." and "p0ssess me".
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/hook"
	"p0ssess/internal/pme/me"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/registry"
	"p0ssess/internal/pme/statusline"
	"p0ssess/internal/rules"
)

const usage = `p0ssess engine: the Predictive Model Engine

  recommend   rank routes for a task (--use-case ID or --text/--in/--out ...)
  status      spend, limits, Claude pressure and λ
  set K V     change a setting (budget.toml or engine.toml)
  rules       list | add '<json>' [--dry-run] | remove ID
  outcome     record what happened to an offer
  tendencies  learned patterns and rule suggestions
  usecases    list the catalog
  refresh     fetch OpenRouter's public model list (cached a day)
  statusline  status line wrapper (reads Claude Code's JSON on stdin)
  hook        UserPromptSubmit hook (reads the hook JSON on stdin)
  me          open the local settings page
  version
`

// Version is the binary's version; the main command sets it.
var Version = "dev"

type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

// Run executes args (without the program name) and returns an exit code.
func Run(args []string, s IO) int {
	if len(args) > 0 && args[0] == "engine" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(s.Out, usage)
		return 0
	}
	d := paths.Default()
	cmd, rest := args[0], args[1:]
	// The two commands Claude Code runs on every refresh and prompt load as
	// little as possible.
	switch cmd {
	case "statusline":
		in, _ := io.ReadAll(io.LimitReader(s.In, 1<<20))
		fmt.Fprintln(s.Out, statusline.Run(in, d, time.Now()))
		return 0
	case "hook":
		in, _ := io.ReadAll(io.LimitReader(s.In, 4<<20))
		out := ""
		if len(rest) > 0 && rest[0] == "tool" {
			out = hook.Tool(in, d)
		} else {
			out = hook.Run(in, d, time.Now())
		}
		if out != "" {
			fmt.Fprintln(s.Out, out)
		}
		return 0
	case "version":
		fmt.Fprintln(s.Out, "p0ssess", Version)
		return 0
	}
	e, err := engine.Load(d)
	if err != nil {
		fmt.Fprintln(s.Err, "p0ssess:", err)
		return 1
	}
	var runErr error
	switch cmd {
	case "recommend":
		runErr = recommend(e, rest, s)
	case "status":
		runErr = status(e, rest, s)
	case "set":
		if len(rest) != 2 {
			runErr = fmt.Errorf("usage: set KEY VALUE (keys: %s)", strings.Join(engine.SettingKeys(), ", "))
			break
		}
		var msg string
		msg, runErr = e.SetSetting(rest[0], rest[1])
		if runErr == nil {
			fmt.Fprintln(s.Out, msg)
		}
	case "rules":
		runErr = rulesCmd(e, rest, s)
	case "outcome":
		runErr = outcome(e, rest, s)
	case "tendencies":
		runErr = tendencies(e, rest, s)
	case "usecases":
		tw := tabwriter.NewWriter(s.Out, 0, 2, 2, ' ', 0)
		for _, u := range e.Cat.UseCases {
			lo, hi := u.StakesBounds()
			fmt.Fprintf(tw, "%d\t%s\t%s\tstakes %d-%d\t%s\t%s\n", u.N, u.ID, u.Kind(), lo, hi, u.Path, u.Task)
		}
		tw.Flush()
	case "refresh":
		runErr = refresh(e, rest, s)
	case "me":
		runErr = serveMe(e, rest, s)
	default:
		fmt.Fprintf(s.Err, "p0ssess: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if runErr != nil {
		if errors.Is(runErr, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(s.Err, "p0ssess:", runErr)
		return 1
	}
	return 0
}

func flags(name string, s IO) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(s.Err)
	return fs
}

func list(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func recommend(e *engine.Engine, args []string, s IO) error {
	fs := flags("recommend", s)
	uc := fs.String("use-case", "", "catalog id")
	text := fs.String("text", "", "short task description")
	in := fs.String("in", "", "input modalities, comma-separated")
	out := fs.String("out", "", "output modalities, comma-separated")
	path := fs.String("path", "", "direct or agent")
	stakes := fs.Int("stakes", 0, "1-4")
	size := fs.String("size", "", "T, S, M, R or L")
	var p catalog.Profile
	fs.IntVar(&p.InTokens, "in-tokens", 0, "")
	fs.IntVar(&p.OutTokens, "out-tokens", 0, "")
	fs.IntVar(&p.Bytes, "bytes", 0, "input size in bytes")
	fs.IntVar(&p.Turns, "turns", 0, "agent turns")
	fs.IntVar(&p.Items, "items", 0, "repeat count")
	fs.Float64Var(&p.AudioSec, "audio-seconds", 0, "")
	fs.Float64Var(&p.VideoSec, "video-seconds", 0, "")
	fs.IntVar(&p.Images, "images", 0, "")
	fs.IntVar(&p.Pages, "pages", 0, "")
	fs.IntVar(&p.Chars, "chars", 0, "")
	fs.Float64Var(&p.GenSec, "gen-seconds", 0, "")
	lead := fs.String("lead", "", "lead Claude model")
	top := fs.Int("top", 5, "options to show")
	cwd := fs.String("cwd", "", "project directory")
	asJSON := fs.Bool("json", false, "print JSON")
	record := fs.Bool("record", false, "record the offer in the history")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p.Text, p.Inputs, p.Outputs, p.Path, p.Size, p.Stakes = *text, list(*in), list(*out), *path, *size, *stakes
	req := engine.Request{UseCase: *uc, Profile: &p, Stakes: *stakes, Lead: *lead, Top: *top, Record: *record, Scope: engine.Scope{Cwd: *cwd}}
	rec, err := e.Recommend(req)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(s.Out, rec)
	}
	PrintRecommendation(s.Out, rec)
	return nil
}

// PrintRecommendation is the terminal view of a recommendation.
func PrintRecommendation(w io.Writer, r *engine.Recommendation) {
	fmt.Fprintf(w, "%s (#%d, %s) · stakes %d · λ %.2f (%s)\n", r.UseCase.ID, r.UseCase.N, r.UseCase.Kind, r.Stakes, r.Lambda.Value, r.Lambda.Note)
	if len(r.Classification) > 1 {
		var alts []string
		for _, m := range r.Classification[1:] {
			alts = append(alts, fmt.Sprintf("%s %.2f", m.ID, m.Confidence))
		}
		fmt.Fprintf(w, "matched %.2f; also %s\n", r.Classification[0].Confidence, strings.Join(alts, ", "))
	}
	if r.StakesNote != "" {
		fmt.Fprintln(w, r.StakesNote)
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\troute\tmodel\ttotal\tcash\tclaude\trisk\tp_fail\t")
	for _, o := range r.Options {
		claude := o.Breakdown.Claude + o.Breakdown.Overhead + o.Breakdown.Review
		ask := ""
		if o.NeedsApproval {
			ask = "ask: " + strings.Join(o.ApprovalReasons, "; ")
		} else if o.ApprovalID != "" {
			ask = "standing approval " + o.ApprovalID
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t$%.4f\t$%.4f\t$%.4f\t$%.4f\t%.2f\t%s\n", o.Rank, o.Route, shortID(o.Model), o.Total, o.Breakdown.Cash, claude, o.Breakdown.Risk, o.PFail, ask)
	}
	tw.Flush()
	if r.Pick != nil {
		fmt.Fprintf(w, "pick: %s\n      %s\n", r.Announce, r.Pick.Reason)
	} else {
		fmt.Fprintln(w, "no route fits")
	}
	for _, x := range r.Excluded {
		if x.Model == "*" {
			fmt.Fprintf(w, "excluded: %s\n", x.Why)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintln(w, "note:", n)
	}
	for _, sg := range r.Suggestions {
		fmt.Fprintln(w, "suggestion:", sg.Text)
	}
	if r.OfferID != "" {
		fmt.Fprintln(w, "offer:", r.OfferID)
	}
}

func shortID(id string) string {
	if strings.Contains(id, ":") && !strings.Contains(id, "/") {
		return id
	}
	if i := strings.Index(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func status(e *engine.Engine, args []string, s IO) error {
	fs := flags("status", s)
	asJSON := fs.Bool("json", false, "print JSON")
	cwd := fs.String("cwd", "", "project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := e.Status(engine.Scope{Cwd: *cwd})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(s.Out, st)
	}
	b := st.Budget
	daily := "off"
	if st.DailyLimit >= 0 {
		daily = fmt.Sprintf("$%.2f", st.DailyLimit)
	}
	fmt.Fprintf(s.Out, "spent $%.2f today, $%.2f of $%.2f this month (daily limit %s, per task $%.2f)\n", st.Spend.Today, st.Spend.Month, b.MonthlyUSD, daily, b.PerTaskUSD)
	fmt.Fprintf(s.Out, "plan %s · λ %.2f · %s\n", st.Plan, st.Lambda.Value, st.Lambda.Note)
	fmt.Fprintf(s.Out, "privacy %s · save Claude %v · live models: %s\n", b.Privacy.Mode, b.SaveClaude, st.LiveModels)
	for _, j := range st.Running {
		fmt.Fprintf(s.Out, "running: %s %s\n", shortID(j.Model), j.UseCase)
	}
	for _, o := range st.Overrides {
		fmt.Fprintln(s.Out, "override:", o)
	}
	return nil
}

func rulesCmd(e *engine.Engine, args []string, s IO) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := flags("rules "+sub, s)
	cwd := fs.String("cwd", "", "project directory")
	session := fs.String("session", "", "session id")
	dry := fs.Bool("dry-run", false, "validate and print without saving")
	expires := fs.Int("expires-in", 0, "minutes until the rule expires")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	scope := engine.Scope{Cwd: *cwd, Session: *session}
	switch sub {
	case "list":
		rs, err := e.RulesList(scope, "")
		if err != nil {
			return err
		}
		if len(rs) == 0 {
			fmt.Fprintln(s.Out, "no rules")
		}
		for _, r := range rs {
			x := ""
			if r.Expired {
				x = " [expired]"
			}
			fmt.Fprintf(s.Out, "%s  %s%s\n", r.ID, r.Text, x)
		}
		return nil
	case "add":
		if len(pos) != 1 {
			return errors.New(`usage: rules add '{"kind":"forbid","target":"model","models":["..."],"scope":"global"}'`)
		}
		var r rules.Rule
		if err := json.Unmarshal([]byte(pos[0]), &r); err != nil {
			return err
		}
		v, err := e.RuleAdd(scope, r, *dry, *expires)
		if err != nil {
			return err
		}
		if *dry {
			fmt.Fprintln(s.Out, "would save:", v.Text)
		} else {
			fmt.Fprintf(s.Out, "saved %s: %s\n", v.ID, v.Text)
		}
		return nil
	case "remove":
		if len(pos) != 1 {
			return errors.New("usage: rules remove ID")
		}
		v, err := e.RuleRemove(scope, pos[0])
		if err == nil {
			fmt.Fprintln(s.Out, "removed:", v.Text)
		}
		return err
	}
	return fmt.Errorf("unknown rules command %q", sub)
}

func outcome(e *engine.Engine, args []string, s IO) error {
	fs := flags("outcome", s)
	var o engine.Outcome
	fs.StringVar(&o.OfferID, "offer", "", "offer id from recommend")
	fs.StringVar(&o.Model, "model", "", "model that ran (or was offered)")
	fs.StringVar(&o.Outcome, "outcome", "", "accepted, redo, overridden or declined")
	fs.Float64Var(&o.CashUSD, "cash", 0, "OpenRouter dollars spent")
	fs.StringVar(&o.UseCase, "use-case", "", "catalog id")
	fs.StringVar(&o.Chosen, "chosen", "", "what ran instead (overridden)")
	fs.StringVar(&o.Reason, "reason", "", "a few words")
	if err := fs.Parse(args); err != nil {
		return err
	}
	en, err := e.RecordOutcome(o)
	if err == nil {
		fmt.Fprintf(s.Out, "recorded %s for %s\n", en.Outcome, shortID(en.Model))
	}
	return err
}

func tendencies(e *engine.Engine, args []string, s IO) error {
	fs := flags("tendencies", s)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l := e.Learned(engine.Scope{})
	if *asJSON {
		return writeJSON(s.Out, l)
	}
	if len(l.Tendencies) == 0 {
		fmt.Fprintln(s.Out, "nothing learned yet")
	}
	for _, t := range l.Tendencies {
		fmt.Fprintln(s.Out, t.Text)
	}
	for _, sg := range l.Suggestions {
		fmt.Fprintln(s.Out, "suggestion:", sg.Text)
	}
	return nil
}

func refresh(e *engine.Engine, args []string, s IO) error {
	fs := flags("refresh", s)
	force := fs.Bool("force", false, "ignore the cache age")
	if err := fs.Parse(args); err != nil {
		return err
	}
	age := registry.LiveMaxAge
	if *force {
		age = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	fetched, err := registry.Refresh(ctx, &http.Client{Timeout: 30 * time.Second}, e.Dirs.ModelsCache(), age, "")
	if err != nil {
		return err
	}
	if err := e.Reg.MergeLive(e.Dirs.ModelsCache()); err != nil {
		return err
	}
	verb := "cached list is fresh"
	if fetched {
		verb = "fetched"
	}
	fmt.Fprintf(s.Out, "%s: %d models as of %s\n", verb, e.Reg.LiveCount, e.Reg.LiveAsOf)
	return nil
}

func serveMe(e *engine.Engine, args []string, s IO) error {
	fs := flags("me", s)
	port := fs.Int("port", 0, "port on 127.0.0.1 (0 picks one)")
	open := fs.Bool("open", false, "open the page in a browser")
	idle := fs.Duration("for", 2*time.Hour, "stop after this long")
	cwd := fs.String("cwd", "", "project directory")
	state := fs.String("state", "", "write the page's pid, URL and end time to this file (mode 0600)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := me.Start(e, engine.Scope{Cwd: *cwd}, *port)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.Out, srv.URL())
	if *state != "" {
		b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "url": srv.URL(), "until": time.Now().Add(*idle)})
		if err := paths.WriteFileAtomic(*state, b, 0o600); err != nil {
			srv.Close()
			return err
		}
		defer os.Remove(*state)
	}
	if *open {
		switch runtime.GOOS {
		case "darwin":
			exec.Command("open", srv.URL()).Start()
		case "windows":
			exec.Command("rundll32", "url.dll,FileProtocolHandler", srv.URL()).Start()
		default:
			exec.Command("xdg-open", srv.URL()).Start()
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(*idle):
	}
	return srv.Close()
}

// parseAll parses flags that may come before or after positional arguments.
func parseAll(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
