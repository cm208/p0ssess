// Package server is the plugin's one MCP server: the relay tools (run, wait, abort, answer_permission,
// models), the direct tool and the engine tools on one stdio connection. It reports relay and direct runs
// to the engine, so the status line's running count and the budget's spend are measured, not reported.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/calibrate"
	"p0ssess/internal/config"
	"p0ssess/internal/costbasis"
	"p0ssess/internal/direct"
	"p0ssess/internal/history"
	"p0ssess/internal/jail"
	"p0ssess/internal/mcp"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/mcpserver"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/relay"
	"p0ssess/internal/usage"
)

// Name is the MCP server name; inside the plugin its tools are mcp__plugin_p0ssess_p0ssess__<tool>.
const Name = "p0ssess"

const instructions = `p0ssess hands work to other models through OpenRouter while Claude stays in charge.
Routed work runs in the p0ssess agent (subagent_type "p0ssess:p0ssess", on Haiku): from the main conversation, hand the request to it and reply with its result. The main conversation never calls direct, run, wait, abort, answer_permission, recommend or models itself unless the user asks for that.
- direct: one call with local files (video, audio, images to make or edit). run/wait/abort/answer_permission/models: a sandboxed OpenCode session for work on files. recommend: prices routes.
- budget_status, rules_*, approvals_*, settings_set, history_record_outcome, tendencies, shortcuts_*: the user's limits and history, used from the main conversation.
`

// Options configure New.
type Options struct {
	Version string
	Log     io.Writer
	// Agent reports why the agent path cannot run here, or nil when it can.
	Agent func() error
}

// loadEngine is swapped in tests.
var loadEngine = local.Load

// New builds the server. close stops background work and interrupts active relay runs.
func New(opt Options) (s *mcp.Server, closeFn func(), err error) {
	if opt.Log == nil {
		opt.Log = os.Stderr
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if os.Getenv("P0SSESS_REGISTRY") == "" {
		if p, err := cfg.RegistryPath(); err == nil {
			os.Setenv("P0SSESS_REGISTRY", p)
		}
	}
	eng, err := loadEngine(enginePaths())
	if err != nil {
		return nil, nil, err
	}
	if err := relay.Migrate(opt.Log); err != nil {
		fmt.Fprintln(opt.Log, "p0ssess: moving relay state:", err)
	}
	obs := NewObserver(eng).(*observer)
	re := relay.NewEngine(cfg, opt.Version, opt.Log)
	re.SetObserver(obs)
	re.SetPricer(Pricer(eng))
	s = relay.NewServer(re, opt.Version)
	s.Name = Name
	s.Instructions = instructions + "\nEngine:\n" + mcpserver.Instructions

	agentCheck := opt.Agent
	if agentCheck == nil {
		agentCheck = AgentAvailable
	}
	for _, name := range []string{"run", "wait", "abort", "answer_permission"} {
		s.Wrap(name, func(next mcp.Handler) mcp.Handler {
			return func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
				if err := agentCheck(); err != nil {
					return mcp.Error("the agent path is unavailable here: " + err.Error() + ". The direct tool and the engine still work.")
				}
				return next(ctx, args, p)
			}
		})
	}

	d := &directTool{cfg: cfg, eng: eng}
	s.AddTool(mcp.Tool{Name: direct.ToolName, Description: direct.ToolDescription, InputSchema: d.schema()}, d.call)

	es := &mcpserver.Server{E: eng}
	for _, t := range mcpserver.Tools() {
		name, _ := t["name"].(string)
		desc, _ := t["description"].(string)
		schema, _ := json.Marshal(t["inputSchema"])
		tool := mcp.Tool{Name: name, Description: desc, InputSchema: schema}
		if readOnly[name] {
			tool.Annotations = map[string]any{"readOnlyHint": true}
		}
		s.AddTool(tool, func(ctx context.Context, args json.RawMessage, _ mcp.Progress) mcp.CallResult {
			out, err := es.Call(name, args)
			if err != nil {
				return mcp.Error(err.Error())
			}
			b, _ := json.MarshalIndent(out, "", " ")
			return mcp.Text(string(b))
		})
	}
	addShortcutTools(s, eng)

	refreshCtx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-refreshCtx.Done():
		case <-time.After(2 * time.Second):
			mcpserver.BackgroundRefresh(eng)()
		}
	}()
	go func() {
		select {
		case <-refreshCtx.Done():
		case <-time.After(5 * time.Second):
			Reconcile(refreshCtx, cfg, eng)
		}
	}()
	go func() {
		select {
		case <-refreshCtx.Done():
		case <-time.After(10 * time.Second):
			ctx, cancel := context.WithTimeout(refreshCtx, 30*time.Second)
			eng.SendOutcomes(ctx)
			d := eng.Dirs
			calibrate.SendPending(ctx, d, account.Current(d).Pro(), remote.New(d).PutCalibration)
			cancel()
		}
	}()
	if agentCheck() == nil {
		// Jails left by a p0ssess process that crashed or was killed.
		go re.Sweep(refreshCtx)
	}
	return s, func() { cancel(); re.Shutdown(); obs.finishAll() }, nil
}

