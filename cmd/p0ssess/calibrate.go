package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/calibrate"
	"p0ssess/internal/config"
	"p0ssess/internal/costbasis"
	"p0ssess/internal/direct"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/me"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/server"
	ustate "p0ssess/internal/usage"
)

const calibrateUsage = `usage: p0ssess calibrate [plan] | run --max-usd X [--yes] [--all] | status | baseline [flags] | report [--pdf FILE] | forget`

// calibrator holds what the calibrate commands use; tests replace the runner, the cloud and Claude.
type calibrator struct {
	d      paths.Dirs
	eng    *local.Engine
	runner calibrate.Runner
	put    func(context.Context, []byte) error
	claude calibrate.Exec
	now    func() time.Time
	out    io.Writer
	// userDir is the folder of the user's own samples.
	userDir string
}

// newCalibrator is replaced in tests.
var newCalibrator = func() (*calibrator, error) {
	d := paths.Default()
	eng, err := local.Load(d)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	rc := remote.New(d)
	return &calibrator{d: d, eng: eng, runner: directRunner{cfg: cfg, eng: eng}, put: rc.PutCalibration,
		claude: runClaude, now: time.Now, out: os.Stdout, userDir: calibrate.UserDir(d)}, nil
}

func calibrateCmd(ctx context.Context, args []string) error {
	c, err := newCalibrator()
	if err != nil {
		return err
	}
	return c.run(ctx, args)
}

func (c *calibrator) run(ctx context.Context, args []string) error {
	sub := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "report":
		return c.report(args)
	case "forget":
		return c.forget(ctx)
	case "status":
		return c.status(ctx)
	}
	if !account.Current(c.d).Pro() {
		fmt.Fprintln(c.out, "Calibration is part of p0ssess Pro: it tunes routing to results measured on your own key. See /p0ssess:pro.")
		return nil
	}
	switch sub {
	case "plan":
		return c.plan(ctx, args)
	case "run":
		return c.execute(ctx, args)
	case "baseline":
		return c.baseline(ctx, args)
	}
	return errors.New(calibrateUsage)
}

// directRunner sends calibration calls through the direct path, under every one of the user's limits,
// ledgered under the "calibrate" route.
type directRunner struct {
	cfg config.Config
	eng *local.Engine
}

// CalibrationMaxTokens caps a calibration reply, reasoning included. A reply cut off at the cap is a
// wrong answer.
const CalibrationMaxTokens = 2048

// calibrationRequest is the direct request for one calibration call. Reasoning models run at low effort
// so they finish inside the cap; OpenRouter ignores the setting for models that do not reason.
func calibrationRequest(c calibrate.Call) direct.Request {
	req := direct.Request{Model: c.Model, Prompt: c.Prompt, Output: direct.OutText, DryRun: c.DryRun,
		MaxTokens: CalibrationMaxTokens, Reasoning: "low"}
	if c.Transcript {
		req.Output = direct.OutTranscript
	}
	if c.File != "" {
		req.Files = []string{c.File}
	}
	return req
}

func (r directRunner) Run(ctx context.Context, c calibrate.Call) (calibrate.Reply, error) {
	req := calibrationRequest(c)
	res, _, err := server.RunDirect(ctx, r.cfg, r.eng, req, server.DirectJob{UseCase: c.UseCase, Stakes: c.Stakes, Route: "calibrate", Confirmed: true})
	var rep calibrate.Reply
	if res != nil {
		charged, basis := res.Usage.CostUSD, res.Usage.CostBasis
		if res.Billed() && !res.Measured() && !(res.Usage.Unmeasured == 0 && res.Usage.Priced > 0) {
			// The call's cost could be neither read nor priced: the ledger counts the reserved maximum, and
			// so does the run's ceiling.
			charged, basis = max(charged, res.Estimate.CostUSD), costbasis.Reserved
		}
		rep = calibrate.Reply{Text: res.Text, CostUSD: res.Usage.CostUSD, ChargedUSD: charged, CostBasis: basis, Elapsed: res.Elapsed,
			EstimateUSD: res.Estimate.CostUSD, EstimateUnknown: res.Estimate.CostUnknown}
	}
	return rep, err
}

