package team

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"p0ssess/internal/account"
)

// ModelSpend is one model's spend in a month and, in the current month, today.
type ModelSpend struct {
	Model    string  `json:"model"`
	Runs     int     `json:"runs"`
	SpentUSD float64 `json:"spent_usd"`
	TodayUSD float64 `json:"today_usd"`
}

// Models is spend per model, most first. An older server sends no by_model, so it is summed from
// the use-case rows, without today.
func (d Dashboard) Models() []ModelSpend {
	if len(d.ByModel) > 0 {
		return d.ByModel
	}
	idx := map[string]int{}
	var out []ModelSpend
	for _, u := range d.ByUseCase {
		i, ok := idx[u.Model]
		if !ok {
			i = len(out)
			idx[u.Model] = i
			out = append(out, ModelSpend{Model: u.Model})
		}
		out[i].Runs += u.Runs
		out[i].SpentUSD += u.SpentUSD
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].SpentUSD > out[b].SpentUSD })
	return out
}

// Explain words the server's Team error codes for people; other errors pass through.
func Explain(err error) error {
	var ae *account.APIError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Code {
	case "not_in_team":
		return errors.New("you are not in a team")
	case "not_owner":
		return errors.New("only the team's owner can do that")
	case "already_in_team":
		return errors.New("this account is already in a team")
	case "seats_full":
		return errors.New("the team has no free seat; its owner can add seats")
	case "invite_invalid":
		return errors.New("that invite is unknown, used up, expired or revoked")
	case "invite_mail_limit":
		return errors.New("too many invite emails today; make an invite link with p0ssess team invite instead")
	case "not_entitled":
		return errors.New("this needs an active Team subscription")
	case "invalid_token":
		return errors.New("this install's link was revoked; run p0ssess account link")
	case "bad_request":
		if ae.Message != "" {
			return fmt.Errorf("the server refused it: %s", ae.Message)
		}
	}
	return err
}

// USD shows a share, or "none".
func USD(v *float64) string {
	if v == nil {
		return "none"
	}
	return fmt.Sprintf("$%.2f", *v)
}