var readOnly = map[string]bool{"recommend": true, "budget_status": true, "rules_list": true, "approvals_list": true,
	"tendencies": true, "shortcuts_suggest": true}

// Serve runs the server on stdio until stdin closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, version string) error {
	s, closeFn, err := New(Options{Version: version})
	if err != nil {
		return err
	}
	defer closeFn()
	err = s.Serve(ctx, in, out)
	if err == io.EOF || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// AgentAvailable reports whether the jailed OpenCode path can run: Linux with podman and nft, or the
// cloud sandbox for an install linked to an account.
func AgentAvailable() error {
	err := localAgentAvailable()
	if err != nil {
		if cfg, cerr := config.Load(); cerr == nil && jail.CloudReady(cfg) {
			return nil
		}
	}
	return err
}

func localAgentAvailable() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("it needs Linux with rootless podman (this is %s)", runtime.GOOS)
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return errors.New("podman is not installed")
	}
	return nil
}

// NewObserver returns the relay observer that enforces the user's limits before each prompt and records
// its cost after. The command line's relay commands use it too.
func NewObserver(eng *local.Engine) relay.Observer {
	return &observer{eng: eng, jobs: map[string]string{}}
}

// observer authorizes relay runs, turns them into running-job files and records their cost.
type observer struct {
	eng  *local.Engine
	mu   sync.Mutex
	jobs map[string]string // session id -> job id
}

// Authorize applies every limit to one relay prompt and reserves its cost cap.
func (o *observer) Authorize(a relay.RunAuth) (relay.Grant, error) {
	// The default ceiling (max_run_cost_usd) may shrink to the room left; a caller's own maximum is kept.
	g, err := o.eng.Authorize(local.Charge{Route: "agent", Model: a.Model, UseCase: a.UseCase, Stakes: a.Stakes,
		MaxUSD: a.MaxCostUSD, ExplicitMax: a.ExplicitMax, Fit: !a.ExplicitMax, Edit: a.Mode == "edit",
		Hold: a.MaxRuntime + 15*time.Minute, Scope: local.Scope{Cwd: a.Cwd}})
	if err != nil {
		return relay.Grant{}, err
	}
	// Shell internet comes only from the user's own budget.toml; a failed read leaves it off.
	internet := false
	if b, err := o.eng.Budget(); err == nil {
		internet = b.Sandbox.ShellInternetFor(a.Repo)
	}
	return relay.Grant{ReserveID: g.ReserveID, CapUSD: g.MaxUSD, ApprovalID: g.ApprovalID, ZDR: g.ZDR, Session: g.Session,
		PriceUnknown: g.PriceUnknown, ShellInternet: internet, Note: strings.Join(g.Notes, "; ")}, nil
}

func (o *observer) Release(g relay.Grant) {
	o.eng.Release(&local.Grant{ReserveID: g.ReserveID})
}

func (o *observer) RunStarted(r relay.RunInfo) {
	j, err := usage.StartJob(o.eng.Dirs.RunningDir(), r.Model, r.Kind, os.Getpid())
	if err != nil {
		return
	}
	o.mu.Lock()
	if old, ok := o.jobs[r.SessionID]; ok {
		usage.FinishJob(o.eng.Dirs.RunningDir(), old)
	}
	o.jobs[r.SessionID] = j.ID
	o.mu.Unlock()
}

