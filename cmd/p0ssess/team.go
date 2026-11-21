package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
	"p0ssess/internal/team"
)

const teamUsage = `usage: p0ssess team COMMAND
  create NAME                      make a team with you as its owner
  status                           the team, your role and share, and the rules
  invite EMAIL                     email someone a one-use join link (owner)
  invite [--uses N] [--days D]     a new invite link (owner)
  invite revoke ID                 revoke an invite link (owner)
  join INVITE_OR_URL               join with an invite
  leave                            leave the team
  remove ACCOUNT_ID                remove a member (owner)
  rules                            the team's rules
  rules set FILE                   replace the rules from a TOML ([[rule]], as rules.toml) or JSON ({"rules":[...]}) file (owner)
  rule add --kind K [--target T] [--models A,B] [--weights 1,2] [--use-cases U] [--kinds K] [--field F] [--value V] [--note N]   (owner)
  rule remove ID                   (owner)
  budget USD|none                  the team's monthly budget: paid runs stop for every seat at it (owner)
  share default USD|none           the monthly share of members without their own (owner)
  share ACCOUNT_ID USD|none        one member's monthly share (owner)
  spend [--month YYYY-MM]          spend per member, model, use case and day, with today (owner)
  sync                             fetch the team's policy now and send the spend queue
  delete --yes                     delete the team, its members, invites, rules and spend (owner)`

// teamKeys are the keys a policy must be signed with; tests replace them.
var teamKeys = table.PinnedKeys

func teamCmd(ctx context.Context, args []string) error {
	return runTeam(ctx, args, os.Stdout)
}

func runTeam(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(teamUsage)
	}
	d := paths.Default()
	c := team.NewClient(d, version)
	t := &teamCLI{d: d, c: c, out: out}
	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "create":
		err = t.create(ctx, rest)
	case "status":
		err = t.status(ctx)
	case "invite":
		err = t.invite(ctx, rest)
	case "join":
		err = t.join(ctx, rest)
	case "leave":
		err = t.leave(ctx)
	case "remove":
		err = t.remove(ctx, rest)
	case "rules":
		if len(rest) > 0 && rest[0] == "set" {
			err = t.rulesSet(ctx, rest[1:])
		} else if len(rest) == 0 {
			err = t.rules(ctx)
		} else {
			err = errors.New("usage: p0ssess team rules [set FILE]")
		}
	case "rule":
		err = t.rule(ctx, rest)
	case "share":
		err = t.share(ctx, rest)
	case "budget":
		err = t.budget(ctx, rest)
	case "spend":
		err = t.spend(ctx, rest)
	case "sync":
		err = t.sync(ctx)
	case "delete":
		err = t.delete(ctx, rest)
	default:
		return fmt.Errorf("team: unknown command %q\n%s", cmd, teamUsage)
	}
	return teamErr(err)
}

// teamErr words the server's Team error codes.
func teamErr(err error) error { return team.Explain(err) }

type teamCLI struct {
	d   paths.Dirs
	c   *team.Client
	out io.Writer
}

func (t *teamCLI) printf(format string, a ...any) { fmt.Fprintf(t.out, format, a...) }

func usd(v *float64) string { return team.USD(v) }

// refresh re-reads the account (its org changes with create, join and leave) and syncs the policy.
func (t *teamCLI) refresh(ctx context.Context) (team.Result, error) {
	if _, err := account.Refresh(ctx, t.d, t.c.API); err != nil {
		return team.Result{Policy: team.Load(t.d, teamKeys())}, err
	}
	return team.Sync(ctx, t.c, teamKeys())
}

func (t *teamCLI) create(ctx context.Context, args []string) error {
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		return errors.New("usage: p0ssess team create NAME")
	}
	v, err := t.c.Create(ctx, name)
	if err != nil {
		return err
	}
	t.refresh(ctx)
	t.printf("Created team %s (%s); you are its owner.\n", v.Org.Name, v.Org.ID)
	t.printf("Seats come with the Team subscription: %s/team/checkout\n", account.BaseURL())
	t.printf("Invite members with: p0ssess team invite\n")
	return nil
}

