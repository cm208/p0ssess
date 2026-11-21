package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/pme/hook"
)

// DirectTask is read-only work p0ssess answers with one direct call instead of a sandboxed session:
// work that needs current web data, or names no repository.
type DirectTask struct {
	Task       string
	Files      []string
	Web        bool
	Model      string
	UseCase    string
	Stakes     int
	MaxCostUSD float64
}

// DirectRunner runs a DirectTask under the user's limits; the plugin's server provides it.
type DirectRunner func(ctx context.Context, t DirectTask) (*Result, error)

// SetDirect sets the direct path Do uses.
func (e *Engine) SetDirect(d DirectRunner) { e.direct = d }

// SetReceipt sets how a finished run's receipt line is written.
func (e *Engine) SetReceipt(f func(*Result) string) { e.receipt = f }

// FailureHint follows every error p0ssess returns, so the main conversation routes the work again
// instead of doing it itself.
const FailureHint = " (p0ssess: tell the user this in one line. Once it is fixed, send the request to the p0ssess agent again; " +
	"do not do the task in the main conversation unless the user says so.)"

const maxAttempts = 4

type failure int

const (
	failNone failure = iota
	failImage
	failModel
	failAfford
	failCredits
	failKey
	failStorage
	failProvider
	failApproval
	failOther
)

var (
	modelGoneRe = regexp.MustCompile(`(?i)model (is )?unavailable|model not found|unknown model|not a valid model id|no endpoints found|no allowed providers`)
	affordRe    = regexp.MustCompile(`(?i)can only afford (\d+)`)
	creditsRe   = regexp.MustCompile(`(?i)insufficient (credits|balance)|out of credits|requires more credits|key limit exceeded|credit limit`)
	storageRe   = regexp.MustCompile(`(?i)creating container storage|container storage|layer not known|storage driver|overlay[^\n]{0,80}(no such file|invalid argument|dangling)`)
	keyRe       = regexp.MustCompile(`(?i)(http|status|code|error)[ :"]*401\b|user not found|invalid api key|no auth credentials`)
	providerRe  = regexp.MustCompile(`(?i)\b(429|502|503|504)\b|rate.?limit|provider returned error|upstream|overloaded|timed? ?out`)
)

// classify reads what went wrong from an error or a failed result's error text.
func classify(err error, res *Result) failure {
	text := ""
	if err != nil {
		var mi *jail.MissingImage
		if errors.As(err, &mi) {
			return failImage
		}
		text = err.Error()
	} else if res != nil && res.Status == "failed" {
		text = res.Error
	} else {
		return failNone
	}
	switch {
	case strings.Contains(text, "needs approval"):
		return failApproval
	case storageRe.MatchString(text):
		return failStorage
	case affordRe.MatchString(text):
		return failAfford
	case creditsRe.MatchString(text):
		return failCredits
	case keyRe.MatchString(text):
		return failKey
	case modelGoneRe.MatchString(text):
		return failModel
	case providerRe.MatchString(text):
		return failProvider
	}
	return failOther
}

// storageLine is the one thing the user must do about damaged podman storage. --repair is only named
// with what it deletes.
const storageLine = "The sandbox's container storage is damaged: podman could not create container storage. " +
	"Run `p0ssess jail build` to rebuild the sandbox image. If that fails too, `podman system check --repair` repairs the storage, " +
	"but it deletes every image no container is using, the sandbox image included; run `p0ssess jail build` after it."

const keyLine = "OpenRouter did not accept your API key. Put a working key in your p0ssess key file (/p0ssess:p0ssess-me shows where), then ask again."

const creditsLine = "Your OpenRouter balance is too low for this run. Add credits at https://openrouter.ai/settings/credits, then ask again."

// rawRe marks error text that is plumbing, not a sentence for the user.
var rawRe = regexp.MustCompile(`(?i)podman |jail: |git [a-z-]+: |opencode|http \d{3}|status \d{3}|dial tcp|connection refused|server log|server did not come up|egress|nft|exit status|context deadline|EOF|panic`)