func (o *observer) RunFinished(r relay.RunInfo, res *relay.Result) error {
	o.mu.Lock()
	if id, ok := o.jobs[r.SessionID]; ok {
		usage.FinishJob(o.eng.Dirs.RunningDir(), id)
		delete(o.jobs, r.SessionID)
	}
	o.mu.Unlock()
	run := local.Run{ID: fmt.Sprintf("%s/%d", r.SessionID, r.Turn), Route: "agent", Model: r.Model,
		CashUSD: res.CostUSD, Status: res.Status, UseCase: r.UseCase, Cwd: r.Repo}
	switch {
	case res.CostEstimated || res.CostBasis == relay.CostCap:
		// The run's cost could not be measured or priced: count it at its cap until a measured figure exists.
		run.CashUSD, run.Estimated = max(res.CostUSD, r.Grant.CapUSD), true
	case res.CostBasis == relay.CostTokens:
		// Priced from tokens, not measured.
		run.Estimated = true
	case r.Grant.PriceUnknown && res.CostUSD <= 0 && (res.Tokens == nil || res.Tokens.Input+res.Tokens.Output+res.Tokens.Reasoning > 0):
		// No price is known and nothing measured a cost: a $0 figure for tokens that were billed is not
		// trusted.
		run.CashUSD, run.Estimated = r.Grant.CapUSD, true
		res.CostEstimated, res.CostUSD, res.CostBasis = true, r.Grant.CapUSD, relay.CostCap
	}
	run.CostBasis = relay.LedgerBasis(res.CostBasis)
	return o.eng.Settle(&local.Grant{ReserveID: r.Grant.ReserveID, ApprovalID: r.Grant.ApprovalID, Session: r.Grant.Session}, run)
}

// Pricer prices relay runs whose cost OpenRouter did not report, at the route table's prices.
func Pricer(eng *local.Engine) relay.Pricer {
	return func(model string, t opencode.Tokens) (float64, bool) {
		return eng.TokenCost(model, t.Input, t.Output+t.Reasoning, t.Cache.Read, t.Cache.Write)
	}
}

func (o *observer) finishAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for sid, id := range o.jobs {
		usage.FinishJob(o.eng.Dirs.RunningDir(), id)
		delete(o.jobs, sid)
	}
}

// DirectHold is how long a direct call's reservation holds: longer than any call can run.
const DirectHold = 2 * time.Hour

// newClient makes the OpenRouter client; tests replace it.
var newClient = func(key string) *direct.Client { return direct.New(key) }

// DirectJob is what the direct path knows about a call beyond the request itself.
type DirectJob struct {
	OfferID string
	UseCase string
	Stakes  int
	// Route is the ledger route; "" is "direct". Calibration runs use "calibrate", so they count as
	// spend but never as accepted work in the history or the outcome records.
	Route string
	// Confirmed passes the user's yes for a whole job to the guard (local.Charge.Confirmed).
	Confirmed bool
	// AwaitOutcome holds the run out of the outcome records until history_record_outcome names it.
	AwaitOutcome bool
}

