package local

import (
	"context"

	"p0ssess/internal/history"
	"p0ssess/internal/outcomes"
)

// PendingOutcomes is the anonymous outcome records the next send (at the end of a session) would carry, or nil
// when outcome records are off.
func (e *Engine) PendingOutcomes() []outcomes.Record {
	if !e.OutcomesEnabled() {
		return nil
	}
	recs, _ := outcomes.Pending(e.Dirs, e.Now())
	return recs
}

// SendOutcomes sends the pending outcome records, when outcome records are on and any are pending.
func (e *Engine) SendOutcomes(ctx context.Context) (int, error) {
	post := func(ctx context.Context, body []byte) (int, error) {
		status, _, err := e.Remote.PostOutcomes(ctx, body)
		if status != 0 {
			// The sender decides from the status; an error here only means "not 2xx".
			return status, nil
		}
		return 0, err
	}
	return outcomes.Send(ctx, e.Dirs, e.OutcomesEnabled(), post, e.Now())
}

// RecordCalibration writes a graded calibration result into the local history: correct is accepted,
// wrong is declined. It tunes this user's own routing (the history summary and the counts behind it)
// and is never part of the anonymous outcome records.
func (e *Engine) RecordCalibration(useCase, model string, stakes int, correct bool) error {
	outcome := history.Declined
	if correct {
		outcome = history.Accepted
	}
	kind, family := "", ""
	if uc := e.facts().UseCase(useCase); uc != nil {
		kind, family = uc.Kind, uc.Family
	}
	return history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeOutcome, Outcome: outcome,
		Model: model, UseCase: useCase, Kind: kind, Family: family, Stakes: stakes, Route: history.RouteCalibrate, Reason: "calibration"})
}