// Plain turns an error into the one plain sentence the user sees, keeping limits, approvals and
// refusals (written for the user already) as they are. Anything raw goes to the error log, and the
// sentence says where.
func Plain(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if strings.Contains(text, FailureHint) {
		return err
	}
	var msg string
	switch classify(err, nil) {
	case failApproval:
		return err
	case failStorage:
		msg = storageLine
	case failCredits:
		msg = creditsLine
	case failKey:
		msg = keyLine
	case failImage:
		msg = "The sandbox image is missing and could not be built; run `p0ssess jail build` and check what it says."
	default:
		switch {
		case strings.Contains(text, "cannot read the OpenRouter key file") || strings.Contains(text, "OpenRouter key file is empty"):
			msg = "p0ssess has no OpenRouter key yet: run /p0ssess:p0ssess-me to add one."
		case strings.Contains(text, "podman is not installed"):
			msg = "The agent path needs rootless podman, which is not installed on this machine."
		case rawRe.MatchString(text):
			msg = "p0ssess could not finish this run (" + failureKind(text) + "); the details are in " + logError(text) + "."
		default:
			msg = strings.TrimSpace(text)
			if len(msg) > 400 {
				msg, _ = clip(msg, 400, "…")
			}
		}
	}
	return errors.New(msg + FailureHint)
}

func failureKind(text string) string {
	switch {
	case strings.Contains(text, "podman") || strings.Contains(text, "jail"):
		return "the sandbox did not start"
	case providerRe.MatchString(text):
		return "the model's provider failed"
	case strings.Contains(strings.ToLower(text), "opencode"):
		return "the sandboxed agent failed"
	}
	return "an internal error"
}

var lastLogged string

// logError appends raw error text to the relay's error log (once, when the same text comes back from
// two layers) and returns its path.
func logError(text string) string {
	p := config.RelayPath("errors.log")
	if text == lastLogged {
		return p
	}
	lastLogged = text
	os.MkdirAll(filepath.Dir(p), 0o700)
	if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), strings.ReplaceAll(text, "\n", " | "))
		f.Close()
	}
	return p
}

// runOnce is one attempt; tests replace it through RunHook.
func (e *Engine) runOnce(ctx context.Context, req RunRequest, progress func(float64, string)) (*Result, error) {
	if e.RunHook != nil {
		return e.RunHook(ctx, req)
	}
	return e.Run(ctx, req, progress)
}

// discard ends a failed attempt's session so a retry starts clean.
func (e *Engine) discard(res *Result) {
	if res != nil && res.SessionID != "" {
		e.closeSession(res.SessionID)
	}
}

// Do runs a task the way the caller means it: it picks where the task works, sends read-only work that
// needs the web or names no repository to the direct path, retries what p0ssess can fix by itself
// (another model, a smaller output limit, a rebuilt sandbox image, a pass that changed nothing), checks
// what an edit run changed, runs the project's tests on it and applies it to a clean checkout. It
// returns the result with a short retry log, or one plain sentence.
func (e *Engine) Do(ctx context.Context, req RunRequest, progress func(float64, string)) (*Result, error) {
	res, err := e.do(ctx, req, progress)
	if res != nil {
		if res.Status == "failed" && res.Error != "" {
			res.Error = Plain(errors.New(res.Error)).Error()
		}
		e.setReceipt(res)
	}
	return res, Plain(err)
}

// setReceipt gives a result its receipt line; a failed run that spent nothing gets none.
func (e *Engine) setReceipt(res *Result) {
	switch {
	case res.Status == "failed" && res.CostUSD == 0:
		res.Receipt = ""
	case e.receipt != nil:
		res.Receipt = e.receipt(res)
	default:
		res.Receipt = DefaultReceipt(res)
	}
}

