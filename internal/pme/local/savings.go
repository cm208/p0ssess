package local

import (
	"context"
	"errors"

	"p0ssess/internal/account"
	"p0ssess/internal/notices"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/savings"
)

// SendSavingsReport uploads last month's savings figures once, for a linked Pro or Team install that has
// the report on. Free installs never reach the network here.
func (e *Engine) SendSavingsReport(ctx context.Context) (bool, error) {
	b, err := e.Budget()
	if err != nil {
		return false, err
	}
	linked := account.Token(e.Dirs) != ""
	pro := linked && account.Current(e.Dirs).Pro()
	if !b.Report.Send || !pro {
		return false, nil
	}
	post := func(ctx context.Context, body []byte) (int, error) {
		status, err := e.Remote.PostMonthlySavings(ctx, body)
		var re *remote.Error
		if errors.As(err, &re) {
			return status, nil
		}
		return status, err
	}
	return savings.Send(ctx, e.Dirs, notices.Receipts(e.Dirs.NoticesFile()), b.Report.Send, pro, linked, post, e.Now())
}