func (t *teamCLI) status(ctx context.Context) error {
	v, err := t.c.Get(ctx)
	if team.Code(err) == "not_in_team" {
		t.printf("Not in a team.\n")
		return nil
	}
	if err != nil {
		return err
	}
	team.WriteStatus(t.out, v)
	return nil
}

func day(rfc string) string { return team.Day(rfc) }

func (t *teamCLI) printRules(rs []team.Rule) { team.WriteRules(t.out, rs) }

func (t *teamCLI) invite(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "revoke" {
		if len(args) != 2 {
			return errors.New("usage: p0ssess team invite revoke ID")
		}
		if err := t.c.RevokeInvite(ctx, args[1]); err != nil {
			return err
		}
		t.printf("Revoked %s.\n", args[1])
		return nil
	}
	if len(args) == 1 && strings.Contains(args[0], "@") {
		inv, err := t.c.InviteEmail(ctx, args[0])
		if err != nil {
			return err
		}
		if inv.Emailed != nil && !*inv.Emailed {
			t.printf("The invite email to %s could not be sent. Send them this link (works once, until %s):\n%s\n", inv.Email, day(inv.ExpiresAt), inv.URL)
			return nil
		}
		t.printf("Invited %s. The email's join link works once, until %s.\n", inv.Email, day(inv.ExpiresAt))
		t.seatHint(ctx)
		return nil
	}
	fs := flag.NewFlagSet("team invite", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uses := fs.Int("uses", 0, "how many people may join with it (1-100, default 1)")
	days := fs.Int("days", 0, "days until it expires (1-30, default 7)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: p0ssess team invite [--uses N] [--days D]: %w", err)
	}
	if *uses < 0 || *uses > 100 || *days < 0 || *days > 30 {
		return errors.New("--uses is 1 to 100 and --days 1 to 30")
	}
	inv, err := t.c.Invite(ctx, *uses, *days)
	if err != nil {
		return err
	}
	t.printf("%s\n", inv.URL)
	n := inv.MaxUses
	plural := "s"
	if n == 1 {
		plural = ""
	}
	t.printf("Good for %d join%s until %s. It is shown once; share it with the people you invite.\n", n, plural, day(inv.ExpiresAt))
	return nil
}

// seatHint warns when pending invites outnumber the free seats, so the owner knows someone cannot join yet.
func (t *teamCLI) seatHint(ctx context.Context) {
	if v, err := t.c.Get(ctx); err == nil {
		if line := team.SeatHint(v); line != "" {
			t.printf("%s\n", line)
		}
	}
}

func (t *teamCLI) join(ctx context.Context, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("usage: p0ssess team join INVITE_OR_URL")
	}
	v, err := t.c.Join(ctx, strings.TrimSpace(args[0]))
	if err != nil {
		return err
	}
	t.printf("Joined %s as a %s.\n", v.Org.Name, v.Role)
	res, err := t.refresh(ctx)
	t.printPolicy(res.Policy)
	if err != nil {
		t.printf("The team's rules could not be fetched yet (%v); they arrive within minutes.\n", teamErr(err))
	}
	return nil
}

func (t *teamCLI) ownID(ctx context.Context) (string, error) {
	account.Refresh(ctx, t.d, t.c.API)
	if id := account.Current(t.d).AccountID; id != "" {
		return id, nil
	}
	v, err := t.c.Get(ctx)
	if err != nil {
		return "", err
	}
	if v.AccountID == "" {
		return "", errors.New("could not tell which account this install is linked to")
	}
	return v.AccountID, nil
}

func (t *teamCLI) leave(ctx context.Context) error {
	id, err := t.ownID(ctx)
	if err != nil {
		return err
	}
	if err := t.c.RemoveMember(ctx, id); err != nil {
		return err
	}
	team.Forget(t.d)
	account.Refresh(ctx, t.d, t.c.API)
	t.printf("Left the team. Its rules no longer apply here.\n")
	return nil
}

func (t *teamCLI) remove(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: p0ssess team remove ACCOUNT_ID")
	}
	if err := t.c.RemoveMember(ctx, args[0]); err != nil {
		return err
	}
	t.printf("Removed %s.\n", args[0])
	return nil
}

