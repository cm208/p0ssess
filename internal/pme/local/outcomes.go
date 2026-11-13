package local

import (
	"context"

	"p0ssess/internal/outcomes"
)

// PendingOutcomes is the batch of anonymous outcome records the next weekly send would carry, or nil
// when outcome records are off.
func (e *Engine) PendingOutcomes() []outcomes.Record {
	if !e.OutcomesEnabled() {
		return nil
	}
	recs, _ := outcomes.Pending(e.Dirs, e.Now())
	return recs
}

// SendOutcomes sends the week's batch of outcome records when it is due and outcome records are on.
func (e *Engine) SendOutcomes(ctx context.Context) (int, error) {
	post := func(ctx context.Context, body []byte) (int, error) {
		status, _, err := e.Remote.PostOutcomes(ctx, body)
		if status != 0 {
			// The sender decides from the status; an error here only means "not 2xx".
			return status, nil
		}
		return 0, err
	}
	return outcomes.MaybeSend(ctx, e.Dirs, e.OutcomesEnabled(), post, e.Now())
}