// RunDirect runs one direct-path job under every limit. With no model it picks one for the use case
// (pickModels). The estimate decides whether the user is asked; the ceiling is the caller's
// max_cost_usd, else the estimate. Authorize checks the rules, lists, stakes, privacy, per-task, daily
// and monthly limits and reserves the ceiling; the measured cost settles the reservation, or, when the
// cost is not known, the reserved maximum is recorded as an estimate. When p0ssess picked the model and
// its provider fails (a timeout or server error OpenRouter did not bill), the next-ranked option gets
// the unanswered part once, as a call of its own under the same limits. runID is empty for dry runs and
// calls that were never sent.
func RunDirect(ctx context.Context, cfg config.Config, eng *local.Engine, req direct.Request, job DirectJob) (*direct.Result, string, error) {
	cwd, _ := os.Getwd()
	if v := os.Getenv("CLAUDE_PROJECT_DIR"); v != "" {
		cwd = v
	}
	if paths.Private(cwd) {
		return nil, "", errors.New("refused: this repository has a .p0ssess-private file; delegation is blocked here")
	}
	c := newClient("")
	c.RecordDir = os.Getenv("P0SSESS_DIRECT_RECORD")
	if p, err := cfg.RegistryPath(); err == nil {
		c.Catalog.LoadRegistry(p)
	}
	var alts []string
	picked := req.Model == ""
	if picked {
		ms, err := pickModels(eng, c, req, &job, cwd)
		if err != nil {
			return nil, "", err
		}
		req.Model, alts = ms[0], ms[1:]
	}
	if eng.Table == nil {
	} else if uc := eng.Table.Table().UseCase(job.UseCase); uc != nil && uc.Preprocess == direct.AudioPrepSpeech {
		req.AudioPrep = direct.AudioPrepSpeech
	}
	res, runID, err := runAuthorized(ctx, cfg, eng, c, req, job, cwd)
	if err != nil && !req.DryRun && picked && direct.ProviderFailed(err) && ctx.Err() == nil {
		if len(alts) > 0 {
			res, runID, err = fallback(ctx, cfg, eng, c, req, job, cwd, alts[0], res, runID, err)
		} else if res != nil {
			res.Warnings = append(res.Warnings, "no other model fits this job without asking, so none was tried")
		}
	}
	var pe *direct.PartialError
	if errors.As(err, &pe) && res != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("partial answer: segment %d of %d and any after it are missing (%v)", pe.Failed+1, pe.Total, pe.Err))
	}
	return res, runID, err
}

// fallback sends the part of req that failed to model alt, under every limit, and merges the answers.
// Without a caller's ceiling it runs only if what was spent plus its estimate stays within the per-task
// auto-approve limit; with one, only within what is left of it.
func fallback(ctx context.Context, cfg config.Config, eng *local.Engine, c *direct.Client, req direct.Request, job DirectJob, cwd, alt string,
	res *direct.Result, runID string, cause error) (*direct.Result, string, error) {
	first := req.Model
	spent := 0.0
	if res != nil {
		spent = res.Usage.CostUSD
	}
	next := req
	next.Model = alt
	var pe *direct.PartialError
	partial := errors.As(cause, &pe)
	if partial {
		next.FromSegment = pe.Failed
	}
	skip := func(why string) (*direct.Result, string, error) {
		if res != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s failed; %s was not tried: %s", history.Short(first), history.Short(alt), why))
		}
		return res, runID, cause
	}
	if req.MaxGiven {
		if next.MaxCostUSD = req.MaxCostUSD - spent; next.MaxCostUSD <= 0 {
			return skip("your ceiling is spent")
		}
	} else {
		dry := next
		dry.DryRun = true
		p, err := c.Plan(ctx, dry)
		if err != nil {
			return skip(err.Error())
		}
		bud, err := eng.Budget()
		if err != nil {
			return skip(err.Error())
		}
		if est := p.Estimate(); est.CostUnknown || spent+est.CostUSD > bud.PerTaskUSD+1e-12 {
			return skip(fmt.Sprintf("with it the task would cost about $%.4f, above your $%.2f auto-approve limit", spent+est.CostUSD, bud.PerTaskUSD))
		}
	}
	job.OfferID = ""
	res2, runID2, err2 := runAuthorized(ctx, cfg, eng, c, next, job, cwd)
	if res2 == nil || (err2 != nil && !errors.As(err2, new(*direct.PartialError)) && !res2.Billed()) {
		why := "it was refused"
		if err2 != nil {
			why = err2.Error()
		}
		if res2 != nil {
			why = "it failed too: " + why
		}
		return skip(why)
	}
	if res == nil {
		res = &direct.Result{}
	}
	text := res.Text
	if partial {
		text = strings.TrimSuffix(text, pe.Marker)
	}
	what := "answered"
	if partial {
		what = fmt.Sprintf("answered from segment %d", pe.Failed+1)
	}
	why := cause.Error()
	if partial {
		why = pe.Err.Error()
	}
	merged := *res2
	merged.Text = text + res2.Text
	merged.Usage = sumUsage(res.Usage, res2.Usage)
	merged.IDs = append(append([]string{}, res.IDs...), res2.IDs...)
	merged.Sent, merged.Rejected = res.Sent+res2.Sent, res.Rejected+res2.Rejected
	merged.Estimate.CostUSD += res.Estimate.CostUSD
	merged.Warnings = append(uniqueAppend(res.Warnings, res2.Warnings...),
		fmt.Sprintf("%s's provider failed (%s); %s %s", history.Short(first), why, history.Short(alt), what))
	ids := runID2
	if runID != "" && runID2 != "" {
		ids = runID + ", " + runID2
	} else if runID != "" {
		ids = runID
	}
	return &merged, ids, err2
}

