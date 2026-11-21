package me

import (
	"encoding/base64"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"p0ssess/internal/account"
	"p0ssess/internal/notices"
	"p0ssess/internal/savings"
)

// proofView is the page's Savings section: this month, last month and all time rolled up from the
// receipts on this machine, and what the monthly report holds.
type proofView struct {
	savings.Report
	Monthly reportView `json:"monthly"`
}

// reportView is the monthly report's state: whether it applies to this install, and the figures exactly
// as they will be sent.
type reportView struct {
	Pro     bool   `json:"pro"`
	Linked  bool   `json:"linked"`
	Enabled bool   `json:"enabled"`
	Month   string `json:"month"`
	Pending string `json:"pending,omitempty"`
}

func (s *Server) proof(w http.ResponseWriter, r *http.Request) {
	now := s.E.Now()
	recs := notices.Receipts(s.E.Dirs.NoticesFile())
	v := proofView{Report: savings.Build(recs, now)}
	b, _ := s.E.Budget()
	v.Monthly = reportView{Linked: account.Token(s.E.Dirs) != "", Enabled: b.Report.Send, Month: savings.Last(now)}
	v.Monthly.Pro = v.Monthly.Linked && account.Current(s.E.Dirs).Pro()
	if v.Monthly.Pro && v.Monthly.Enabled {
		if m, ok := savings.Pending(s.E.Dirs, recs, now); ok {
			v.Monthly.Pending = m.Body()
		}
	}
	writeJSON(w, http.StatusOK, v)
}

var cardMonthRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// savingsCard serves the shareable card as an SVG, with Obel Mono embedded so it looks the same wherever
// it is opened. ?month=YYYY-MM picks a month, ?month=all everything; the default is this month.
func (s *Server) savingsCard(w http.ResponseWriter, r *http.Request) {
	now := s.E.Now()
	m := r.URL.Query().Get("month")
	switch {
	case m == "all":
		m = ""
	case m == "":
		m = savings.Month(now, now.Location())
	case !cardMonthRe.MatchString(m):
		http.Error(w, "month must be YYYY-MM or all", http.StatusBadRequest)
		return
	}
	p := savings.Roll(notices.Receipts(s.E.Dirs.NoticesFile()), m, now.Location())
	name := "p0ssess-savings-" + p.Label + ".svg"
	w.Header().Set("Content-Type", "image/svg+xml")
	// The card styles itself and carries its font as data, so opened on its own it needs these two.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src data:; sandbox")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	w.Write([]byte(SavingsCardSVG(p)))
}

// The card's colors are the page's: AA on the ground (white 19:1, body 11:1, quiet 5.6:1, violet ink 7:1).
const (
	cardGround = "#0b0b0b"
	cardLine   = "#2a2a2a"
	cardInk    = "#ffffff"
	cardBody   = "#c9c9c9"
	cardQuiet  = "#8a8a8a"
	cardViolet = "#7457e0"
	cardVioInk = "#a593f2"
)

