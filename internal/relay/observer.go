package relay

import (
	"errors"
	"strings"
	"time"
)

// Observer hears about each prompt the relay sends. Authorize runs before anything is spent and may
// refuse; Release closes an authorization whose prompt was never sent; RunStarted and RunFinished
// bracket the run, RunFinished once with its cost. The plugin's server uses it to enforce the user's
// limits, for the status line's running count and for the engine's spend.
type Observer interface {
	Authorize(a RunAuth) (Grant, error)
	Release(g Grant)
	RunStarted(r RunInfo)
	RunFinished(r RunInfo, res *Result) error
}

// RunAuth is what a prompt is about to do.
type RunAuth struct {
	Model      string
	Mode       string
	Kind       string
	UseCase    string
	Stakes     int
	MaxCostUSD float64
	// ExplicitMax is true when the caller passed max_cost_usd instead of taking max_run_cost_usd.
	ExplicitMax bool
	MaxRuntime  time.Duration
	Repo        string
	Cwd         string
}

// Grant is an authorized prompt: its reservation and the cost cap the relay enforces.
type Grant struct {
	ReserveID  string  `json:"reserve_id,omitempty"`
	CapUSD     float64 `json:"cap_usd,omitempty"`
	ApprovalID string  `json:"approval_id,omitempty"`
	ZDR        bool    `json:"zdr,omitempty"`
	Session    string  `json:"claude_session,omitempty"`
	// PriceUnknown means no price is known for the model: OpenCode may report $0 for a billed run.
	PriceUnknown bool `json:"price_unknown,omitempty"`
	// ShellInternet is set when the user opted this project in to internet for shell commands.
	ShellInternet bool   `json:"shell_internet,omitempty"`
	Note          string `json:"-"`
}

// RunInfo identifies one prompt on one session.
type RunInfo struct {
	SessionID string
	Turn      int
	Model     string
	Mode      string
	Kind      string
	UseCase   string
	Repo      string
	Grant     Grant
}

// ErrNoObserver refuses runs when nothing enforces the user's limits.
var ErrNoObserver = errors.New("refused: no budget guard is attached to the relay")

func (e *Engine) SetObserver(o Observer) { e.obs = o }

func (e *Engine) runInfo(rec *SessionRecord) RunInfo {
	return RunInfo{SessionID: rec.SessionID, Turn: rec.Turns, Model: rec.Model, Mode: rec.Mode, Kind: rec.Kind,
		UseCase: rec.UseCase, Repo: rec.Repo, Grant: rec.Grant}
}

// Chooser is an Observer that can pick the model for a run that names none, from the routing engine.
type Chooser interface {
	Choose(c ChooseRequest) (Choice, error)
}

// ChooseRequest is what the engine needs to pick a model for a run.
type ChooseRequest struct {
	Task    string
	Mode    string
	UseCase string
	Stakes  int
	Cwd     string
}

// Choice is the engine's pick: the model, the use case and stakes it was priced under, and one line on why.
type Choice struct {
	Model   string
	UseCase string
	Stakes  int
	Why     string
}

// chooseModel fills in req.Model (and the use case and stakes) for a new run that names no model, by
// asking the routing engine. It returns the one-line note for the result. A run that names a model, or
// continues a session, is left alone.
func (e *Engine) chooseModel(req *RunRequest) (string, error) {
	if strings.TrimSpace(req.Model) != "" || req.Session != "" {
		return "", nil
	}
	ch, ok := e.obs.(Chooser)
	if !ok {
		return "", nil // the run is refused where the model is parsed, with the way to name one
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = defaultCwd()
	}
	c, err := ch.Choose(ChooseRequest{Task: req.Task, Mode: req.Mode, UseCase: req.UseCase, Stakes: req.Stakes, Cwd: cwd})
	if err != nil {
		return "", err
	}
	req.Model = c.Model
	if req.UseCase == "" {
		req.UseCase = c.UseCase
	}
	if req.Stakes == 0 {
		req.Stakes = c.Stakes
	}
	return "p0ssess chose " + c.Model + ": " + c.Why, nil
}
