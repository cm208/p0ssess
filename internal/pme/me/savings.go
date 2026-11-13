package me

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"p0ssess/internal/calibrate"
	"p0ssess/internal/pme/me/pdf"
)

// Copy shared by the page and the PDF; app.js carries the same sentences.
const (
	copyNotPro     = "The savings report is part of p0ssess Pro."
	copyNotProHint = "Run /p0ssess:pro in Claude Code for details."
	copyNoRuns     = "Nothing has been measured yet."
	copyNoRunsHint = "Calibration runs each task kind on samples with known answers, with Claude alone and with p0ssess, and records what a correct answer cost and how long it took. Run /p0ssess:calibrate to start. It shows what the runs will cost before anything runs."
	copyNoBaseline = "Claude alone has not been measured yet, so there is nothing to compare against."
	copyModelOnly  = "Model call only: Claude's handoff to p0ssess is not in these numbers."
)

func (s *Server) report() (calibrate.Report, error) {
	var r calibrate.Report
	if s.Savings != nil {
		var err error
		if r, err = s.Savings(); err != nil {
			return r, err
		}
	}
	if r.Kinds == nil {
		r.Kinds = []calibrate.KindRow{}
	}
	if r.Models == nil {
		r.Models = []calibrate.ModelRow{}
	}
	if r.Prices == nil {
		r.Prices = []calibrate.Price{}
	}
	if r.Notes == nil {
		r.Notes = []string{}
	}
	return r, nil
}

func (s *Server) savings(w http.ResponseWriter, r *http.Request) {
	rep, err := s.report()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) savingsPDF(w http.ResponseWriter, r *http.Request) {
	rep, err := s.report()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := SavingsPDF(rep)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+SavingsPDFName(rep)+`"`)
	w.Write(b)
}

// SavingsPDFName is p0ssess-savings-YYYY-MM-DD.pdf, dated when the report was built.
func SavingsPDFName(r calibrate.Report) string {
	return "p0ssess-savings-" + generated(r).Format("2006-01-02") + ".pdf"
}

func generated(r calibrate.Report) time.Time {
	if r.GeneratedAt.IsZero() {
		return time.Now()
	}
	return r.GeneratedAt.Local()
}

// measured is false for the zero time and the JSON zero time.
func measured(t time.Time) bool { return t.Year() > 1 }