func (c *calibrator) stakes(uc string) int { return calibrate.StakesFor(c.eng.Table.Table(), uc) }

// route is the models p0ssess would pick for a use case now, best first: direct routes only.
func (c *calibrator) route(_ context.Context, uc string) ([]string, error) {
	rec, err := c.eng.Recommend(local.Request{UseCase: uc, Stakes: c.stakes(uc), Top: 10})
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, o := range rec.Options {
		if o.Route != "direct" || strings.HasPrefix(o.Model, "anthropic/") || seen[o.Model] {
			continue
		}
		seen[o.Model] = true
		out = append(out, o.Model)
	}
	if len(out) == 0 && len(rec.Options) > 0 {
		return nil, errors.New("Claude does this kind itself at your settings")
	}
	return out, nil
}

// samples are the built-in samples (written to disk) and the user's own.
func (c *calibrator) samples() ([]calibrate.Sample, error) {
	b, err := calibrate.Builtin()
	if err != nil {
		return nil, err
	}
	if err := calibrate.Materialize(b, filepath.Join(c.d.Cache, "calibration-samples")); err != nil {
		return nil, err
	}
	u, err := calibrate.LoadUser(c.userDir)
	if err != nil {
		return nil, fmt.Errorf("your samples in %s: %w", c.userDir, err)
	}
	return append(b, u...), nil
}

func (c *calibrator) makePlan(ctx context.Context, all bool) (calibrate.Plan, *calibrate.Results, error) {
	ss, err := c.samples()
	if err != nil {
		return calibrate.Plan{}, nil, err
	}
	rs, err := calibrate.LoadResults(c.d)
	if err != nil {
		return calibrate.Plan{}, nil, err
	}
	p, err := calibrate.NewPlan(ctx, ss, c.route, c.runner, c.stakes, rs, !all)
	return p, rs, err
}

// ceiling is the most a run may spend: the estimates, which already price each reply at its token
// limit, with a margin for providers that bill a little above list, rounded up to the cent.
func ceiling(est float64) float64 { return math.Ceil(est*1.1*100+1e-9) / 100 }

func (c *calibrator) plan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate plan", flag.ContinueOnError)
	all := fs.Bool("all", false, "measure every pair again, not only new ones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, _, err := c.makePlan(ctx, *all)
	if err != nil {
		return err
	}
	meta := calibrate.LoadMeta(c.d)
	meta.Offered = true
	calibrate.SaveMeta(c.d, meta)
	for _, s := range p.Skipped {
		fmt.Fprintln(c.out, "skipped  "+s)
	}
	if len(p.Items) == 0 {
		fmt.Fprintln(c.out, "Calibration is up to date: every model your routes use has been measured.")
		return nil
	}
	bud, err := c.eng.Budget()
	if err != nil {
		return err
	}
	top := ceiling(p.EstimateUSD)
	fmt.Fprintf(c.out, "calibrate  %d calls on %d kinds of task, at most $%.2f on your OpenRouter key (every reply priced at its full token limit; most cost far less)\n",
		len(p.Items), len(p.Kinds), top)
	fmt.Fprintln(c.out, "kinds      "+strings.Join(p.Kinds, ", "))
	fmt.Fprintln(c.out, "sends      built-in samples with known answers, and yours from "+c.userDir+" if any; only right/wrong counts go to p0ssess")
	if top > bud.PerTaskUSD+1e-9 {
		fmt.Fprintf(c.out, "asks       this is above your $%.2f auto-approve limit. Ask the user first; on a yes run: p0ssess calibrate run --max-usd %.2f --yes%s\n", bud.PerTaskUSD, top, allFlag(*all))
	} else {
		fmt.Fprintf(c.out, "next       p0ssess calibrate run --max-usd %.2f%s\n", top, allFlag(*all))
	}
	return nil
}

func allFlag(all bool) string {
	if all {
		return " --all"
	}
	return ""
}