func (e *Engine) do(ctx context.Context, req RunRequest, progress func(float64, string)) (*Result, error) {
	if req.Mode == "" {
		req.Mode = "read"
	}
	if req.Session != "" {
		return e.runOnce(ctx, req, progress)
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = defaultCwd()
	}
	tg := Resolve(req.Task, cwd, req.Cwd != "", e.cfg.OpenRouterKeyFile, req.Attachments)
	req.target = &tg
	if tg.Repo != "" {
		req.Cwd = tg.Cwd
	}
	var log []string
	current := hook.NeedsCurrentInfo(req.Task)
	spent := 0.0
	var noSandbox error
	if e.SandboxCheck != nil {
		noSandbox = e.SandboxCheck()
	}
	if noSandbox != nil && req.Mode != "read" {
		return nil, fmt.Errorf("edit work runs in the sandboxed agent path, which is unavailable here: %v", noSandbox)
	}
	if noSandbox != nil {
		for _, p := range taskPaths(req.Task) {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				tg.Files = addUnique(tg.Files, p)
			}
		}
	}
	if req.Mode == "read" && (current && !tg.Named || tg.Repo == "" || noSandbox != nil) {
		if e.direct == nil {
			if current {
				return nil, errors.New("this task needs current information from the web, and the web search route is not available here, so nothing was run")
			}
		} else {
			files := append(append([]string{}, req.Attachments...), tg.Files...)
			res, err := e.direct(ctx, DirectTask{Task: req.Task, Files: files, Web: current, Model: req.Model, UseCase: req.UseCase,
				Stakes: req.Stakes, MaxCostUSD: req.MaxCostUSD})
			if res != nil {
				res.Route = "direct"
				if current {
					res.Retries = append(res.Retries, "answered with web search on the direct path")
				} else {
					res.Retries = append(res.Retries, append(tg.Notes, "no repository is needed, so it ran on the direct path")...)
				}
			}
			if err != nil && current {
				return res, fmt.Errorf("this task needs current information from the web, and no web-capable route fits: %w", err)
			}
			return res, err
		}
	}
	if current {
		if e.direct == nil {
			return nil, errors.New("this task needs current information from the web, which the sandbox cannot reach, and the web search route is not available here, so nothing was run")
		}
		res, err := e.direct(ctx, DirectTask{Task: "Find the current facts this task needs, with sources and dates. Answer with the facts only.\n\n" + req.Task,
			Web: true, Stakes: req.Stakes})
		if err != nil {
			return nil, fmt.Errorf("this task needs current information from the web, which the sandbox cannot reach, and no web-capable route fits: %w", err)
		}
		req.Task += "\n\nCurrent information p0ssess gathered from the web for this task (" + res.Model + "):\n" + res.Answer
		spent += res.CostUSD
		log = append(log, fmt.Sprintf("gathered current web data first (%s, $%.4f)", shortModel(res.Model), res.CostUSD))
	}

	picked := strings.TrimSpace(req.Model) == ""
	chosen, err := e.chooseModel(&req)
	if err != nil {
		return nil, err
	}
	tried := []string{BaseID(req.Model)}
	next := func() string {
		alts := req.Alternatives
		if !picked && len(alts) == 0 {
			// The caller named the model; the engine's own pick is the next option.
			alt := req
			alt.Model = ""
			if _, err := e.chooseModel(&alt); err == nil && alt.Model != "" {
				alts = append([]string{alt.Model}, alt.Alternatives...)
				req.Alternatives = alts
			}
		}
		for _, a := range alts {
			if !containsID(tried, a) {
				tried = append(tried, BaseID(a))
				return a
			}
		}
		return ""
	}
	built := false
	var res *Result
	for attempt := 0; attempt < maxAttempts; attempt++ {
		res, err = e.runOnce(ctx, req, progress)
		res, log, err = e.unprefix(ctx, req.WaitS, res, err, log, progress)
		kind := classify(err, res)
		if kind == failNone {
			break
		}
		if res != nil {
			spent += res.CostUSD
		}
		retry := ""
		switch kind {
		case failImage:
			if !built && e.BuildImage != nil {
				built = true
				if berr := e.BuildImage(ctx); berr == nil {
					retry = "the sandbox image was missing, so p0ssess built it"
				}
			}
		case failModel, failProvider:
			if alt := next(); alt != "" {
				why := "is unavailable in the sandbox"
				if kind == failProvider {
					why = "'s provider failed"
				}
				retry = fmt.Sprintf("%s %s; switched to %s", shortModel(req.Model), why, shortModel(alt))
				req.Model = alt
			}
		case failAfford:
			text := errText(err, res)
			if m := affordRe.FindStringSubmatch(text); m != nil {
				n, _ := strconv.Atoi(m[1])
				if n >= 2048 && (req.MaxOutput == 0 || n*9/10 < req.MaxOutput) {
					req.MaxOutput = n * 9 / 10
					retry = fmt.Sprintf("OpenRouter's key limit allowed %d output tokens; retried with a smaller output limit", n)
				} else if alt := next(); alt != "" {
					retry = fmt.Sprintf("OpenRouter's key limit is too low for %s; switched to %s", shortModel(req.Model), shortModel(alt))
					req.Model = alt
				}
			}
		case failApproval:
			if picked {
				if alt := next(); alt != "" {
					retry = fmt.Sprintf("%s needs approval above your limit; used %s inside it", shortModel(req.Model), shortModel(alt))
					req.Model = alt
				}
			}
		}
		if retry == "" {
			break
		}
		log = append(log, retry)
		e.discard(res)
	}
	if res == nil {
		return nil, err
	}
	if chosen != "" {
		res.Chosen = chosen
	}
	if err == nil && res.Status == "done" && req.Mode == "edit" {
		res, log = e.afterEdit(ctx, req, res, log, progress)
	}
	if rec, lerr := loadRecord(res.SessionID); lerr == nil {
		res.Retries = append(append(rec.Log, log...), res.Retries...)
	} else {
		res.Retries = append(log, res.Retries...)
	}
	if spent > 0 {
		res.Retries = append(res.Retries, fmt.Sprintf("earlier attempts cost $%.4f", spent))
	}
	return res, err
}

