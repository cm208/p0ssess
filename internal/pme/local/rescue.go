package local

import (
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/rescue"
)

// rescueLift reports whether a Pro rescue period is running: stakes-4 work may then go to another
// model, but only with a yes for that task (see approve and Authorize). Outside rescue the stakes gate
// is unchanged.
func (e *Engine) rescueLift(now time.Time) bool {
	if !account.Current(e.Dirs).Pro() {
		return false
	}
	if b, err := e.Budget(); err != nil || !b.Rescue.Enabled {
		return false
	}
	_, on := rescue.Active(e.Dirs, now)
	return on
}