func (c *calibrator) execute(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate run", flag.ContinueOnError)
	maxUSD := fs.Float64("max-usd", 0, "the most the run may spend (from calibrate plan)")
	yes := fs.Bool("yes", false, "the user agreed to a cost above the auto-approve limit")
	all := fs.Bool("all", false, "measure every pair again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !(*maxUSD > 0) || math.IsInf(*maxUSD, 0) {
		return errors.New("pass --max-usd from `p0ssess calibrate plan`, so the cost is shown before anything runs")
	}
	bud, err := c.eng.Budget()
	if err != nil {
		return err
	}
	if *maxUSD > bud.PerTaskUSD+1e-9 && !*yes {
		return fmt.Errorf("$%.2f is above your $%.2f auto-approve limit: ask the user, and pass --yes only after they agree", *maxUSD, bud.PerTaskUSD)
	}
	p, rs, err := c.makePlan(ctx, *all)
	if err != nil {
		return err
	}
	if len(p.Items) == 0 {
		fmt.Fprintln(c.out, "Calibration is up to date; nothing ran.")
		return c.upload(ctx, rs)
	}
	if p.EstimateUSD > *maxUSD+1e-9 {
		return fmt.Errorf("the run can now cost up to $%.4f, above the $%.2f agreed; run `p0ssess calibrate plan` again", p.EstimateUSD, *maxUSD)
	}
	fmt.Fprintf(c.out, "calibrating  %d calls, at most $%.2f\n", len(p.Items), *maxUSD)
	res, spent, runErr := calibrate.Execute(ctx, p, c.runner, c.stakes, *maxUSD, c.now, nil)
	rs.Merge(res)
	if err := calibrate.SaveResults(c.d, rs); err != nil {
		return err
	}
	c.recordHistory(res)
	right, graded, failed := 0, 0, 0
	for _, r := range res {
		switch {
		case !r.Graded():
			failed++
		case r.Correct:
			right++
			graded++
		default:
			graded++
		}
	}
	fmt.Fprintf(c.out, "measured     %d of %d right, $%.4f spent", right, graded, spent)
	if failed > 0 {
		fmt.Fprintf(c.out, ", %d calls failed (not counted)", failed)
	}
	fmt.Fprintln(c.out)
	for _, line := range scoreLines(res) {
		fmt.Fprintln(c.out, "  "+line)
	}
	if errors.Is(runErr, calibrate.ErrCeiling) {
		fmt.Fprintf(c.out, "stopped      at the $%.2f ceiling; the rest waits for the next run\n", *maxUSD)
	} else if runErr != nil {
		return runErr
	}
	return c.upload(ctx, rs)
}

// recordHistory writes each graded result into the local history, where it tunes this user's own
// routing. A call that failed before a reply came back is not graded and writes nothing.
func (c *calibrator) recordHistory(res []calibrate.Result) {
	for _, r := range res {
		if r.Graded() {
			c.eng.RecordCalibration(r.UseCase, r.Model, c.stakes(r.UseCase), r.Correct)
		}
	}
}

// scoreLines is one line per kind and model from one run's results.
func scoreLines(res []calibrate.Result) []string {
	rs := &calibrate.Results{List: res}
	var out []string
	for _, s := range rs.Scores() {
		out = append(out, fmt.Sprintf("%-26s %-34s %d/%d", s.UseCase, history.Short(s.Model), s.Correct, s.N))
	}
	for _, r := range res {
		if !r.Graded() {
			out = append(out, fmt.Sprintf("%-26s %-34s failed: %s", r.UseCase, history.Short(r.Model), r.Error))
		}
	}
	return out
}

// upload sends the scores; when the cloud cannot take them now, they wait and go with the next start.
func (c *calibrator) upload(ctx context.Context, rs *calibrate.Results) error {
	uctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	meta := calibrate.LoadMeta(c.d)
	if err := calibrate.Upload(uctx, c.put, rs); err != nil {
		meta.Pending = true
		calibrate.SaveMeta(c.d, meta)
		fmt.Fprintln(c.out, "routing      p0ssess cloud is not reachable; your scores go with the next start")
		return nil
	}
	meta.Pending = false
	calibrate.SaveMeta(c.d, meta)
	fmt.Fprintln(c.out, "routing      your Pro routes now use these results")
	return nil
}