// Spent shows dollars to the cent, and a sub-cent amount as "<$0.01" so a real run never reads as $0.00.
func Spent(v float64) string {
	if v > 0 && v < 0.005 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

// Day shows an RFC 3339 time as a date.
func Day(rfc string) string {
	if tm, err := time.Parse(time.RFC3339, rfc); err == nil {
		return tm.UTC().Format("Jan 2, 2006")
	}
	return rfc
}

// WriteRules lists rules, one per line with its id.
func WriteRules(w io.Writer, rs []Rule) {
	if len(rs) == 0 {
		fmt.Fprintf(w, "No team rules.\n")
		return
	}
	fmt.Fprintf(w, "Rules:\n")
	for _, r := range rs {
		line := fmt.Sprintf("  %s  %s", r.ID, r.String())
		if r.Note != "" {
			line += "  (" + r.Note + ")"
		}
		fmt.Fprintf(w, "%s\n", line)
	}
}

// WriteStatus is the team at a glance: role, seats, shares, members, pending invites and rules.
func WriteStatus(w io.Writer, v View) {
	fmt.Fprintf(w, "Team %s (%s); you are its %s\n", v.Org.Name, v.Org.ID, v.Role)
	if !v.Org.Active {
		fmt.Fprintf(w, "The team has no active subscription: its rules and shares are not in force.\n")
	}
	if v.Owner() {
		free := v.Org.Seats - len(v.Members)
		if free < 0 {
			free = 0
		}
		fmt.Fprintf(w, "Seats: %d of %d used, %d free, %d pending invites\n", v.SeatsUsed, v.Org.Seats, free, len(v.Invites))
		fmt.Fprintf(w, "Default monthly share: %s\n", USD(v.DefaultMonthlyUSD))
	}
	fmt.Fprintf(w, "Your monthly share: %s\n", USD(v.MonthlyShareUSD))
	if len(v.Members) > 0 {
		fmt.Fprintf(w, "Members:\n")
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for _, m := range v.Members {
			share := "default"
			if m.MonthlyShareUSD != nil {
				share = USD(m.MonthlyShareUSD)
			}
			seat := ""
			if !m.HasSeat {
				seat = "no seat"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\tshare %s\t%s\n", m.Name(), m.AccountID, m.Role, share, seat)
		}
		tw.Flush()
	}
	if len(v.Invites) > 0 {
		fmt.Fprintf(w, "Pending invites:\n")
		for _, inv := range v.Invites {
			who := "link"
			if inv.Email != "" {
				who = inv.Email
			}
			fmt.Fprintf(w, "  %s  %s  %d of %d used, expires %s\n", inv.ID, who, inv.Uses, inv.MaxUses, Day(inv.ExpiresAt))
		}
	}
	WriteRules(w, v.Rules)
	if v.DashboardURL != "" {
		fmt.Fprintf(w, "Dashboard: %s\n", v.DashboardURL)
	}
}

// WriteSpend is the month's spend by member, model and day; today's column appears when the server
// sends it (the current month).
func WriteSpend(w io.Writer, d Dashboard, full bool) error {
	today := d.Today != ""
	if today {
		fmt.Fprintf(w, "Team spend %s: %s this month, %s today (%s UTC)\n", d.Month, Spent(d.TotalUSD), Spent(d.TodayUSD), d.Today)
	} else {
		fmt.Fprintf(w, "Team spend %s: %s\n", d.Month, Spent(d.TotalUSD))
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	row := func(cells ...string) {
		fmt.Fprintln(tw, strings.Join(cells, "\t")+"\t")
	}
	if len(d.Members) > 0 {
		fmt.Fprintln(tw)
		if today {
			row("Member", "Role", "Today", "Month", "Share", "Runs")
		} else {
			row("Member", "Role", "Month", "Share", "Runs")
		}
		for _, m := range d.Members {
			if today {
				row(m.Name(), m.Role, Spent(m.TodayUSD), Spent(m.SpentUSD), USD(m.MonthlyShareUSD), fmt.Sprint(m.Runs))
			} else {
				row(m.Name(), m.Role, Spent(m.SpentUSD), USD(m.MonthlyShareUSD), fmt.Sprint(m.Runs))
			}
		}
	}
	if ms := d.Models(); len(ms) > 0 {
		fmt.Fprintln(tw)
		withToday := today && len(d.ByModel) > 0
		if withToday {
			row("Model", "Today", "Month", "Runs")
		} else {
			row("Model", "Month", "Runs")
		}
		for _, m := range ms {
			if withToday {
				row(m.Model, Spent(m.TodayUSD), Spent(m.SpentUSD), fmt.Sprint(m.Runs))
			} else {
				row(m.Model, Spent(m.SpentUSD), fmt.Sprint(m.Runs))
			}
		}
	}
	if full && len(d.ByUseCase) > 0 {
		fmt.Fprintln(tw)
		row("Use case", "Model", "Route", "Runs", "Spent")
		for _, u := range d.ByUseCase {
			uc := u.UseCase
			if uc == "" {
				uc = "(none)"
			}
			row(uc, u.Model, u.Route, fmt.Sprint(u.Runs), Spent(u.SpentUSD))
		}
	}
	if full && len(d.ByDay) > 0 {
		fmt.Fprintln(tw)
		row("Day", "Runs", "Spent")
		for _, dd := range d.ByDay {
			row(dd.Day, fmt.Sprint(dd.Runs), Spent(dd.SpentUSD))
		}
	}
	return tw.Flush()
}

// SeatHint is a line for the owner when pending invites outnumber the free seats, else "".
func SeatHint(v View) string {
	if !v.Owner() {
		return ""
	}
	free := v.Org.Seats - len(v.Members)
	if free < 0 {
		free = 0
	}
	if len(v.Invites) <= free {
		return ""
	}
	return fmt.Sprintf("%d pending invites but %d free seats: add seats on the team page so everyone can join.", len(v.Invites), free)
}
