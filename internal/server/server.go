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
	"p0ssess/internal/mcp"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/mcpserver"
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
	obs := &observer{eng: eng, jobs: map[string]string{}}
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

	// A relay run can cost up to max_run_cost_usd before the relay interrupts it; refuse to start one
	// that could pass the monthly cap.
	s.Wrap("run", func(next mcp.Handler) mcp.Handler {
		return func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			if st, err := eng.Status(engine.Scope{}); err == nil && st.Spend.Month+cfg.MaxRunCostUSD > st.Budget.MonthlyUSD {
				return mcp.Error(fmt.Sprintf("refused: $%.2f spent this month plus up to $%.2f for this run would pass the $%.2f monthly cap; "+
					"raise monthly_usd first, or lower max_run_cost_usd in config.json", st.Spend.Month, cfg.MaxRunCostUSD, st.Budget.MonthlyUSD))
			}
			return next(ctx, args, p)
		}
	})

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

// observer turns relay runs into running-job files and measured ledger runs.
type observer struct {
	eng  *engine.Engine
	mu   sync.Mutex
	jobs map[string]string // session id -> job id
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

func (o *observer) RunFinished(r relay.RunInfo, res *relay.Result) {
	o.mu.Lock()
	if id, ok := o.jobs[r.SessionID]; ok {
		usage.FinishJob(o.eng.Dirs.RunningDir(), id)
		delete(o.jobs, r.SessionID)
	}
	o.mu.Unlock()
	o.eng.RecordRun(engine.Run{ID: fmt.Sprintf("%s/%d", r.SessionID, r.Turn), Route: "agent", Model: r.Model,
		CashUSD: res.CostUSD, Status: res.Status, UseCase: r.Kind, Cwd: r.Repo})
}

func (o *observer) finishAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for sid, id := range o.jobs {
		usage.FinishJob(o.eng.Dirs.RunningDir(), id)
		delete(o.jobs, sid)
	}
}

// RunDirect runs one direct-path job with the monthly cap checked first, a running-job file while it
// runs, and its measured cost recorded in the ledger. runID is empty for dry runs and failed calls
// that cost nothing.
func RunDirect(ctx context.Context, cfg config.Config, eng *engine.Engine, req direct.Request, offerID, useCase string) (*direct.Result, string, error) {
	key := ""
	if !req.DryRun {
		var err error
		if key, err = cfg.OpenRouterKey(); err != nil {
			return nil, "", err
		}
		st, err := eng.Status(engine.Scope{})
		if err == nil && st.Spend.Month+req.MaxCostUSD > st.Budget.MonthlyUSD {
			return nil, "", fmt.Errorf("refused: $%.2f spent this month plus up to $%.2f would pass the $%.2f monthly cap; "+
				"raise monthly_usd first", st.Spend.Month, req.MaxCostUSD, st.Budget.MonthlyUSD)
		}
	}
	c := direct.New(key)
	c.RecordDir = os.Getenv("P0SSESS_DIRECT_RECORD")
	if p, err := cfg.RegistryPath(); err == nil {
		c.Catalog.LoadRegistry(p)
	}
	var job usage.Job
	if !req.DryRun {
		job, _ = usage.StartJob(eng.Dirs.RunningDir(), req.Model, useCase, os.Getpid())
	}
	res, err := c.Run(ctx, req)
	if job.ID != "" {
		usage.FinishJob(eng.Dirs.RunningDir(), job.ID)
	}
	runID := ""
	if res != nil && !req.DryRun && res.Usage.CostSource != "" {
		runID = "d-" + randID()
		status := "done"
		if err != nil {
			status = "failed"
		}
		cwd, _ := os.Getwd()
		if v := os.Getenv("CLAUDE_PROJECT_DIR"); v != "" {
			cwd = v
		}
		eng.RecordRun(engine.Run{ID: runID, Route: "direct", Model: req.Model, CashUSD: res.Usage.CostUSD,
			UpstreamUSD: res.Usage.UpstreamUSD, Status: status, OfferID: offerID, UseCase: useCase, Cwd: cwd})
	}
	return res, runID, err
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
	props["use_case"] = map[string]any{"type": "string", "description": "catalog use case id, for the history"}
	b, _ := json.Marshal(m)
	return b
}

func (d *directTool) call(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
	var extra struct {
		OfferID string `json:"offer_id"`
		UseCase string `json:"use_case"`
	}
	json.Unmarshal(raw, &extra)
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return mcp.Error("bad arguments: " + err.Error())
	}
	delete(all, "offer_id")
	delete(all, "use_case")
	clean, _ := json.Marshal(all)
	req, err := direct.ParseArgs(clean)
	if err != nil {
		return mcp.Error(err.Error())
	}
	res, runID, err := RunDirect(ctx, d.cfg, d.eng, req, extra.OfferID, extra.UseCase)
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