// unprefixNote is what a model hears when it asked to run an allowed command behind VAR=value
// assignments, which the session rules cannot tell from any other command.
const unprefixNote = "Not run: commands with a VAR=value prefix ask the user. The environment is already set up; " +
	"run the same command without %s and it runs without asking."

// unprefix answers a permission request that only a VAR=value prefix raised (gate run of 2026-11-21:
// "GOPROXY=off GOFLAGS=-mod=mod go test ./..."): the model is told to drop it, up to three times, so the
// user is not asked.
func (e *Engine) unprefix(ctx context.Context, waitS int, res *Result, err error, log []string,
	progress func(float64, string)) (*Result, []string, error) {
	for i := 0; i < 3 && err == nil && res != nil && res.Status == "needs_permission" && res.Permission != nil &&
		res.Permission.Action == "shell"; i++ {
		rec, lerr := loadRecord(res.SessionID)
		if lerr != nil {
			break
		}
		prefix, ok := plainCommand(rec.Mode, rec.ShellInternet, res.Permission.Resources)
		if !ok {
			break
		}
		res, err = e.Answer(ctx, res.SessionID, res.Permission.RequestID, "reject", fmt.Sprintf(unprefixNote, prefix), waitS, progress)
		if i == 0 {
			log = append(log, "the model asked to run a command behind "+prefix+"; p0ssess told it to run it plainly")
		}
	}
	return res, log, err
}

// Settle finishes a result that wait or answer_permission returned: an edit run that has just ended is
// checked like one Do waited on, and errors become plain sentences.
func (e *Engine) Settle(ctx context.Context, res *Result, err error, progress func(float64, string)) (*Result, error) {
	var healed []string
	if res, healed, err = e.unprefix(ctx, 60, res, err, nil, progress); res != nil {
		res.Retries = append(res.Retries, healed...)
	}
	checked := false
	if err == nil && res != nil && res.Status == "done" && res.Mode == "edit" && res.Route == "" {
		if rec, lerr := loadRecord(res.SessionID); lerr == nil && !rec.Checked {
			checked = true
		}
		var log []string
		res, log = e.afterEdit(ctx, RunRequest{}, res, log, progress)
		res.Retries = append(res.Retries, log...)
	}
	if res != nil {
		if res.Status == "failed" && res.Error != "" {
			res.Error = Plain(errors.New(res.Error)).Error()
		}
		// A result Do already gave its receipt (and recorded it) keeps it.
		if res.Receipt == "" || checked {
			e.setReceipt(res)
		}
	}
	return res, Plain(err)
}

func errText(err error, res *Result) string {
	if err != nil {
		return err.Error()
	}
	if res != nil {
		return res.Error
	}
	return ""
}

func containsID(ids []string, m string) bool {
	for _, id := range ids {
		if id == BaseID(m) {
			return true
		}
	}
	return false
}

func shortModel(m string) string {
	m = BaseID(m)
	if _, after, ok := strings.Cut(m, "/"); ok {
		return after
	}
	return m
}

// scopeRetry is the second prompt for an edit run whose first pass changed nothing.
const scopeRetry = "Nothing changed in your copy, but this task asks for changes. Make them now: edit the files in %s " +
	"(the paths in the task point into it) with your tools, then finish with the same short report."

