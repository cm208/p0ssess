package cloudsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"p0ssess/internal/pme/paths"
)

// HostURL is the sandbox host for a ticket: P0SSESS_SANDBOX_URL when set (tests), else the ticket's own.
func HostURL(t *Ticket) (string, error) {
	if v := os.Getenv("P0SSESS_SANDBOX_URL"); v != "" {
		return v, nil
	}
	if t != nil && t.SandboxURL != "" {
		return t.SandboxURL, nil
	}
	return "", errors.New("the cloud sandbox is not available yet (no sandbox host was assigned)")
}

// usageTTL is how long a reading of the month's hours stands; a session that ends clears it.
const usageTTL = 5 * time.Minute

func usageFile(d paths.Dirs) string { return filepath.Join(d.Cache, "sandbox-usage.json") }

// ForgetUsage drops the cached reading, so the next one asks the host.
func ForgetUsage(d paths.Dirs) { os.Remove(usageFile(d)) }

// CurrentUsage is this month's sandbox hours for the linked account. Readings are cached for a few
// minutes; a failure returns the last reading if there is one.
func CurrentUsage(ctx context.Context, d paths.Dirs, token string, now time.Time) (Usage, error) {
	var cached Usage
	if b, err := os.ReadFile(usageFile(d)); err == nil && json.Unmarshal(b, &cached) == nil && cached.Month == now.UTC().Format("2006-01") {
		if now.Sub(cached.CheckedAt) < usageTTL {
			return cached, nil
		}
	} else {
		cached = Usage{}
	}
	u, err := fetchUsage(ctx, token)
	if err != nil {
		if cached.Month != "" {
			return cached, err
		}
		return Usage{}, err
	}
	u.CheckedAt = now.UTC()
	b, _ := json.Marshal(u)
	_ = paths.WriteFileAtomic(usageFile(d), b, 0o600)
	return u, nil
}

func fetchUsage(ctx context.Context, token string) (Usage, error) {
	plain := &http.Client{Timeout: 15 * time.Second}
	t, err := FetchTicket(ctx, plain, CloudURL(), token)
	if err != nil {
		return Usage{}, err
	}
	host, err := HostURL(t)
	if err != nil {
		return Usage{}, err
	}
	hc, err := Transport(os.Getenv("P0SSESS_SANDBOX_CA"))
	if err != nil {
		return Usage{}, err
	}
	return FetchUsage(ctx, hc, host, t.Ticket)
}
