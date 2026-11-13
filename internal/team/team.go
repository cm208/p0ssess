// Package team is the client side of p0ssess Team: the org's API, its signed policy (shared rules and
// the member's budget share, cached and enforced offline) and the spend reports a member's client
// sends the org (see the Team section of the cloud service's api/auth.md).
//
// Nothing here touches the network without a device token, and nothing is sent unless the cached
// account shows an org.
package team

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/rules"
)

// ErrNotLinked means this install has no device token.
var ErrNotLinked = errors.New("this install is not linked to an account; run p0ssess account link")

// MaxRules is the most shared rules an org holds; MaxNote the longest note.
const (
	MaxRules = 200
	MaxNote  = 200
)

// Rule is one shared rule as the server takes and gives it: the client's rule fields and nothing else.
type Rule struct {
	ID       string    `json:"id,omitempty"`
	Kind     string    `json:"kind"`
	Target   string    `json:"target,omitempty"`
	Models   []string  `json:"models,omitempty"`
	Weights  []float64 `json:"weights,omitempty"`
	UseCases []string  `json:"use_cases,omitempty"`
	Kinds    []string  `json:"kinds,omitempty"`
	Field    string    `json:"field,omitempty"`
	Value    *float64  `json:"value,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Local is the rule at team scope.
func (r Rule) Local() rules.Rule {
	l := rules.Rule{ID: r.ID, Kind: r.Kind, Target: r.Target, Models: r.Models, Weights: r.Weights, UseCases: r.UseCases,
		Kinds: r.Kinds, Field: r.Field, Note: r.Note, Scope: rules.ScopeTeam}
	if r.Value != nil {
		l.Value = *r.Value
	}
	return l
}

var teamID = regexp.MustCompile(`^t-[0-9a-f]{8}$`)

// FromLocal turns a rule from a rules file into a shared rule. Ids that are not team ids are dropped
// so the server fills them in; times, scope and approval limits are not part of a shared rule.
func FromLocal(l rules.Rule) (Rule, error) {
	if l.Expires != nil {
		return Rule{}, errors.New("team rules cannot expire")
	}
	if l.MaxStakes != 0 || l.MaxPerRunUSD != 0 || l.MaxCount != 0 || l.MaxTotalUSD != 0 || l.Period != "" {
		return Rule{}, fmt.Errorf("%s: team rules take no stakes or approval limits", l.Kind)
	}
	r := Rule{Kind: l.Kind, Target: l.Target, Models: l.Models, Weights: l.Weights, UseCases: l.UseCases, Kinds: l.Kinds,
		Field: l.Field, Note: l.Note}
	if teamID.MatchString(l.ID) {
		r.ID = l.ID
	}
	if l.Kind == rules.KindBudget {
		v := l.Value
		r.Value = &v
	}
	return r, nil
}

// String is the one line shown to members.
func (r Rule) String() string {
	s := r.Local()
	s.Scope = ""
	return s.String()
}

// Check validates shared rules as the server does: the client's rule checks, kinds a team may use,
// and only rules that tighten.
func Check(rs []Rule) error {
	if len(rs) > MaxRules {
		return fmt.Errorf("at most %d team rules", MaxRules)
	}
	for i, r := range rs {
		switch r.Kind {
		case rules.KindForbid, rules.KindOnly, rules.KindMix, rules.KindAsk, rules.KindBudget:
		default:
			return fmt.Errorf("rule %d: team rules are forbid, only, mix, ask or budget, not %q", i+1, r.Kind)
		}
		if r.Kind == rules.KindBudget && r.Value == nil {
			return fmt.Errorf("rule %d: budget needs a value", i+1)
		}
		l := r.Local()
		if err := l.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		if why := l.Loosens(); why != "" {
			return fmt.Errorf("rule %d: team rules can only tighten (%s)", i+1, why)
		}
		if len([]rune(r.Note)) > MaxNote {
			return fmt.Errorf("rule %d: note is longer than %d characters", i+1, MaxNote)
		}
		if r.ID != "" && !teamID.MatchString(r.ID) {
			return fmt.Errorf("rule %d: id %q is not a team rule id", i+1, r.ID)
		}
	}
	return nil
}

// Member is one member as the owner sees them.
type Member struct {
	AccountID       string   `json:"account_id"`
	Email           string   `json:"email,omitempty"`
	GitHubLogin     string   `json:"github_login,omitempty"`
	Role            string   `json:"role"`
	HasSeat         bool     `json:"has_seat"`
	MonthlyShareUSD *float64 `json:"monthly_share_usd"`
}

// Name is the member's email, else GitHub login, else account id.
func (m Member) Name() string {
	switch {
	case m.Email != "":
		return m.Email
	case m.GitHubLogin != "":
		return m.GitHubLogin
	}
	return m.AccountID
}

// Invite is an invite link; URL is only present when it was just made.
type Invite struct {
	ID        string `json:"id"`
	URL       string `json:"url,omitempty"`
	MaxUses   int    `json:"max_uses"`
	Uses      int    `json:"uses"`
	ExpiresAt string `json:"expires_at"`
}

// View is the answer of GET /v1/team. Members, invites and seats used are the owner's only.
type View struct {
	Org struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Seats  int    `json:"seats"`
		Active bool   `json:"active"`
	} `json:"org"`
	Role              string   `json:"role"`
	AccountID         string   `json:"account_id"`
	MonthlyShareUSD   *float64 `json:"monthly_share_usd"`
	DefaultMonthlyUSD *float64 `json:"default_monthly_usd,omitempty"`
	Rules             []Rule   `json:"rules"`
	SeatsUsed         int      `json:"seats_used,omitempty"`
	Members           []Member `json:"members,omitempty"`
	Invites           []Invite `json:"invites,omitempty"`
	DashboardURL      string   `json:"dashboard_url,omitempty"`
}

// Owner reports whether the caller owns the org.
func (v View) Owner() bool { return v.Role == "owner" }

// Dashboard is the answer of GET /v1/team/spend.
type Dashboard struct {
	Month   string `json:"month"`
	Members []struct {
		Member
		SpentUSD float64 `json:"spent_usd"`
		Runs     int     `json:"runs"`
	} `json:"members"`
	ByUseCase []struct {
		UseCase  string  `json:"use_case"`
		Model    string  `json:"model"`
		Route    string  `json:"route"`
		Runs     int     `json:"runs"`
		SpentUSD float64 `json:"spent_usd"`
	} `json:"by_use_case"`
	ByDay []struct {
		Day      string  `json:"day"`
		Runs     int     `json:"runs"`
		SpentUSD float64 `json:"spent_usd"`
	} `json:"by_day"`
	TotalUSD float64 `json:"total_usd"`
}

// Client calls the Team endpoints with this install's device token, read per request.
type Client struct {
	API  *account.Client
	Dirs paths.Dirs
}

// NewClient returns a client for the accounts API base URL.
func NewClient(d paths.Dirs, version string) *Client {
	return &Client{API: account.NewClient(version), Dirs: d}
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	tok := account.Token(c.Dirs)
	if tok == "" {
		return ErrNotLinked
	}
	_, err := c.API.Do(ctx, method, path, tok, in, out)
	return err
}

// Code is the server's error code of err, or "".
func Code(err error) string {
	var ae *account.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// gone reports the answers after which this account has no team to enforce or report to.
func gone(err error) bool {
	c := Code(err)
	return c == "not_in_team" || c == "not_entitled"
}

func (c *Client) Create(ctx context.Context, name string) (View, error) {
	var v View
	return v, c.call(ctx, "POST", "/v1/team", map[string]string{"name": name}, &v)
}

func (c *Client) Get(ctx context.Context) (View, error) {
	var v View
	return v, c.call(ctx, "GET", "/v1/team", nil, &v)
}

func (c *Client) Delete(ctx context.Context) error {
	return c.call(ctx, "DELETE", "/v1/team", nil, nil)
}

// Invite makes an invite link; zero leaves a field to the server's default.
func (c *Client) Invite(ctx context.Context, maxUses, expiresDays int) (Invite, error) {
	body := map[string]int{}
	if maxUses > 0 {
		body["max_uses"] = maxUses
	}
	if expiresDays > 0 {
		body["expires_days"] = expiresDays
	}
	var inv Invite
	return inv, c.call(ctx, "POST", "/v1/team/invites", body, &inv)
}

func (c *Client) RevokeInvite(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", "/v1/team/invites/"+url.PathEscape(id), nil, nil)
}

// Join joins with an invite secret or the whole invite URL.
func (c *Client) Join(ctx context.Context, invite string) (View, error) {
	var v View
	return v, c.call(ctx, "POST", "/v1/team/join", map[string]string{"invite": invite}, &v)
}

// RemoveMember removes a member, or leaves when accountID is the caller's own.
func (c *Client) RemoveMember(ctx context.Context, accountID string) error {
	return c.call(ctx, "DELETE", "/v1/team/members/"+url.PathEscape(accountID), nil, nil)
}

// PutRules replaces the shared rules and returns them with ids filled in.
func (c *Client) PutRules(ctx context.Context, rs []Rule) ([]Rule, error) {
	if rs == nil {
		rs = []Rule{}
	}
	var out struct {
		Rules []Rule `json:"rules"`
	}
	err := c.call(ctx, "PUT", "/v1/team/rules", map[string][]Rule{"rules": rs}, &out)
	return out.Rules, err
}

// Share is a budget share change: Set false clears it.
type Share struct {
	Set bool
	USD float64
}

func (s Share) value() any {
	if !s.Set {
		return nil
	}
	return s.USD
}

// SetBudget changes the default share (when def is not nil) and members' shares.
func (c *Client) SetBudget(ctx context.Context, def *Share, members map[string]Share) (View, error) {
	body := map[string]any{}
	if def != nil {
		body["default_monthly_usd"] = def.value()
	}
	if len(members) > 0 {
		m := map[string]any{}
		for id, s := range members {
			m[id] = s.value()
		}
		body["members"] = m
	}
	var v View
	return v, c.call(ctx, "PUT", "/v1/team/budget", body, &v)
}

// Spend is the owner's dashboard data for a month ("" for the current one).
func (c *Client) Spend(ctx context.Context, month string) (Dashboard, error) {
	p := "/v1/team/spend"
	if month != "" {
		p += "?month=" + url.QueryEscape(month)
	}
	var d Dashboard
	return d, c.call(ctx, "GET", p, nil, &d)
}
