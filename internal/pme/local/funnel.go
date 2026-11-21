package local

import (
	"context"
	"errors"

	"p0ssess/internal/account"
	"p0ssess/internal/funnel"
	"p0ssess/internal/notices"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/tour"
)

// FunnelView is what the p0ssess page shows about the opt-in usage counts.
func (e *Engine) FunnelView() funnel.View { return funnel.Show(e.Dirs, e.Now()) }

// SendFunnel sends the finished weeks of usage counts, when they are on.
func (e *Engine) SendFunnel(ctx context.Context) (int, error) {
	post := func(ctx context.Context, body []byte) (int, error) {
		status, err := e.Remote.PostFunnel(ctx, body)
		var re *remote.Error
		if errors.As(err, &re) {
			return status, nil
		}
		return status, err
	}
	return funnel.Send(ctx, e.Dirs, post, e.Now())
}

// funnelChanged starts or stops the usage counts after funnel.send changed.
func (e *Engine) funnelChanged(on bool) error {
	if !on {
		return funnel.Stop(e.Dirs)
	}
	r := funnel.Reached{Linked: account.Token(e.Dirs) != ""}
	if s, ok := tour.Load(e.Dirs); ok && s.State == tour.Done {
		r.TourDone = true
	}
	r.Routed = len(e.Ledger().Runs(1)) > 0
	for _, n := range notices.Recent(e.Dirs.NoticesFile(), 500) {
		if n.SavedUSD > 0 {
			r.Saved = true
			break
		}
	}
	return funnel.Start(e.Dirs, r, e.Now())
}
