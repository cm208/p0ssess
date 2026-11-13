// Package account is the client side of p0ssess Pro: the device token, the cached account state, the
// link flow and the calls to the accounts API (see the contract in the cloud service's api/auth.md).
//
// Free users have no account. Without a token file nothing here touches the network.
package account

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
)

// DefaultBaseURL is where the accounts API lives until hosting is chosen. P0SSESS_CLOUD_URL overrides it.
const DefaultBaseURL = "https://cloud.p0ssess.me"

// WatchEvery is how often a running MCP server re-checks the account.
const WatchEvery = 6 * time.Hour

var now = time.Now

// BaseURL is the accounts API base URL without a trailing slash.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv("P0SSESS_CLOUD_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

func tokenFile(d paths.Dirs) string { return filepath.Join(d.Config, "secrets", "p0ssess_token") }
func stateFile(d paths.Dirs) string { return filepath.Join(d.State, "account.json") }

// Token returns the device token, or "" when this install is not linked. It reads the file on every
// call, so a link finished by another process turns Pro on without a restart. Cloud requests send it as
// "Authorization: Bearer <token>".
func Token(d paths.Dirs) string {
	b, err := os.ReadFile(tokenFile(d))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveToken stores the device token (mode 0600, atomic).
func SaveToken(d paths.Dirs, token string) error {
	return paths.WriteFileAtomic(tokenFile(d), []byte(strings.TrimSpace(token)+"\n"), 0o600)
}

// DeleteToken removes the token and the cached account state.
func DeleteToken(d paths.Dirs) {
	os.Remove(tokenFile(d))
	os.Remove(stateFile(d))
}

// State is the cached answer of GET /v1/account.
type State struct {
	Plan         string    `json:"plan"`
	Status       string    `json:"status"`
	Interval     string    `json:"interval,omitempty"`
	RenewsAt     string    `json:"renews_at,omitempty"`
	EndsAt       string    `json:"ends_at,omitempty"`
	Renew        string    `json:"renew,omitempty"`
	Manage       string    `json:"manage,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
	LapsePending bool      `json:"lapse_pending,omitempty"`
	LapseShown   bool      `json:"lapse_shown,omitempty"`
}

func free() State { return State{Plan: "free", Status: "none"} }

func (s State) paid() bool { return s.Plan == "pro" || s.Plan == "team" }

// Pro reports whether Pro features are on. A canceled plan stops at its end date even before the next check.
func (s State) Pro() bool {
	if !s.paid() {
		return false
	}
	if s.Status == "canceling" || s.Status == "lapsed" {
		if t, err := time.Parse(time.RFC3339, s.EndsAt); err == nil && !t.After(now()) {
			return false
		}
	}
	return true
}

func load(d paths.Dirs) (State, bool) {
	b, err := os.ReadFile(stateFile(d))
	if err != nil {
		return free(), false
	}
	var s State
	if json.Unmarshal(b, &s) != nil || s.Plan == "" {
		return free(), false
	}
	return s, true
}

func save(d paths.Dirs, s State) error {
	b, _ := json.MarshalIndent(s, "", " ")
	return paths.WriteFileAtomic(stateFile(d), b, 0o600)
}

// Current is the cached state, read from disk with no network. It is free without a token. Pro features
// read it and fall back to the free plan quietly.
func Current(d paths.Dirs) State {
	if Token(d) == "" {
		return free()
	}
	s, _ := load(d)
	return s
}

// Refresh asks the server for the account and updates the cache. With no token it does nothing. A 401
// removes the token: the install is free again. Other failures keep the cached state.
func Refresh(ctx context.Context, d paths.Dirs, c *Client) (State, error) {
	tok := Token(d)
	if tok == "" {
		return free(), nil
	}
	if c == nil {
		c = NewClient("")
	}
	resp, err := c.Account(ctx, tok)
	if err != nil {
		if IsInvalidToken(err) {
			DeleteToken(d)
			return free(), nil
		}
		s, _ := load(d)
		return s, err
	}
	prev, _ := load(d)
	s := State{
		Plan:      resp.Entitlements.Plan,
		Status:    resp.Entitlements.Status,
		Interval:  resp.Entitlements.Interval,
		RenewsAt:  resp.Entitlements.RenewsAt,
		EndsAt:    resp.Entitlements.EndsAt,
		Renew:     resp.Links.Renew,
		Manage:    resp.Links.Manage,
		CheckedAt: now().UTC(),

		LapsePending: prev.LapsePending,
		LapseShown:   prev.LapseShown,
	}
	if s.Plan == "" {
		s.Plan = "free"
	}
	switch {
	case s.paid():
		s.LapsePending, s.LapseShown = false, false
	case prev.paid() && s.Status == "lapsed":
		s.LapsePending, s.LapseShown = true, false
	}
	return s, save(d, s)
}

// Watch refreshes the account now and every WatchEvery until ctx ends. A free install (no token file)
// makes no calls. Run it in its own goroutine.
func Watch(ctx context.Context, d paths.Dirs) {
	c := NewClient("")
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	for {
		if Token(d) != "" {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			Refresh(rctx, d, c)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Notice returns the one line to show when Pro has just ended, and "" every time after that. It reads the
// cache only.
func Notice(d paths.Dirs) string {
	s, ok := load(d)
	if !ok || !s.LapsePending || s.LapseShown || s.paid() {
		return ""
	}
	s.LapseShown, s.LapsePending = true, false
	if save(d, s) != nil {
		return ""
	}
	ended := "p0ssess Pro ended"
	if t, err := time.Parse(time.RFC3339, s.EndsAt); err == nil {
		ended += " on " + t.UTC().Format("Jan 2")
	}
	renew := s.Renew
	if renew == "" {
		renew = BaseURL() + "/pro"
	}
	return fmt.Sprintf("%s; routing is back on the free plan. Renew: %s", ended, renew)
}
