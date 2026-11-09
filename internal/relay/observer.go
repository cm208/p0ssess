package relay

import (
	"errors"
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
	PriceUnknown bool   `json:"price_unknown,omitempty"`
	Note         string `json:"-"`
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
