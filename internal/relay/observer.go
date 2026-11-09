package relay

// Observer hears about each prompt the relay sends: when it starts and, once, when it ends with its
// measured cost. The plugin's server uses it for the status line's running count and the engine's spend.
type Observer interface {
	RunStarted(r RunInfo)
	RunFinished(r RunInfo, res *Result)
}

// RunInfo identifies one prompt on one session.
type RunInfo struct {
	SessionID string
	Turn      int
	Model     string
	Mode      string
	Kind      string
	Repo      string
}

func (e *Engine) SetObserver(o Observer) { e.obs = o }

func (e *Engine) runInfo(rec *SessionRecord) RunInfo {
	return RunInfo{SessionID: rec.SessionID, Turn: rec.Turns, Model: rec.Model, Mode: rec.Mode, Kind: rec.Kind, Repo: rec.Repo}
}