func (c *calibrator) status(ctx context.Context) error {
	rs, err := calibrate.LoadResults(c.d)
	if err != nil {
		return err
	}
	bs, _ := calibrate.LoadBaselines(c.d)
	if len(rs.List) == 0 {
		fmt.Fprintln(c.out, "Not calibrated yet. `p0ssess calibrate` shows what it would cost first.")
	} else {
		fmt.Fprintf(c.out, "calibrated   %s, %d measured calls\n", rs.Last().Local().Format("Jan 2 15:04"), len(rs.List))
		for _, line := range scoreLines(rs.List) {
			fmt.Fprintln(c.out, "  "+line)
		}
	}
	fmt.Fprintf(c.out, "baselines    %d Claude runs\n", len(bs.Runs))
	if calibrate.LoadMeta(c.d).Pending {
		fmt.Fprintln(c.out, "routing      scores waiting to reach p0ssess cloud")
	}
	return nil
}

func (c *calibrator) forget(ctx context.Context) error {
	if account.Token(c.d) != "" {
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := c.put(fctx, nil); err != nil {
			return fmt.Errorf("p0ssess cloud did not take the request (%v); nothing was deleted, try again later", err)
		}
	}
	if err := calibrate.Forget(c.d); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "Calibration results and Claude baselines are deleted here and on p0ssess cloud.")
	return nil
}

// Report builds the savings report from what is measured on this machine.
func (c *calibrator) Report() (calibrate.Report, error) { return buildReport(c.d, c.eng, c.now()) }

func buildReport(d paths.Dirs, eng *local.Engine, now time.Time) (calibrate.Report, error) {
	rs, err := calibrate.LoadResults(d)
	if err != nil {
		return calibrate.Report{}, err
	}
	bs, err := calibrate.LoadBaselines(d)
	if err != nil {
		return calibrate.Report{}, err
	}
	t := eng.Table.Table()
	picked := map[string]string{}
	kinds := map[string]bool{}
	for _, r := range rs.List {
		kinds[r.UseCase] = true
	}
	for _, b := range bs.Runs {
		kinds[b.UseCase] = true
	}
	for uc := range kinds {
		// The route table answers offline; the report never waits on the network.
		st := 2
		if u := t.UseCase(uc); u != nil && len(u.StakesRange) > 0 {
			st = min(max(u.StakesRange[0], 1), 3)
		}
		if e := t.Route(uc, st, 1); e != nil {
			for _, o := range e.Options {
				if o.Route == "direct" && !strings.HasPrefix(o.Model, "anthropic/") {
					picked[uc] = o.Model
					break
				}
			}
		}
	}
	return calibrate.BuildReport(calibrate.ReportInput{Pro: account.Current(d).Pro(), Now: now, Results: rs, Baselines: bs, Picked: picked,
		Task: func(uc string) string {
			if u := t.UseCase(uc); u != nil {
				return u.Task
			}
			return ""
		}}), nil
}

func (c *calibrator) report(args []string) error {
	fs := flag.NewFlagSet("calibrate report", flag.ContinueOnError)
	pdf := fs.String("pdf", "", "write the report as a PDF to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := c.Report()
	if err != nil {
		return err
	}
	if *pdf == "" {
		b, _ := json.MarshalIndent(r, "", " ")
		fmt.Fprintln(c.out, string(b))
		return nil
	}
	b, err := me.SavingsPDF(r)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*pdf, b, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "wrote "+*pdf)
	return nil
}

