// Package pdf writes small PDF documents: US Letter pages, the standard Helvetica and Helvetica-Bold
// fonts in WinAnsi encoding, text, lines and filled rectangles. Coordinates are points from the top
// left corner of the page, y growing downward.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// US Letter, in points.
const (
	PageWidth  = 612.0
	PageHeight = 792.0
)

type Font int

const (
	Regular Font = iota
	Bold
)

// Color is an RGB color, each channel 0 to 1.
type Color struct{ R, G, B float64 }

// Gray is a neutral color: 0 is black, 1 is white.
func Gray(v float64) Color { return Color{v, v, v} }

var Black = Gray(0)

type Doc struct {
	Title   string
	Created time.Time
	pages   []*bytes.Buffer
	cur     int
}

func New() *Doc { return &Doc{cur: -1} }

// AddPage starts a page and makes it the one drawn on.
func (d *Doc) AddPage() {
	d.pages = append(d.pages, &bytes.Buffer{})
	d.cur = len(d.pages) - 1
}

func (d *Doc) Pages() int { return len(d.pages) }

// SetPage makes page i (from 0) the one drawn on.
func (d *Doc) SetPage(i int) { d.cur = i }

func (d *Doc) page() *bytes.Buffer {
	if d.cur < 0 {
		d.AddPage()
	}
	return d.pages[d.cur]
}

// Text draws s with its baseline at y.
func (d *Doc) Text(x, y float64, f Font, size float64, c Color, s string) {
	fmt.Fprintf(d.page(), "BT %s rg /F%d %s Tf %s %s Td (%s) Tj ET\n", rgb(c), int(f)+1, num(size), num(x), num(PageHeight-y), escape(Encode(s)))
}

// TextRight draws s ending at x.
func (d *Doc) TextRight(x, y float64, f Font, size float64, c Color, s string) {
	d.Text(x-Width(f, size, s), y, f, size, c, s)
}

func (d *Doc) Line(x1, y1, x2, y2, width float64, c Color) {
	fmt.Fprintf(d.page(), "%s RG %s w %s %s m %s %s l S\n", rgb(c), num(width), num(x1), num(PageHeight-y1), num(x2), num(PageHeight-y2))
}

// Rect fills the rectangle whose top left corner is (x, y).
func (d *Doc) Rect(x, y, w, h float64, c Color) {
	fmt.Fprintf(d.page(), "%s rg %s %s %s %s re f\n", rgb(c), num(x), num(PageHeight-y-h), num(w), num(h))
}

// Bytes returns the finished file.
func (d *Doc) Bytes() []byte {
	if len(d.pages) == 0 {
		d.AddPage()
	}
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(d.pages)
	kids := make([]string, n)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", 6+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	info := "<< /Producer (p0ssess)"
	if d.Title != "" {
		info += " /Title (" + escape(Encode(d.Title)) + ")"
	}
	if !d.Created.IsZero() {
		info += " /CreationDate (D:" + d.Created.UTC().Format("20060102150405") + "Z)"
	}
	obj(info + " >>")
	for i, p := range d.pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", num(PageWidth), num(PageHeight), 7+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", p.Len(), p.Bytes()))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

func num(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

func rgb(c Color) string { return num(c.R) + " " + num(c.G) + " " + num(c.B) }

// escape makes WinAnsi bytes safe inside a PDF literal string, keeping the file ASCII.
func escape(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		switch {
		case c == '(' || c == ')' || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c < 32 || c > 126:
			fmt.Fprintf(&s, "\\%03o", c)
		default:
			s.WriteByte(c)
		}
	}
	return s.String()
}

// winAnsi maps the characters of 0x80-0x9f that are not Latin-1.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89,
	'Š': 0x8a, '‹': 0x8b, 'Œ': 0x8c, 'Ž': 0x8e, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95,
	'–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9a, '›': 0x9b, 'œ': 0x9c, 'ž': 0x9e, 'Ÿ': 0x9f,
}

// spelled are stand-ins for common characters the standard fonts lack.
var spelled = map[rune]string{
	'→': "->", '←': "<-", '↔': "<->", '≈': "~", '≤': "<=", '≥': ">=", '≠': "!=", '\u2212': "-",
	'\u2010': "-", '\u2011': "-", '\u2009': " ", '\u202f': " ", '\u200b': "", 'λ': "lambda",
	'✓': "yes", '✗': "no",
}

// Encode turns s into WinAnsi bytes. Characters outside it become a close ASCII stand-in, or "?".
func Encode(s string) []byte {
	var out []byte
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			out = append(out, ' ')
		case r < 0x80 || (r >= 0xa0 && r <= 0xff):
			out = append(out, byte(r))
		case winAnsi[r] != 0:
			out = append(out, winAnsi[r])
		case spelled[r] != "" || r == '\u200b':
			out = append(out, spelled[r]...)
		default:
			out = append(out, '?')
		}
	}
	return out
}

// Width is the advance width of s in points.
func Width(f Font, size float64, s string) float64 {
	t := &widths[f]
	var w int
	for _, c := range Encode(s) {
		switch {
		case c >= 32 && c <= 126:
			w += int(t[c-32])
		case c == 0x85 || c == 0x97 || c == 0x89 || c == 0x99:
			w += 1000
		case c == 0x95:
			w += 350
		case c == 0x91 || c == 0x92 || c == 0x82:
			w += []int{222, 278}[f]
		case c == 0x93 || c == 0x94 || c == 0x84:
			w += []int{333, 500}[f]
		case c == 0xa0 || c == 0xb7:
			w += 278
		case c == 0xd7:
			w += 584
		default:
			w += 556
		}
	}
	return float64(w) * size / 1000
}

// Wrap breaks s into lines no wider than max, at spaces where it can.
func Wrap(f Font, size float64, s string, max float64) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		next := word
		if line != "" {
			next = line + " " + word
		}
		if line == "" || Width(f, size, next) <= max {
			line = next
			continue
		}
		lines = append(lines, line)
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i := 0; i < len(lines); i++ {
		if Width(f, size, lines[i]) <= max {
			continue
		}
		r := []rune(lines[i])
		cut := len(r) - 1
		for cut > 1 && Width(f, size, string(r[:cut])) > max {
			cut--
		}
		rest := append([]string{string(r[cut:])}, lines[i+1:]...)
		lines = append(lines[:i], string(r[:cut]))
		lines = append(lines, rest...)
	}
	return lines
}

// widths are the Adobe metrics for characters 32-126, in thousandths of the font size.
var widths = [2][95]int16{
	{
		278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
		1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
		333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
		556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
	},
	{
		278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
		975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
		333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
		611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
	},
}