// afterEdit checks a finished edit run: one more pass when nothing changed, then the project's tests on
// the changed copy, then the changes applied to the caller's checkout when it is clean.
func (e *Engine) afterEdit(ctx context.Context, req RunRequest, res *Result, log []string, progress func(float64, string)) (*Result, []string) {
	rec, err := loadRecord(res.SessionID)
	if err != nil || rec.Checked {
		return res, log
	}
	if res.Changed == "no changes" {
		follow := RunRequest{Task: fmt.Sprintf(scopeRetry, rec.Location), Session: res.SessionID, Mode: "edit", WaitS: req.WaitS, MaxCostUSD: req.MaxCostUSD}
		if r2, err := e.runOnce(ctx, follow, progress); err == nil && r2 != nil && r2.Status == "done" {
			log = append(log, "the first pass changed nothing, so p0ssess asked once more")
			r2.CostUSD += res.CostUSD
			res = r2
		}
		if res.Changed == "no changes" {
			res.Note = strings.TrimSpace(res.Note + " the run changed no files although the task asked for changes")
			return res, log
		}
	}
	testsOK := true
	if e.cfg.VerifyTests != "off" && !rec.Scratch && e.TestRunner != nil {
		var lines []string
		for _, cd := range res.copies {
			found, _ := jail.Toolchains(cd.repo)
			for _, tc := range found {
				if tc.Test == "" {
					continue
				}
				tr := e.TestRunner(ctx, cd.wt, tc, rec.Toolchains)
				line := ""
				switch {
				case tr.Skipped != "":
					line = "not run (" + tr.Skipped + ")"
				case tr.Passed:
					line = "passed"
				default:
					testsOK = false
					line = "failed: " + lastLines(tr.Tail, 4)
				}
				lines = append(lines, tc.Test+" "+line)
			}
		}
		res.Tests = strings.Join(lines, "; ")
	}
	e.recMu.Lock()
	if cur, err := loadRecord(res.SessionID); err == nil {
		rec = cur
	}
	rec.Checked = true
	rec.save()
	e.recMu.Unlock()
	if e.cfg.ApplyEdits == "never" || len(res.copies) == 0 {
		return res, log
	}
	if rec.Scratch {
		applied, reverts, keep, notes := applyScratch(rec, res.copies[0].wt)
		res.Applied = append(res.Applied, applied...)
		res.Apply, res.Revert = strings.Join(keep, " && "), strings.Join(reverts, " && ")
		log = append(log, notes...)
		saveFinal(res)
		return res, log
	}
	var keep, reverts []string
	for _, cd := range res.copies {
		f, a, d := statCounts(cd.stat)
		why := ""
		switch {
		case !testsOK:
			why = "the tests failed"
		case cd.carried > 0:
			why = "the checkout had uncommitted changes"
		default:
			why = applyToCaller(cd.repo, cd.head, cd.patch)
		}
		if why == "" {
			res.Applied = append(res.Applied, fmt.Sprintf("%s (%d file(s), +%d -%d)", cd.repo, f, a, d))
			reverts = append(reverts, "git -C "+shellQuote(cd.repo)+" apply -R "+shellQuote(cd.patch))
			continue
		}
		log = append(log, "not applied to "+cd.repo+": "+why)
		keep = append(keep, "git -C "+shellQuote(cd.repo)+" apply "+shellQuote(cd.patch))
	}
	res.Apply = strings.Join(keep, " && ")
	res.Revert = strings.Join(reverts, " && ")
	saveFinal(res)
	return res, log
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " | ")
}

// DefaultReceipt is the receipt line without a savings estimate.
func DefaultReceipt(r *Result) string {
	if r == nil || r.Model == "" {
		return ""
	}
	cost := fmt.Sprintf("$%.2f", r.CostUSD)
	if r.CostUSD > 0 && r.CostUSD < 0.01 {
		cost = fmt.Sprintf("$%.4f", r.CostUSD)
	}
	if r.CostUSD > 0 && r.CostUSD < 0.00005 {
		cost = "under $0.0001"
	}
	if r.CostEstimated {
		cost = "about " + cost
	}
	return "(" + shortModel(r.Model) + ", " + cost + ")"
}
