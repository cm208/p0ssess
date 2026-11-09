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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/history"
	"p0ssess/internal/mcp"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/mcpserver"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/relay"
	"p0ssess/internal/usage"
)

// Name is the MCP server name; inside the plugin its tools are mcp__plugin_p0ssess_p0ssess__<tool>.
const Name = "p0ssess"

const instructions = `p0ssess hands work to other models through OpenRouter while Claude stays in charge.
- run/wait/abort/answer_permission/models: the agent path, a sandboxed OpenCode session for multi-step work on files. Use it through the p0ssess relay subagent.
- direct: one OpenRouter call with local files, for media Claude cannot read or make (video, audio, image generation) and one-shot jobs.
- recommend, budget_status, rules_*, approvals_*, history_record_outcome, tendencies, settings_set, shortcuts_*: the routing engine.
`

// Options configure New.
type Options struct {
	Version string
	Log     io.Writer
	// Agent reports why the agent path cannot run here, or nil when it can.
	Agent func() error
}

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
	eng, err := engine.Load(enginePaths())
	if err != nil {
		return nil, nil, err
	}
	obs := NewObserver(eng).(*observer)
	re := relay.NewEngine(cfg, opt.Version, opt.Log)
	re.SetObserver(obs)
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
	s.AddTool(mcp.Tool{Name: direct.ToolName, Description: direct.ToolDescription + " Pass offer_id and use_case from recommend when you have them. " +
		"The result ends with a run id for history_record_outcome.", InputSchema: d.schema()}, d.call)

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

