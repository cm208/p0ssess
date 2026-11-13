package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
)

// Link is the state of a device authorization in progress.
type Link struct {
	DeviceCode string    `json:"device_code"`
	UserCode   string    `json:"user_code"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
	Interval   int       `json:"interval"`
}

var errSuperseded = errors.New("a newer link replaced this one")

func linkFile(d paths.Dirs) string { return filepath.Join(d.State, "account-link.json") }
func lockFile(d paths.Dirs) string { return filepath.Join(d.State, "account-poll.pid") }

// PollLog is where the detached poller writes.
func PollLog(d paths.Dirs) string { return filepath.Join(d.State, "account-poll.log") }

// StartLink asks for a device code and saves it.
func StartLink(ctx context.Context, d paths.Dirs, c *Client) (Link, error) {
	dc, err := c.DeviceCode(ctx)
	if err != nil {
		return Link{}, err
	}
	l := Link{DeviceCode: dc.DeviceCode, UserCode: dc.UserCode, URL: dc.VerificationURIComplete,
		ExpiresAt: now().Add(time.Duration(dc.ExpiresIn) * time.Second), Interval: dc.Interval}
	if l.Interval <= 0 {
		l.Interval = 5
	}
	b, _ := json.Marshal(l)
	return l, paths.WriteFileAtomic(linkFile(d), b, 0o600)
}

// PendingLink returns the link in progress, if its poller is still running and it has not expired.
func PendingLink(d paths.Dirs) (Link, bool) {
	b, err := os.ReadFile(linkFile(d))
	if err != nil {
		return Link{}, false
	}
	var l Link
	if json.Unmarshal(b, &l) != nil || l.DeviceCode == "" || !l.ExpiresAt.After(now()) {
		return Link{}, false
	}
	return l, true
}

// PollerRunning reports whether a poller holds the lock.
func PollerRunning(d paths.Dirs) bool {
	b, err := os.ReadFile(lockFile(d))
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return proc.Alive(pid)
}

// lock takes the single-poller lock; it reports false when a live poller already holds it.
func lock(d paths.Dirs) (release func(), ok bool) {
	f := lockFile(d)
	os.MkdirAll(filepath.Dir(f), 0o700)
	for i := 0; i < 2; i++ {
		fh, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprint(fh, os.Getpid())
			fh.Close()
			return func() { os.Remove(f) }, true
		}
		if PollerRunning(d) {
			return nil, false
		}
		os.Remove(f)
	}
	return nil, false
}

// sleep waits for d or until ctx ends; tests replace it.
var sleep = func(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Poll finishes the saved link: it polls until the user confirms, denies or the code expires. On success it
// writes the token and refreshes the account. Only one poller runs at a time; a second returns nil at once.
func Poll(ctx context.Context, d paths.Dirs, c *Client) error {
	release, ok := lock(d)
	if !ok {
		return nil
	}
	defer release()
	l, ok := PendingLink(d)
	if !ok {
		os.Remove(linkFile(d))
		return errors.New("no link in progress")
	}
	interval := time.Duration(l.Interval) * time.Second
	fail := 0
	for {
		sleep(ctx, interval)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cur, ok := PendingLink(d); !ok || cur.DeviceCode != l.DeviceCode {
			if !ok {
				os.Remove(linkFile(d))
				return errors.New("the link code expired")
			}
			return errSuperseded
		}
		tok, err := c.DeviceToken(ctx, l.DeviceCode)
		if err == nil {
			if err := SaveToken(d, tok); err != nil {
				return err
			}
			os.Remove(linkFile(d))
			Refresh(ctx, d, c)
			return nil
		}
		var ae *APIError
		if !errors.As(err, &ae) {
			// Network trouble: keep trying until the code expires.
			if fail++; fail > 20 {
				return err
			}
			continue
		}
		switch ae.Code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			os.Remove(linkFile(d))
			return errors.New("the link was declined in the browser")
		case "expired_token":
			os.Remove(linkFile(d))
			return errors.New("the link code expired")
		default:
			if ae.Status >= 500 || ae.Status == 429 {
				continue
			}
			os.Remove(linkFile(d))
			return ae
		}
	}
}
