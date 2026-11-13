package team

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
)

// Every is how often a running MCP server fetches the policy.
const Every = 5 * time.Minute

// ShareRuleID names the budget rule made from the member's monthly share.
const ShareRuleID = "t-share"

// Policy is the payload of the signed policy.
type Policy struct {
	V               int      `json:"v"`
	Type            string   `json:"type"`
	OrgID           string   `json:"org_id"`
	OrgName         string   `json:"org_name"`
	AccountID       string   `json:"account_id"`
	Role            string   `json:"role"`
	Issued          string   `json:"issued"`
	Rules           []Rule   `json:"rules"`
	MonthlyShareUSD *float64 `json:"monthly_share_usd,omitempty"`
	ReportSpend     bool     `json:"report_spend"`

	issued time.Time
}

// LocalRules is the policy at team scope: its rules, and the member's share as a cap on monthly_usd.
func (p *Policy) LocalRules() []rules.Rule {
	if p == nil {
		return nil
	}
	out := make([]rules.Rule, 0, len(p.Rules)+1)
	for _, r := range p.Rules {
		out = append(out, r.Local())
	}
	if p.MonthlyShareUSD != nil {
		out = append(out, rules.Rule{ID: ShareRuleID, Kind: rules.KindBudget, Field: "monthly_usd", Value: *p.MonthlyShareUSD,
			Note: "team budget share", Scope: rules.ScopeTeam})
	}
	return out
}

func policyFile(d paths.Dirs) string { return filepath.Join(d.State, "team-policy.json") }

// parse checks the signature and the payload's shape, but not whose it is.
func parse(raw []byte, keys map[string]ed25519.PublicKey) (*Policy, error) {
	payload, err := table.Unseal(raw, keys, "team policy")
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("team policy: %w", err)
	}
	if p.Type != "team-policy" {
		return nil, fmt.Errorf("team policy: type %q", p.Type)
	}
	if p.V != 1 {
		return nil, fmt.Errorf("team policy: version %d", p.V)
	}
	if p.issued, err = time.Parse(time.RFC3339, p.Issued); err != nil {
		return nil, fmt.Errorf("team policy: issued: %w", err)
	}
	if p.MonthlyShareUSD != nil && !(*p.MonthlyShareUSD >= 0) {
		return nil, errors.New("team policy: negative share")
	}
	return &p, nil
}

// Verify checks a fetched envelope: a pinned key and a good signature, type team-policy, issued to
// accountID, and not older than cached (when there is one).
func Verify(raw []byte, keys map[string]ed25519.PublicKey, accountID string, cached *Policy) (*Policy, error) {
	p, err := parse(raw, keys)
	if err != nil {
		return nil, err
	}
	if accountID == "" || p.AccountID != accountID {
		return nil, errors.New("team policy: issued to another account")
	}
	if cached != nil && p.issued.Before(cached.issued) {
		return nil, errors.New("team policy: older than the one in force")
	}
	return p, nil
}

// Load returns the cached policy, checked again, or nil when there is none in force: no token, no
// cache, or one that does not verify or belongs to another account.
func Load(d paths.Dirs, keys map[string]ed25519.PublicKey) *Policy {
	if account.Token(d) == "" {
		return nil
	}
	raw, err := os.ReadFile(policyFile(d))
	if err != nil {
		return nil
	}
	p, err := parse(raw, keys)
	if err != nil {
		return nil
	}
	if id := account.Current(d).AccountID; id != "" && id != p.AccountID {
		return nil
	}
	return p
}

// Forget deletes the cached policy and the spend queue.
func Forget(d paths.Dirs) {
	os.Remove(policyFile(d))
	clearQueue(d)
}

// Result is what one sync did.
type Result struct {
	Policy *Policy // in force after the sync; nil when none
	Sent   int     // spend runs sent
	Left   bool    // the account has no team: the cache and the queue were deleted
}

// Sync fetches the policy, keeps it when it verifies, and sends the spend queue. It does nothing
// without a token, and only forgets when the cached account shows no org. A failed fetch keeps the
// cached policy in force.
func Sync(ctx context.Context, c *Client, keys map[string]ed25519.PublicKey) (Result, error) {
	d := c.Dirs
	tok := account.Token(d)
	if tok == "" {
		return Result{}, nil
	}
	st := account.Current(d)
	if st.OrgID == "" {
		Forget(d)
		return Result{Left: true}, nil
	}
	var raw json.RawMessage
	_, err := c.API.Do(ctx, "GET", "/v1/team/policy", tok, nil, &raw)
	switch {
	case gone(err):
		Forget(d)
		return Result{Left: true}, nil
	case account.IsInvalidToken(err):
		account.DeleteToken(d)
		Forget(d)
		return Result{Left: true}, nil
	case err != nil:
		return Result{Policy: Load(d, keys)}, err
	}
	cached := Load(d, keys)
	p, err := Verify(raw, keys, st.AccountID, cached)
	if err != nil {
		return Result{Policy: cached}, err
	}
	if err := paths.WriteFileAtomic(policyFile(d), raw, 0o600); err != nil {
		return Result{Policy: cached}, err
	}
	n, err := Flush(ctx, c)
	return Result{Policy: p, Sent: n}, err
}

// startDelay lets the account check at start finish before the first fetch; tests shorten it.
var startDelay = 3 * time.Second

// Watch syncs shortly after start and every Every until ctx ends. An install with no token makes no
// calls. Run it in its own goroutine.
func Watch(ctx context.Context, d paths.Dirs, keys map[string]ed25519.PublicKey) {
	c := NewClient(d, "")
	wait := startDelay
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if account.Token(d) != "" {
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			Sync(sctx, c, keys)
			cancel()
		}
		wait = Every
	}
}