func (c *calibrator) baseline(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate baseline", flag.ContinueOnError)
	mode := fs.String("mode", "both", "alone, with or both")
	model := fs.String("model", "opus", "the Claude model, as claude --model takes it")
	maxUSD := fs.Float64("max-usd-per-run", 2, "claude --max-budget-usd for each run")
	timeout := fs.Duration("timeout", 10*time.Minute, "a run that takes longer counts as a wrong answer")
	kinds := fs.String("use-case", "", "comma-separated use cases (default: every built-in kind)")
	user := fs.Bool("include-user", false, "also run your own samples")
	dry := fs.Bool("dry-run", false, "print the commands without running Claude")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := map[string][]string{"alone": {calibrate.ModeAlone}, "with": {calibrate.ModeWith}, "both": {calibrate.ModeAlone, calibrate.ModeWith}}[*mode]
	if modes == nil {
		return errors.New("--mode is alone, with or both")
	}
	all, err := c.samples()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	var ss []calibrate.Sample
	for _, s := range all {
		if (s.Builtin || *user) && (len(want) == 0 || want[s.UseCase]) {
			ss = append(ss, s)
		}
	}
	if len(ss) == 0 {
		return errors.New("no samples match")
	}
	if *dry {
		for _, s := range ss {
			for _, m := range modes {
				fmt.Fprintf(c.out, "%s %s: claude %s\n", s.ID, m, shellQuote(calibrate.ClaudeArgs(m, *model, calibrate.ClaudePrompt(s), *maxUSD)))
			}
		}
		return nil
	}
	work, err := os.MkdirTemp("", "p0ssess-baseline-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	o := calibrate.BaselineOptions{Modes: modes, Model: *model, MaxUSD: *maxUSD, Timeout: *timeout, Work: work, Now: c.now,
		Spent: func(session string, from, to time.Time) float64 { return ledgerSpend(c.d, session, from, to) },
		Reference: func() (calibrate.Reading, bool) {
			us, err := ustate.Load(c.d.UsageFile())
			if err != nil || us.RateLimits == nil || us.RateLimits.SevenDay == nil || us.RateLimits.SevenDay.UsedPercentage == nil || us.RateLimits.SevenDay.ResetsAt == nil {
				return calibrate.Reading{}, false
			}
			return calibrate.Reading{Used: *us.RateLimits.SevenDay.UsedPercentage / 100, ResetsAt: *us.RateLimits.SevenDay.ResetsAt, At: us.ReadingAt}, true
		}}
	fmt.Fprintf(c.out, "baseline     %d samples x %s on Claude %s; this uses your Claude limits, one run at a time\n", len(ss), strings.Join(modes, "+"), *model)
	bs, err := calibrate.LoadBaselines(c.d)
	if err != nil {
		return err
	}
	runs := calibrate.RunBaselines(ctx, ss, o, c.claude, func(r calibrate.BaselineRun) {
		fmt.Fprintln(c.out, "  "+baselineLine(r))
	})
	bs.Merge(runs)
	if err := calibrate.SaveBaselines(c.d, bs); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "saved        the savings report on the local page now uses these runs (p0ssess calibrate report --pdf FILE exports it)")
	return nil
}

func baselineLine(r calibrate.BaselineRun) string {
	verdict := "wrong"
	if r.Correct {
		verdict = "right"
	}
	if r.Error != "" {
		verdict = "failed: " + r.Error
	}
	line := fmt.Sprintf("%-26s %-6s %-6s %5.0fs", r.UseCase, r.Mode, verdict, r.Seconds)
	if usd, ok := r.TotalUSD(); ok {
		line += fmt.Sprintf("  $%.4f", usd)
	}
	if r.WeeklyShare != nil {
		line += fmt.Sprintf("  %.2f%% of the weekly limit", *r.WeeklyShare*100)
	}
	return line
}

// ledgerSpend sums what p0ssess recorded for a Claude session, or, when the session is unknown, for any
// session between from and to.
func ledgerSpend(d paths.Dirs, session string, from, to time.Time) float64 {
	es, err := history.Read(d.HistoryFile())
	if err != nil {
		return 0
	}
	sum := 0.0
	for _, e := range es {
		if e.Type != history.TypeRun {
			continue
		}
		if (session != "" && e.Session == session) || (session == "" && !e.TS.Before(from) && !e.TS.After(to)) {
			sum += e.CashUSD
		}
	}
	return sum
}

// runClaude runs the claude CLI headless. Claude Code's own session variables are left out, so a run
// started from inside a session is a session of its own.
func runClaude(ctx context.Context, dir string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") || k == "CLAUDE_PROJECT_DIR" {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 5 * time.Second
	return cmd.Output()
}

func shellQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?(){}[];&|<>!#~") {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}