// usd prints a cost per correct answer with two significant digits below a dollar.
func usd(v float64) string {
	switch {
	case v >= 1000:
		return "$" + thousands(int64(math.Round(v)))
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v <= 0:
		return "$0"
	case v < 0.0001:
		return "<$0.0001"
	}
	return fmt.Sprintf("$%.*f", int(math.Ceil(-math.Log10(v)))+1, v)
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func secs(v float64) string {
	switch {
	case v < 10:
		return fmt.Sprintf("%.1f s", v)
	case v < 90:
		return fmt.Sprintf("%.0f s", v)
	}
	return fmt.Sprintf("%.1f min", v/60)
}

// share prints a fraction of the weekly limit as a percentage.
func share(v float64) string {
	p := v * 100
	switch {
	case p >= 10:
		return fmt.Sprintf("%.0f%%", p)
	case p >= 1:
		return fmt.Sprintf("%.1f%%", p)
	case p <= 0:
		return "0%"
	case p < 0.001:
		return "<0.001%"
	}
	return fmt.Sprintf("%.*f%%", int(math.Ceil(-math.Log10(p)))+1, p)
}

func times(r float64) string {
	if r >= 10 {
		return fmt.Sprintf("%.0f×", r)
	}
	return fmt.Sprintf("%.1f×", r)
}

// compare words how b (with p0ssess) stands against a (alone): less and more name the two directions.
func compare(a, b float64, less, more string) string {
	switch {
	case a <= 0 && b <= 0:
		return "the same"
	case b <= 0:
		return "nothing with p0ssess"
	case a/b >= 2:
		return times(a/b) + " " + less
	case a/b >= 1.05:
		return fmt.Sprintf("%.0f%% %s", (1-b/a)*100, less)
	case b/a >= 2:
		return times(b/a) + " " + more
	case b/a >= 1.05:
		return fmt.Sprintf("%.0f%% %s", (b/a-1)*100, more)
	}
	return "about the same"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func sideCost(s *calibrate.Side) string {
	switch {
	case s == nil:
		return "not measured"
	case s.Correct == 0 || s.CostPerCorrectUSD == nil:
		return "no correct answers in " + plural(s.Runs, "run", "runs")
	}
	return usd(*s.CostPerCorrectUSD)
}

func sideTime(s *calibrate.Side) string {
	if s == nil || s.Runs == 0 {
		return "not measured"
	}
	return secs(s.MeanSeconds)
}

func sideShare(s *calibrate.Side) string {
	switch {
	case s == nil:
		return "not measured"
	case s.WeeklySharePerCorrect == nil:
		return "not read"
	}
	return share(*s.WeeklySharePerCorrect)
}

func costSaving(k calibrate.KindRow) string {
	if k.Alone == nil || k.With == nil || k.Alone.CostPerCorrectUSD == nil || k.With.CostPerCorrectUSD == nil {
		return ""
	}
	return compare(*k.Alone.CostPerCorrectUSD, *k.With.CostPerCorrectUSD, "cheaper", "more expensive")
}

func modelOnly(s *calibrate.Side) bool { return s != nil && s.Source == "calibration" }

func words(id string) string {
	s := strings.ReplaceAll(id, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var (
	ink    = pdf.Black
	ink2   = pdf.Gray(0.38)
	rule   = pdf.Gray(0.78)
	band   = pdf.Gray(0.94)
	violet = pdf.Color{R: 0.455, G: 0.341, B: 0.878}
)

const (
	left   = 54.0
	right  = pdf.PageWidth - 54
	top    = 60.0
	bottom = pdf.PageHeight - 64
)

type layout struct {
	d *pdf.Doc
	y float64
	// header redraws a table's column heads when a table runs onto a new page.
	header func()
}

func (l *layout) need(h float64) {
	if l.y+h <= bottom {
		return
	}
	l.d.AddPage()
	l.y = top
	if l.header != nil {
		l.header()
	}
}

// para writes wrapped text and moves the cursor below it.
func (l *layout) para(f pdf.Font, size float64, c pdf.Color, s string, after float64) {
	lh := size * 1.35
	for _, line := range pdf.Wrap(f, size, s, right-left) {
		l.need(lh)
		l.y += lh
		l.d.Text(left, l.y-size*0.3, f, size, c, line)
	}
	l.y += after
}

func (l *layout) heading(s string) {
	l.need(40)
	l.y += 14
	l.d.Line(left, l.y, right, l.y, 0.5, rule)
	l.d.Line(left, l.y, left+24, l.y, 0.5, violet)
	l.y += 16
	l.d.Text(left, l.y, pdf.Bold, 11, ink, s)
	l.y += 8
}

type col struct {
	head  string
	width float64
	right bool
}

// group labels a run of columns above their heads.
type group struct {
	label        string
	first, count int
}

// table draws rows of cells, wrapping each cell inside its column and repeating heads on new pages.
func (l *layout) table(cols []col, groups []group, rows [][]cell) {
	const size, lh, pad = 8.5, 11.0, 5.0
	x := make([]float64, len(cols)+1)
	x[0] = left
	for i, c := range cols {
		x[i+1] = x[i] + c.width
	}
	head := func() {
		h := lh + 2*pad
		if groups != nil {
			h += lh
		}
		l.d.Rect(left, l.y, right-left, h, band)
		y := l.y + pad + lh - 2.5
		if groups != nil {
			for _, g := range groups {
				l.d.Text(x[g.first]+4, y, pdf.Bold, size, ink, g.label)
				l.d.Line(x[g.first]+4, y+3, x[g.first+g.count]-4, y+3, 0.5, rule)
			}
			y += lh
		}
		for i, c := range cols {
			if c.right {
				l.d.TextRight(x[i+1]-4, y, pdf.Bold, size, ink, c.head)
			} else {
				l.d.Text(x[i]+4, y, pdf.Bold, size, ink, c.head)
			}
		}
		l.y += h
	}
	l.need(4 * lh)
	head()
	l.header = head
	defer func() { l.header = nil }()
	for _, row := range rows {
		lines := make([][]line, len(cols))
		n := 0
		for i, c := range row {
			for _, part := range c {
				for _, s := range pdf.Wrap(part.font, part.size(size), part.text, cols[i].width-8) {
					lines[i] = append(lines[i], line{s, part})
				}
			}
			n = max(n, len(lines[i]))
		}
		h := float64(n)*lh + 2*pad
		l.need(h)
		for i, ls := range lines {
			for j, ln := range ls {
				y := l.y + pad + float64(j+1)*lh - 2.5
				f, sz, c := ln.part.font, ln.part.size(size), ln.part.color()
				if cols[i].right {
					l.d.TextRight(x[i+1]-4, y, f, sz, c, ln.text)
				} else {
					l.d.Text(x[i]+4, y, f, sz, c, ln.text)
				}
			}
		}
		l.y += h
		l.d.Line(left, l.y, right, l.y, 0.4, rule)
	}
}

// cell is one table cell: one or more runs of text, each starting on its own line.
type cell []part

type part struct {
	text  string
	font  pdf.Font
	quiet bool // smaller and gray
}

type line struct {
	text string
	part part
}

func (p part) size(base float64) float64 {
	if p.quiet {
		return base - 1
	}
	return base
}

func (p part) color() pdf.Color {
	if p.quiet {
		return ink2
	}
	return ink
}

func txt(s string) cell { return cell{{text: s}} }

// SavingsPDF renders the savings report as a US Letter PDF for printing or forwarding.
func SavingsPDF(r calibrate.Report) ([]byte, error) {
	d := pdf.New()
	d.Title = "p0ssess savings report"
	gen := generated(r)
	d.Created = gen
	d.AddPage()
	l := &layout{d: d, y: top}

	d.Text(left, l.y+16, pdf.Bold, 20, ink, "p0ssess savings report")
	l.y += 26
	plan := "free"
	if r.Pro {
		plan = "p0ssess Pro"
	}
	const day = "Jan 2, 2006"
	meta := []string{"Generated " + gen.Format(day), "Plan: " + plan}
	if r.Pro {
		if measured(r.Calibrated) {
			meta = append(meta, "Calibrated "+r.Calibrated.Local().Format(day))
		}
		if measured(r.Baselined) {
			meta = append(meta, "Claude baseline "+r.Baselined.Local().Format(day))
		}
	}
	l.para(pdf.Regular, 9.5, ink2, strings.Join(meta, "  ·  "), 4)
	l.y += 6
	d.Line(left, l.y, right, l.y, 0.8, ink)
	d.Line(left, l.y, left+24, l.y, 0.8, violet)
	l.y += 10

	switch {
	case !r.Pro:
		l.para(pdf.Bold, 11, ink, copyNotPro, 2)
		l.para(pdf.Regular, 10, ink2, copyNotProHint, 0)
	case len(r.Kinds) == 0 && len(r.Models) == 0:
		l.para(pdf.Bold, 11, ink, copyNoRuns, 2)
		l.para(pdf.Regular, 10, ink2, copyNoRunsHint, 0)
	default:
		savingsBody(l, r)
	}

	n := d.Pages()
	for i := 0; i < n; i++ {
		d.SetPage(i)
		y := pdf.PageHeight - 36
		d.Line(left, y-12, right, y-12, 0.4, rule)
		d.Text(left, y, pdf.Regular, 8, ink2, "p0ssess savings report, "+gen.Format("January 2, 2006"))
		d.TextRight(right, y, pdf.Regular, 8, ink2, fmt.Sprintf("Page %d of %d", i+1, n))
	}
	return d.Bytes(), nil
}

func savingsBody(l *layout, r calibrate.Report) {
	baseline := false
	for _, k := range r.Kinds {
		baseline = baseline || k.Alone != nil
	}
	if len(r.Kinds) > 0 {
		l.para(pdf.Regular, 10, ink, "Each task kind was run on samples with known answers, by Claude alone and by Claude with p0ssess. Every figure is per correct answer. Cost is Claude at API prices plus OpenRouter.", 4)
		if !baseline {
			l.para(pdf.Regular, 10, ink, copyNoBaseline, 4)
		}
		l.heading("By task kind")
		footnote := false
		rows := make([][]cell, 0, len(r.Kinds))
		for _, k := range r.Kinds {
			kind := cell{{text: words(k.UseCase), font: pdf.Bold}}
			if k.Task != "" {
				kind = append(kind, part{text: k.Task, quiet: true})
			}
			if k.Model != "" {
				kind = append(kind, part{text: "Routed to " + k.Model, quiet: true})
			}
			withCost, withTime := sideCost(k.With), sideTime(k.With)
			if modelOnly(k.With) {
				footnote = true
				withCost += " *"
				withTime += " *"
			}
			rows = append(rows, []cell{kind, txt(sideCost(k.Alone)), txt(withCost), txt(costSaving(k)),
				txt(sideTime(k.Alone)), txt(withTime), txt(sideShare(k.Alone)), txt(sideShare(k.With))})
		}
		l.table([]col{{"Kind", 132, false}, {"Alone", 62, true}, {"With", 62, true}, {"Saving", 62, true},
			{"Alone", 46, true}, {"With", 46, true}, {"Alone", 47, true}, {"With", 47, true}},
			[]group{{"Cost", 1, 3}, {"Mean time", 4, 2}, {"Weekly limit", 6, 2}}, rows)
		if footnote {
			l.y += 4
			l.para(pdf.Regular, 8, ink2, "* "+copyModelOnly, 0)
		}
	}

	if t := r.Totals; t != nil && t.Kinds > 0 {
		l.heading("Totals")
		s := fmt.Sprintf("Over the %s measured both ways, one correct answer of each cost %s with Claude alone and %s with p0ssess (%s).",
			plural(t.Kinds, "kind", "kinds"), usd(t.AloneUSD), usd(t.WithUSD), compare(t.AloneUSD, t.WithUSD, "cheaper", "more expensive"))
		s += fmt.Sprintf(" Time: %s alone, %s with p0ssess.", secs(t.AloneSeconds), secs(t.WithSeconds))
		if t.WeeklyShareSaved != nil {
			s += " Weekly limit saved: " + share(*t.WeeklyShareSaved) + "."
		}
		l.para(pdf.Regular, 10, ink, s, 0)
	}

	if len(r.Models) > 0 {
		l.heading("Models measured")
		if len(r.Kinds) == 0 {
			l.para(pdf.Regular, 10, ink, copyNoBaseline, 6)
		}
		rows := make([][]cell, 0, len(r.Models))
		for _, m := range r.Models {
			model := cell{{text: m.Model}}
			if m.Picked {
				model = append(model, part{text: "routed here", quiet: true})
			}
			c := "none correct"
			if m.Correct > 0 && m.CostPerCorrectUSD != nil {
				c = usd(*m.CostPerCorrectUSD)
			}
			rows = append(rows, []cell{txt(words(m.UseCase)), model, txt(fmt.Sprintf("%d of %d", m.Correct, m.Runs)), txt(c), txt(secs(m.MeanSeconds))})
		}
		l.table([]col{{"Kind", 120, false}, {"Model", 194, false}, {"Correct", 60, true}, {"Cost per correct", 76, true}, {"Mean time", 54, true}}, nil, rows)
	}

	if len(r.Prices) > 0 {
		l.heading("Claude API prices used")
		l.para(pdf.Regular, 9, ink2, "Dollars per million tokens.", 2)
		rows := make([][]cell, 0, len(r.Prices))
		for _, p := range r.Prices {
			rows = append(rows, []cell{txt(p.Model), txt(price(p.In)), txt(price(p.Out)), txt(price(p.CacheRead))})
		}
		l.table([]col{{"Model", 216, false}, {"Input", 96, true}, {"Output", 96, true}, {"Cache read", 96, true}}, nil, rows)
	}

	if len(r.Notes) > 0 {
		l.heading("Notes")
		for _, n := range r.Notes {
			l.para(pdf.Regular, 8.5, ink2, "•  "+n, 2)
		}
	}
}

func price(v float64) string {
	s := fmt.Sprintf("$%.2f", v)
	if v > 0 && v < 0.1 {
		s = fmt.Sprintf("$%.3f", v)
	}
	return s
}