// AgentAvailable reports whether the jailed OpenCode path can run: Linux with podman and nft.
func AgentAvailable() error {
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
func NewObserver(eng *engine.Engine) relay.Observer {
	return &observer{eng: eng, jobs: map[string]string{}}
}

// observer authorizes relay runs, turns them into running-job files and records their cost.
type observer struct {
	eng  *engine.Engine
	mu   sync.Mutex
	jobs map[string]string // session id -> job id
}

// Authorize applies every limit to one relay prompt and reserves its cost cap.
func (o *observer) Authorize(a relay.RunAuth) (relay.Grant, error) {
	g, err := o.eng.Authorize(engine.Charge{Route: "agent", Model: a.Model, UseCase: a.UseCase, Stakes: a.Stakes,
		MaxUSD: a.MaxCostUSD, Hold: a.MaxRuntime + 15*time.Minute, Scope: engine.Scope{Cwd: a.Cwd}})
	if err != nil {
		return relay.Grant{}, err
	}
	return relay.Grant{ReserveID: g.ReserveID, CapUSD: g.MaxUSD, ApprovalID: g.ApprovalID, ZDR: g.ZDR, Session: g.Session,
		Note: strings.Join(g.Notes, "; ")}, nil
}

func (o *observer) Release(g relay.Grant) {
	o.eng.Release(&engine.Grant{ReserveID: g.ReserveID})
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
	run := engine.Run{ID: fmt.Sprintf("%s/%d", r.SessionID, r.Turn), Route: "agent", Model: r.Model,
		CashUSD: res.CostUSD, Status: res.Status, UseCase: r.UseCase, Cwd: r.Repo}
	if res.CostEstimated {
		// The session's cost could not be read: count the run at its cap until a measured figure exists.
		run.CashUSD, run.Estimated = max(res.CostUSD, r.Grant.CapUSD), true
	}
	return o.eng.Settle(&engine.Grant{ReserveID: r.Grant.ReserveID, ApprovalID: r.Grant.ApprovalID, Session: r.Grant.Session}, run)
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
}

// RunDirect runs one direct-path job under every limit: Authorize checks the rules, lists, stakes,
// privacy, per-task, daily and monthly limits and reserves max_cost_usd; the measured cost settles the
// reservation, or, when the cost is not known, the reserved maximum is recorded as an estimate. runID
// is empty for dry runs and calls that were never sent.
func RunDirect(ctx context.Context, cfg config.Config, eng *engine.Engine, req direct.Request, job DirectJob) (*direct.Result, string, error) {
	cwd, _ := os.Getwd()
	if v := os.Getenv("CLAUDE_PROJECT_DIR"); v != "" {
		cwd = v
	}
	key := ""
	var grant *engine.Grant
	if !req.DryRun {
		g, err := eng.Authorize(engine.Charge{Route: "direct", Model: req.Model, UseCase: job.UseCase, Stakes: job.Stakes,
			MaxUSD: req.MaxCostUSD, Hold: DirectHold, Scope: engine.Scope{Cwd: cwd}})
		if err != nil {
			return nil, "", err
		}
		grant = g
		if g.ZDR {
			req.StrictZDR = true
		}
		if key, err = cfg.OpenRouterKey(); err != nil {
			eng.Release(grant)
			return nil, "", err
		}
	} else if paths.Private(cwd) {
		return nil, "", errors.New("refused: this repository has a .p0ssess-private file; delegation is blocked here")
	}
	c := newClient(key)
	c.RecordDir = os.Getenv("P0SSESS_DIRECT_RECORD")
	if p, err := cfg.RegistryPath(); err == nil {
		c.Catalog.LoadRegistry(p)
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
	runID := "d-" + randID()
	status := "done"
	if err != nil {
		status = "failed"
	}
	run := engine.Run{ID: runID, Route: "direct", Model: req.Model, CashUSD: res.Usage.CostUSD,
		UpstreamUSD: res.Usage.UpstreamUSD, Status: status, OfferID: job.OfferID, UseCase: job.UseCase, Cwd: cwd}
	if !res.Measured() {
		run.CashUSD, run.Estimated = max(grant.MaxUSD, res.Usage.CostUSD), true
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

// Reconcile looks up the measured cost of recent estimated direct runs and records it.
func Reconcile(ctx context.Context, cfg config.Config, eng *engine.Engine) {
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

func reconcileRun(ctx context.Context, key string, eng *engine.Engine, runID string, ids []string) {
	c := newClient(key)
	c.GenerationWait = 2 * time.Second
	total := 0.0
	for _, id := range ids {
		cost, err := c.GenerationCost(ctx, id)
		if err != nil {
			return
		}
		total += cost
	}
	eng.Reconcile(runID, total)
}

// directTool is the direct path as an MCP tool, with the engine's budget cap and measured spend.
type directTool struct {
	cfg config.Config
	eng *engine.Engine
}

func (d *directTool) schema() json.RawMessage {
	var m map[string]any
	json.Unmarshal(direct.ToolSchema, &m)
	props, _ := m["properties"].(map[string]any)
	props["offer_id"] = map[string]any{"type": "string", "description": "offer_id from recommend, for the history"}
	props["use_case"] = map[string]any{"type": "string", "description": "catalog use case id; rules limited to use cases apply by it"}
	props["stakes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 4,
		"description": "your stakes rating, 1-4; without it the use case's catalog stakes apply, or 3"}
	b, _ := json.Marshal(m)
	return b
}

func (d *directTool) call(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
	var extra struct {
		OfferID string `json:"offer_id"`
		UseCase string `json:"use_case"`
		Stakes  int    `json:"stakes"`
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
	clean, _ := json.Marshal(all)
	req, err := direct.ParseArgs(clean)
	if err != nil {
		return mcp.Error(err.Error())
	}
	res, runID, err := RunDirect(ctx, d.cfg, d.eng, req, DirectJob{OfferID: extra.OfferID, UseCase: extra.UseCase, Stakes: extra.Stakes})
	if err != nil {
		msg := err.Error()
		if res != nil && res.Usage.CostUSD > 0 {
			msg += fmt.Sprintf("\n(spent $%.6f before the error; run: %s)", res.Usage.CostUSD, runID)
		}
		return mcp.Error(msg)
	}
	var b bytes.Buffer
	b.WriteString(direct.Format(res))
	if runID != "" {
		fmt.Fprintf(&b, "run: %s\n", runID)
	}
	return mcp.Text(strings.TrimRight(b.String(), "\n"))
}