func (t *teamCLI) rules(ctx context.Context) error {
	v, err := t.c.Get(ctx)
	if err != nil {
		return err
	}
	t.printRules(v.Rules)
	return nil
}

// readRules reads a TOML file in the rules.toml format or a JSON {"rules":[...]} file.
func readRules(path string) ([]team.Rule, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(path), ".json") || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		var in struct {
			Rules []team.Rule `json:"rules"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return in.Rules, nil
	}
	ls, err := rules.ReadFile(path, rules.ScopeTeam)
	if err != nil {
		return nil, err
	}
	out := make([]team.Rule, 0, len(ls))
	for i, l := range ls {
		r, err := team.FromLocal(l)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (t *teamCLI) put(ctx context.Context, rs []team.Rule) ([]team.Rule, error) {
	if err := team.Check(rs); err != nil {
		return nil, err
	}
	saved, err := t.c.PutRules(ctx, rs)
	if err != nil {
		return nil, err
	}
	t.refresh(ctx)
	return saved, nil
}

func (t *teamCLI) rulesSet(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: p0ssess team rules set FILE")
	}
	rs, err := readRules(args[0])
	if err != nil {
		return err
	}
	saved, err := t.put(ctx, rs)
	if err != nil {
		return err
	}
	t.printf("Saved %d team rules.\n", len(saved))
	t.printRules(saved)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (t *teamCLI) rule(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: p0ssess team rule add|remove ...")
	}
	switch args[0] {
	case "add":
		r, err := ruleFlags(args[1:])
		if err != nil {
			return err
		}
		v, err := t.c.Get(ctx)
		if err != nil {
			return err
		}
		saved, err := t.put(ctx, append(v.Rules, r))
		if err != nil {
			return err
		}
		if len(saved) > 0 {
			n := saved[len(saved)-1]
			t.printf("Added %s: %s\n", n.ID, n.String())
		}
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: p0ssess team rule remove ID")
		}
		v, err := t.c.Get(ctx)
		if err != nil {
			return err
		}
		var keep []team.Rule
		var gone *team.Rule
		for i, r := range v.Rules {
			if r.ID == args[1] {
				gone = &v.Rules[i]
				continue
			}
			keep = append(keep, r)
		}
		if gone == nil {
			return fmt.Errorf("no team rule %s", args[1])
		}
		if _, err := t.put(ctx, keep); err != nil {
			return err
		}
		t.printf("Removed %s: %s\n", gone.ID, gone.String())
		return nil
	}
	return fmt.Errorf("team rule: unknown command %q", args[0])
}

func ruleFlags(args []string) (team.Rule, error) {
	fs := flag.NewFlagSet("team rule add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kind := fs.String("kind", "", "forbid, only, mix, ask or budget")
	target := fs.String("target", "", "forbid: model, claude_agents, opencode or delegation")
	models := fs.String("models", "", "comma-separated OpenRouter model ids")
	weights := fs.String("weights", "", "mix: comma-separated weights, one per model")
	useCases := fs.String("use-cases", "", "comma-separated use-case ids")
	kinds := fs.String("kinds", "", "comma-separated kinds: media, generate, agent, review, code, text")
	field := fs.String("field", "", "budget: per_task_usd, daily_usd or monthly_usd")
	value := fs.String("value", "", "budget: the limit in USD")
	note := fs.String("note", "", "a note members see")
	if err := fs.Parse(args); err != nil {
		return team.Rule{}, fmt.Errorf("team rule add: %w", err)
	}
	if fs.NArg() > 0 {
		return team.Rule{}, fmt.Errorf("team rule add: unexpected %q", fs.Arg(0))
	}
	r := team.Rule{Kind: *kind, Target: *target, Models: splitList(*models), UseCases: splitList(*useCases),
		Kinds: splitList(*kinds), Field: *field, Note: *note}
	for _, w := range splitList(*weights) {
		f, err := strconv.ParseFloat(w, 64)
		if err != nil {
			return team.Rule{}, fmt.Errorf("weight %q is not a number", w)
		}
		r.Weights = append(r.Weights, f)
	}
	if *value != "" {
		f, err := strconv.ParseFloat(strings.TrimPrefix(*value, "$"), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return team.Rule{}, fmt.Errorf("value %q is not a number", *value)
		}
		r.Value = &f
	}
	if r.Kind == "" {
		return team.Rule{}, errors.New("team rule add needs --kind")
	}
	return r, nil
}

func parseShare(s string) (team.Share, error) {
	if strings.EqualFold(s, "none") {
		return team.Share{}, nil
	}
	f, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
	if err != nil || !(f >= 0) || math.IsInf(f, 0) {
		return team.Share{}, fmt.Errorf("%q is not an amount in USD (0 or more) or none", s)
	}
	return team.Share{Set: true, USD: f}, nil
}

func (s shareText) String() string {
	if !s.Set {
		return "none"
	}
	return fmt.Sprintf("$%.2f", s.USD)
}

type shareText team.Share

func (t *teamCLI) share(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: p0ssess team share default|ACCOUNT_ID USD|none")
	}
	sh, err := parseShare(args[1])
	if err != nil {
		return err
	}
	if args[0] == "default" {
		if _, err := t.c.SetBudget(ctx, &sh, nil); err != nil {
			return err
		}
		t.printf("Default monthly share: %s\n", shareText(sh))
	} else {
		if _, err := t.c.SetBudget(ctx, nil, map[string]team.Share{args[0]: sh}); err != nil {
			return err
		}
		if sh.Set {
			t.printf("Monthly share of %s: %s\n", args[0], shareText(sh))
		} else {
			t.printf("%s now has the default share.\n", args[0])
		}
	}
	t.refresh(ctx)
	return nil
}

func (t *teamCLI) budget(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: p0ssess team budget USD|none")
	}
	sh, err := parseShare(args[0])
	if err != nil {
		return err
	}
	if _, err := t.c.SetTeamBudget(ctx, sh); err != nil {
		return err
	}
	if sh.Set {
		t.printf("Team budget: %s a month. Paid runs stop for every seat once the team reaches it.\n", shareText(sh))
	} else {
		t.printf("No team budget; each seat keeps its own share.\n")
	}
	t.refresh(ctx)
	return nil
}

func (t *teamCLI) spend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("team spend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	month := fs.String("month", "", "YYYY-MM, default this month (UTC)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: p0ssess team spend [--month YYYY-MM]: %w", err)
	}
	if *month != "" {
		if _, err := time.Parse("2006-01", *month); err != nil {
			return fmt.Errorf("month %q is not YYYY-MM", *month)
		}
	}
	db, err := t.c.Spend(ctx, *month)
	if err != nil {
		return err
	}
	return team.WriteSpend(t.out, db, true)
}

func (t *teamCLI) printPolicy(p *team.Policy) {
	if p == nil {
		t.printf("No team policy in force.\n")
		return
	}
	t.printf("Team %s: you are its %s.\n", p.OrgName, p.Role)
	t.printf("Monthly share: %s\n", usd(p.MonthlyShareUSD))
	t.printRules(p.Rules)
	if p.ReportSpend {
		t.printf("Each paid run's day, use case, model, route and cost is reported to the team.\n")
	}
}

func (t *teamCLI) sync(ctx context.Context) error {
	if account.Token(t.d) == "" {
		return team.ErrNotLinked
	}
	res, err := t.refresh(ctx)
	if res.Left {
		t.printf("Not in a team; no team rules apply here.\n")
		return nil
	}
	t.printPolicy(res.Policy)
	if res.Sent > 0 {
		t.printf("Sent %d paid runs to the team.\n", res.Sent)
	}
	if q := len(team.Queued(t.d)); q > 0 {
		t.printf("%d paid runs wait to be sent.\n", q)
	}
	if err != nil {
		return fmt.Errorf("sync failed (%v); the last policy fetched stays in force", teamErr(err))
	}
	return nil
}

func (t *teamCLI) delete(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "--yes" {
		return errors.New("p0ssess team delete --yes deletes the team with its members, invites, rules, shares and spend; it cannot be undone")
	}
	if err := t.c.Delete(ctx); err != nil {
		return err
	}
	team.Forget(t.d)
	account.Refresh(ctx, t.d, t.c.API)
	t.printf("Deleted the team.\n")
	return nil
}

func spent(v float64) string { return team.Spent(v) }
