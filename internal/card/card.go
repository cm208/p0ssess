// Package card draws the small boxed cards p0ssess prints into the terminal: the tour's steps and the
// /p0ssess-me summary. Widths count East Asian wide characters as two columns so boxes stay square.
package card

import (
	"strings"
	"unicode"
)

// Width is the default outer width of a card.
const Width = 64

// Card is one box: a title and a right-hand label set into the top border, then rows.
type Card struct {
	Title string
	Right string
	Rows  []string
	W     int
}

// String renders the card.
func (c Card) String() string {
	w := c.W
	if w == 0 {
		w = Width
	}
	in := w - 4
	var b strings.Builder
	top := "╭"
	if c.Title != "" {
		top += "─ " + c.Title + " "
	}
	right := ""
	if c.Right != "" {
		right = " " + c.Right + " ─"
	}
	top += strings.Repeat("─", max(1, w-Cols(top)-Cols(right)-1)) + right + "╮"
	b.WriteString(top + "\n")
	for _, r := range c.Rows {
		r = Clip(r, in)
		b.WriteString("│ " + r + strings.Repeat(" ", in-Cols(r)) + " │\n")
	}
	b.WriteString("╰" + strings.Repeat("─", w-2) + "╯\n")
	return b.String()
}

// Cols is how many terminal columns s takes.
func Cols(s string) int {
	n := 0
	for _, r := range s {
		n += runeCols(r)
	}
	return n
}

func runeCols(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1f64f) || (r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

// Clip cuts s to n columns, ending in "…" when it was longer.
func Clip(s string, n int) string {
	if Cols(s) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		c := runeCols(r)
		if w+c > n-1 {
			break
		}
		b.WriteRune(r)
		w += c
	}
	return b.String() + "…"
}

// Wrap breaks text into lines of at most n columns, each prefixed with indent.
func Wrap(text string, n int, indent string) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = indent + word
		case Cols(line)+1+Cols(word) <= n:
			line += " " + word
		default:
			lines = append(lines, line)
			line = indent + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Bar is a meter of n cells for frac in [0,1].
func Bar(frac float64, n int) string {
	full := int(frac*float64(n) + 0.5)
	if frac > 0 && full == 0 {
		full = 1
	}
	full = min(max(full, 0), n)
	return strings.Repeat("▰", full) + strings.Repeat("▱", n-full)
}

// Pair lays out a label and a value with the value starting at column col.
func Pair(label, value string, col int) string {
	return label + strings.Repeat(" ", max(1, col-Cols(label))) + value
}

// Spread puts left and right on one line n columns wide.
func Spread(left, right string, n int) string {
	return left + strings.Repeat(" ", max(2, n-Cols(left)-Cols(right))) + right
}