func sumUsage(a, b direct.Usage) direct.Usage {
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.AudioTokens += b.AudioTokens
	a.VideoTokens += b.VideoTokens
	a.AudioSeconds += b.AudioSeconds
	a.CostUSD += b.CostUSD
	a.OpenRouterUSD += b.OpenRouterUSD
	a.UpstreamUSD += b.UpstreamUSD
	a.BYOK = a.BYOK || b.BYOK
	a.Unmeasured += b.Unmeasured
	a.Priced += b.Priced
	a.CostBasis = costbasis.Combine(a.CostBasis, b.CostBasis)
	if a.CostSource == "" || b.CostSource == "unknown" {
		a.CostSource = b.CostSource
	}
	return a
}

func uniqueAppend(have []string, add ...string) []string {
	out := append([]string{}, have...)
	for _, s := range add {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// runAuthorized is one call: authorize, run, then settle or release.
func runAuthorized(ctx context.Context, cfg config.Config, eng *local.Engine, c *direct.Client, req direct.Request, job DirectJob, cwd string) (*direct.Result, string, error) {
	var grant *local.Grant
	if !req.DryRun {
		dry := req
		dry.DryRun = true
		p, err := c.Plan(ctx, dry)
		if err != nil {
			return nil, "", err
		}
		est := p.Estimate()
		switch {
		case req.MaxGiven && !est.CostUnknown && est.CostUSD > req.MaxCostUSD:
			return nil, "", fmt.Errorf("%w: about $%.4f for %s, limit $%.4f", direct.ErrOverBudget, est.CostUSD, req.Model, req.MaxCostUSD)
		case !req.MaxGiven && est.CostUnknown:
			return nil, "", fmt.Errorf("%w: no price is known for %s, so the cost cannot be estimated; ask the user for a ceiling "+
				"and pass it as max_cost_usd", direct.ErrPriceUnknown, req.Model)
		case !req.MaxGiven:
			req.MaxCostUSD = math.Ceil(est.CostUSD*1e6) / 1e6
		}
		g, err := eng.Authorize(local.Charge{Route: "direct", Model: req.Model, UseCase: job.UseCase, Stakes: job.Stakes,
			MaxUSD: req.MaxCostUSD, ExplicitMax: true, EstimateUSD: est.CostUSD, Hold: DirectHold, Confirmed: job.Confirmed,
			Scope: local.Scope{Cwd: cwd}})
		if err != nil {
			return nil, "", err
		}
		grant = g
		if g.ZDR {
			req.StrictZDR = true
		}
		if c.Key == "" {
			if c.Key, err = cfg.OpenRouterKey(); err != nil {
				eng.Release(grant)
				return nil, "", err
			}
		}
	}
	var j usage.Job
	if !req.DryRun {
		j, _ = usage.StartJob(eng.Dirs.RunningDir(), req.Model, job.UseCase, os.Getpid())
	}
	res, err := c.Run(ctx, req)
	if j.ID != "" {
		usage.FinishJob(eng.Dirs.RunningDir(), j.ID)
	}
	if req.DryRun {
		return res, "", err
	}
	if res == nil || !res.Billed() {
		if rerr := eng.Release(grant); rerr != nil && err == nil {
			err = fmt.Errorf("releasing the reservation: %w", rerr)
		}
		return res, "", err
	}
	key := c.Key
	runID := "d-" + randID()
	status := "done"
	if errors.As(err, new(*direct.PartialError)) {
		status = "partial"
	} else if err != nil {
		status = "failed"
	}
	route := job.Route
	if route == "" {
		route = "direct"
	}
	run := local.Run{ID: runID, Route: route, Model: req.Model, CashUSD: res.Usage.CostUSD,
		UpstreamUSD: res.Usage.UpstreamUSD, Status: status, OfferID: job.OfferID, UseCase: job.UseCase, Cwd: cwd,
		AwaitOutcome: job.AwaitOutcome}
	run.CostBasis = res.Usage.CostBasis
	switch {
	case res.Measured():
	case res.Usage.Unmeasured == 0 && res.Usage.Priced > 0:
		// Priced from the calls' tokens at the model's list price: OpenRouter did not report the cost. The
		// price is recorded as an estimate until the generation records report one.
		run.Estimated = true
		if len(res.IDs) == res.Sent-res.Rejected {
			run.Generations = res.IDs
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("cost not reported; $%.4f (priced from the tokens at the model's list price) is recorded until OpenRouter reports it", run.CashUSD))
	default:
		run.CashUSD, run.Estimated = max(grant.MaxUSD, res.Usage.CostUSD), true
		run.CostBasis = costbasis.Reserved
		// Only a full set of generation ids can replace the estimate later.
		if len(res.IDs) == res.Sent-res.Rejected {
			run.Generations = res.IDs
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("cost not measured; $%.4f (the reserved maximum) is recorded until OpenRouter reports it", run.CashUSD))
	}
	if rerr := eng.Settle(grant, run); rerr != nil {
		res.Warnings = append(res.Warnings, "recording the cost failed: "+rerr.Error()+"; the reservation counts as spent")
	}
	if run.Estimated && len(run.Generations) > 0 {
		go func() {
			time.Sleep(30 * time.Second)
			rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			reconcileRun(rctx, key, eng, runID, run.Generations)
		}()
	}
	return res, runID, err
}

// inferUseCase reads the use case from what a request carries, for a call that names neither a model
// nor a use case. It returns "" when the files do not say.
func inferUseCase(out direct.Output, kinds map[direct.Kind]int) string {
	switch {
	case out == direct.OutImage && kinds[direct.KindImage] > 0:
		return "image-edit"
	case out == direct.OutImage:
		return "image-generate"
	case out == direct.OutTranscript:
		return "audio-transcribe"
	case kinds[direct.KindVideo] > 0:
		return "video-understand"
	case kinds[direct.KindAudio] > 0:
		return "audio-transcribe"
	}
	return ""
}

// pickModels chooses the model for a direct call that names none: the engine's best direct option for
// the use case, sized from the files. It prefers an option that runs without asking when one costs at
// most half again as much as the best. The other direct options that run without asking follow in rank
// order, as fallbacks. The offer is recorded, and job gets its id and the use case.
func pickModels(eng *local.Engine, c *direct.Client, req direct.Request, job *DirectJob, cwd string) ([]string, error) {
	prof := local.Profile{Text: "direct call"}
	kinds := map[direct.Kind]int{}
	for _, f := range req.Files {
		in, err := direct.Inspect(f, c.Limits)
		if err != nil {
			return nil, err
		}
		kinds[in.Kind]++
		switch in.Kind {
		case direct.KindVideo:
			prof.VideoSec += in.Seconds
		case direct.KindAudio:
			prof.AudioSec += in.Seconds
		case direct.KindImage:
			prof.Images++
		case direct.KindPDF:
			prof.Pages += in.Pages
		default:
			prof.InTokens += int(in.Bytes / 4)
		}
	}
	uc := job.UseCase
	if uc == "" {
		uc = inferUseCase(req.Output, kinds)
	}
	if uc == "" {
		return nil, errors.New("name a model, or a use_case (for example video-understand, audio-transcribe, image-generate), so p0ssess can pick one")
	}
	rec, err := eng.Recommend(local.Request{UseCase: uc, Profile: &prof, Stakes: job.Stakes, Record: true, Scope: local.Scope{Cwd: cwd}})
	if err != nil {
		return nil, err
	}
	var best *local.Option
	for i := range rec.Options {
		o := &rec.Options[i]
		if o.Route != local.RouteDirect {
			continue
		}
		switch {
		case best == nil:
			best = o
		case best.NeedsApproval && !o.NeedsApproval && o.Total <= best.Total*1.5:
			best = o
		}
		if !best.NeedsApproval {
			break
		}
	}
	if rec.Pick != nil && rec.Pick.Route == local.RouteDirect && rec.Pick.MixNext {
		best = rec.Pick
	}
	if best == nil {
		why := "no model fits " + uc + " within your rules and limits"
		if len(rec.Notes) > 0 {
			why += " (" + strings.Join(rec.Notes, "; ") + ")"
		}
		return nil, errors.New(why)
	}
	job.UseCase, job.OfferID = uc, rec.OfferID
	out := []string{best.Model}
	for _, o := range rec.Options {
		if o.Route == local.RouteDirect && !o.NeedsApproval && !slices.Contains(out, o.Model) {
			out = append(out, o.Model)
		}
	}
	return out, nil
}

// Reconcile looks up the measured cost of recent estimated direct runs and records it.
func Reconcile(ctx context.Context, cfg config.Config, eng *local.Engine) {
	es, err := history.Read(eng.Dirs.HistoryFile())
	if err != nil {
		return
	}
	l := history.NewLedger(es)
	since := eng.Now().Add(-30 * 24 * time.Hour)
	var todo []history.Entry
	for _, e := range l.Entries {
		if l.Unsettled(e) && e.Route == "direct" && len(e.Generations) > 0 && e.TS.After(since) {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return
	}
	key, err := cfg.OpenRouterKey()
	if err != nil {
		return
	}
	for _, e := range todo {
		if ctx.Err() != nil {
			return
		}
		reconcileRun(ctx, key, eng, e.RunID, e.Generations)
	}
}

func reconcileRun(ctx context.Context, key string, eng *local.Engine, runID string, ids []string) {
	c := newClient(key)
	c.GenerationWait = 2 * time.Second
	total, basis := 0.0, ""
	for _, id := range ids {
		cost, b, err := c.GenerationCost(ctx, id)
		if err != nil {
			return
		}
		total += cost
		basis = costbasis.Combine(basis, b)
	}
	eng.ReconcileBasis(runID, total, basis)
}

// directTool is the direct path as an MCP tool, with the engine's budget cap and measured spend.
type directTool struct {
	cfg config.Config
	eng *local.Engine
}

func (d *directTool) schema() json.RawMessage {
	var m map[string]any
	json.Unmarshal(direct.ToolSchema, &m)
	props, _ := m["properties"].(map[string]any)
	props["offer_id"] = map[string]any{"type": "string", "description": "offer_id, only if you called recommend"}
	props["use_case"] = map[string]any{"type": "string", "description": "catalog use case id, e.g. video-understand, audio-transcribe, audio-tone, image-generate; inferred from the files when left out"}
	props["stakes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 4,
		"description": "your stakes rating, 1-4; without it the use case's catalog stakes apply, or 3"}
	props["await_outcome"] = map[string]any{"type": "boolean",
		"description": "true when you will rate the result: the run is not counted as accepted until history_record_outcome names its run id"}
	b, _ := json.Marshal(m)
	return b
}

func (d *directTool) call(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
	var extra struct {
		OfferID      string `json:"offer_id"`
		UseCase      string `json:"use_case"`
		Stakes       int    `json:"stakes"`
		AwaitOutcome bool   `json:"await_outcome"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return mcp.Error("bad arguments: " + err.Error())
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return mcp.Error("bad arguments: " + err.Error())
	}
	delete(all, "offer_id")
	delete(all, "use_case")
	delete(all, "stakes")
	delete(all, "await_outcome")
	clean, _ := json.Marshal(all)
	req, err := direct.ParseArgs(clean)
	if err != nil {
		return mcp.Error(err.Error())
	}
	res, runID, err := RunDirect(ctx, d.cfg, d.eng, req, DirectJob{OfferID: extra.OfferID, UseCase: extra.UseCase, Stakes: extra.Stakes,
		AwaitOutcome: extra.AwaitOutcome})
	if errors.As(err, new(*direct.PartialError)) && res != nil && strings.TrimSpace(res.Text) != "" {
		// The parts answered before the failure are the result; the note says what is missing.
		err = nil
	}
	if err != nil {
		msg := err.Error()
		if res != nil && res.Usage.CostUSD > 0 {
			msg += fmt.Sprintf("\n(spent $%.6f before the error; run: %s)", res.Usage.CostUSD, runID)
		}
		return mcp.Error(msg)
	}
	var b bytes.Buffer
	tail := ""
	if runID != "" {
		tail = " · run " + runID
	}
	b.WriteString(direct.Compact(res, tail))
	return mcp.Text(strings.TrimRight(b.String(), "\n"))
}
