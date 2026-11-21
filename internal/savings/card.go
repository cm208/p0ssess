package savings

import (
	"fmt"
	"strings"
	"time"

	"p0ssess/internal/card"
)

// KindName is a kind of work in words.
func KindName(k string) string {
	switch k {
	case "agent-edit":
		return "agent edits"
	case "agent-read":
		return "agent reading"
	case "agent":
		return "agent work"
	case "", "other":
		return "other"
	}
	return strings.ReplaceAll(k, "-", " ")
}

// Money prints dollars: cents above a dime, more places below so a tiny amount does not read as zero.
func Money(v float64) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	switch {
	case v >= 100:
		return fmt.Sprintf("%s$%.0f", neg, v)
	case v >= 0.1 || v == 0:
		return fmt.Sprintf("%s$%.2f", neg, v)
	case v >= 0.001:
		return fmt.Sprintf("%s$%.3f", neg, v)
	}
	return neg + "<$0.001"
}

// TokenCount prints a token count as 340, 12.4k or 1.2M.
func TokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// HeadlineText is the period's headline in one sentence; it never claims more than the figures show.
func HeadlineText(t Totals) string {
	h := HeadlineOf(t)
	switch h.Kind {
	case "saved":
		s := "~" + Money(h.Amount) + " saved against Claude"
		if p := SharePct(t); p > 0 {
			s += fmt.Sprintf(" (%d%%)", p)
		}
		return s
	case "worse":
		return "no saving: Claude would have cost " + Money(h.Amount) + " less"
	}
	if t.Priced == 0 && t.Runs > 0 {
		return "no saving to show yet: no run had token counts"
	}
	if t.Runs == 0 {
		return "no runs yet"
	}
	return "no saving this period"
}

// Basis is how the figures were arrived at, in words.
func Basis(t Totals) string {
	var ps []string
	if t.Measured > 0 {
		ps = append(ps, fmt.Sprintf("%d measured", t.Measured))
	}
	if t.Estimated > 0 {
		ps = append(ps, fmt.Sprintf("%d estimated", t.Estimated))
	}
	if t.Unpriced > 0 {
		ps = append(ps, fmt.Sprintf("%d not priced", t.Unpriced))
	}
	if len(ps) == 0 {
		return ""
	}
	return strings.Join(ps, " · ") + " (runs)"
}

// PeriodTitle is "November 2026" for a month key, "all time" for the rest.
func PeriodTitle(label string) string {
	if t, err := time.Parse("2006-01", label); err == nil {
		return t.Format("January 2006")
	}
	return "all time"
}

// Card draws a period as the terminal card.
func Card(p Period) string {
	const w = card.Width
	in := w - 4
	t := p.Totals
	rows := []string{HeadlineText(t)}
	if t.Runs == 0 {
		return card.Card{Title: "p0ssess savings", Right: PeriodTitle(p.Label), Rows: rows, W: w}.String()
	}
	line := fmt.Sprintf("%d runs · cost %s", t.Runs, Money(t.CostUSD))
	if t.Priced > 0 {
		line += " · Claude would have cost " + Money(t.ClaudeUSD)
	}
	rows = append(rows, line)
	if t.ClaudeCheaper > 0 {
		rows = append(rows, fmt.Sprintf("Claude was cheaper on %d of %d priced runs", t.ClaudeCheaper, t.Priced))
	}
	section := func(name string, rs []Row, name2 func(string) string) {
		if len(rs) == 0 {
			return
		}
		rows = append(rows, "", name)
		for i, r := range rs {
			if i == 5 {
				rows = append(rows, card.Spread(fmt.Sprintf("  and %d more", len(rs)-i), "", in))
				break
			}
			rows = append(rows, "  "+card.Pair(card.Clip(name2(r.Key), 24), fmt.Sprintf("%3d runs   %s", r.Runs, saved(r.Totals)), 26))
		}
	}
	section("by model", p.ByModel, func(s string) string { return s })
	section("by kind of work", p.ByKind, KindName)
	if p.Rescue.Runs > 0 {
		rows = append(rows, "", "rescue   "+fmt.Sprintf("%d runs · %s", p.Rescue.Runs, saved(p.Rescue)),
			"normal   "+fmt.Sprintf("%d runs · %s", p.Normal.Runs, saved(p.Normal)))
	}
	if t.TokensKept > 0 {
		rows = append(rows, "", "Claude usage kept  "+TokenCount(t.TokensKept)+" tokens")
	}
	if b := Basis(t); b != "" {
		rows = append(rows, "", b)
	}
	return card.Card{Title: "p0ssess savings", Right: PeriodTitle(p.Label), Rows: rows, W: w}.String()
}

func saved(t Totals) string {
	switch h := HeadlineOf(t); h.Kind {
	case "saved":
		return "~" + Money(h.Amount) + " saved"
	case "worse":
		return Money(h.Amount) + " more than Claude"
	}
	if t.Priced == 0 {
		return "not priced"
	}
	return "no saving"
}