// SavingsCardSVG draws a period as a 1200 by 630 card: the headline figure, the totals, the top models and
// what they are measured against. It claims no more than the figures do.
func SavingsCardSVG(p savings.Period) string {
	t := p.Totals
	var font string
	if b, err := fs.ReadFile(web, "web/fonts/obel-mono.woff2"); err == nil {
		font = base64.StdEncoding.EncodeToString(b)
	}
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630" role="img" aria-label="p0ssess savings, ` + e(savings.PeriodTitle(p.Label)) + `">`)
	b.WriteString(`<style>@font-face{font-family:"Obel Mono";src:url(data:font/woff2;base64,` + font + `) format("woff2");font-weight:400 700}` +
		`text{font-family:"Obel Mono",ui-monospace,Menlo,Consolas,monospace}.q{fill:` + cardQuiet + `;font-size:22px}.b{fill:` + cardBody + `;font-size:26px}` +
		`.i{fill:` + cardInk + `;font-size:26px}.h{fill:` + cardVioInk + `;font-weight:700}</style>`)
	b.WriteString(`<rect width="1200" height="630" fill="` + cardGround + `"/>`)
	// The hairline grid: rules every 90 px, each starting with 24 px of violet.
	for y := 90; y < 630; y += 90 {
		fmt.Fprintf(&b, `<path d="M0 %d H1200" stroke="%s" stroke-width="1"/><path d="M0 %d H24" stroke="%s" stroke-width="1"/>`, y, cardLine, y, cardViolet)
	}
	for x := 120; x < 1200; x += 120 {
		fmt.Fprintf(&b, `<path d="M%d 0 V630" stroke="%s" stroke-width="1"/>`, x, cardLine)
	}
	b.WriteString(`<rect x="48" y="36" width="1104" height="558" fill="` + cardGround + `" opacity="0.92"/>`)
	b.WriteString(`<text x="64" y="86" class="i" font-size="30" font-weight="700">p<tspan fill="` + cardViolet + `">0</tspan>ssess</text>`)
	b.WriteString(`<text x="1136" y="86" class="q" text-anchor="end">` + e(savings.PeriodTitle(p.Label)) + `</text>`)

	h := savings.HeadlineOf(t)
	big, sub := "no saving", ""
	switch h.Kind {
	case "saved":
		big = "~" + savings.Money(h.Amount)
		sub = "saved against Claude"
		if pct := savings.SharePct(t); pct > 0 {
			sub += fmt.Sprintf(" (%d%%)", pct)
		}
	case "worse":
		sub = "Claude would have cost " + savings.Money(h.Amount) + " less"
	default:
		sub = "nothing to claim yet"
		if t.Runs > 0 && t.Priced > 0 {
			sub = "no saving this period"
		}
	}
	class := "h"
	if h.Kind != "saved" {
		class = "i"
	}
	fmt.Fprintf(&b, `<text x="64" y="238" class="%s" font-size="104" font-weight="700">%s</text>`, class, e(big))
	b.WriteString(`<text x="64" y="288" class="b">` + e(sub) + `</text>`)

	stat := func(x, y int, label, val string) {
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="q">%s</text><text x="%d" y="%d" class="i" font-weight="700">%s</text>`, x, y, e(label), x, y+34, e(val))
	}
	stat(64, 366, "runs", fmt.Sprint(t.Runs))
	stat(64, 456, "cost", savings.Money(t.CostUSD))
	if t.Priced > 0 {
		stat(264, 366, "Claude would have cost", savings.Money(t.ClaudeUSD))
	}
	if t.TokensKept > 0 {
		stat(264, 456, "Claude usage kept", savings.TokenCount(t.TokensKept)+" tokens")
	}

	b.WriteString(`<path d="M700 130 V470" stroke="` + cardLine + `"/>`)
	b.WriteString(`<text x="740" y="170" class="q">by model</text>`)
	for i, r := range p.ByModel {
		if i == 5 {
			break
		}
		y := 216 + i*52
		fmt.Fprintf(&b, `<text x="740" y="%d" class="b">%s</text><text x="1136" y="%d" class="i" text-anchor="end" font-weight="700">%s</text>`,
			y, e(clip(r.Key, 17)), y, e(cardSaved(r.Totals)))
	}

	basis := savings.Basis(t)
	if basis == "" {
		basis = "no runs measured yet"
	}
	b.WriteString(`<text x="64" y="548" class="q" font-size="20">` + e(basis) + `</text>`)
	b.WriteString(`<text x="64" y="578" class="q" font-size="18">Priced at Claude's list price for the same tokens.</text>`)
	b.WriteString(`<text x="1136" y="548" class="q" font-size="20" text-anchor="end">p0ssess.me</text>`)
	b.WriteString(`</svg>`)
	return b.String()
}

func cardSaved(t savings.Totals) string {
	switch h := savings.HeadlineOf(t); h.Kind {
	case "saved":
		return "~" + savings.Money(h.Amount)
	case "worse":
		return "-" + savings.Money(h.Amount)
	}
	return "none"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
