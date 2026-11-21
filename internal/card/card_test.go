package card

import (
	"strings"
	"testing"
)

// Every line of a card is the same width, wide characters included.
func TestCardIsSquare(t *testing.T) {
	c := Card{Title: "3 / 4 · live demo", Right: "1.4 s", Rows: []string{"model   deepseek", "", "  「ちゃんと動く」", strings.Repeat("x", 200)}}
	lines := strings.Split(strings.TrimSuffix(c.String(), "\n"), "\n")
	for i, l := range lines {
		if Cols(l) != Width {
			t.Errorf("line %d is %d columns: %q", i, Cols(l), l)
		}
	}
	if !strings.Contains(lines[0], "3 / 4 · live demo") || !strings.Contains(lines[0], "1.4 s") {
		t.Error(lines[0])
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("one two three four five six", 10, "  ")
	for _, l := range got {
		if Cols(l) > 10 {
			t.Errorf("%q", l)
		}
	}
	if len(got) < 2 || !strings.HasPrefix(got[0], "  one") {
		t.Error(got)
	}
}
