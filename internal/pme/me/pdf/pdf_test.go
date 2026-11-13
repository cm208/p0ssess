package pdf

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDocument(t *testing.T) {
	d := New()
	d.Title = "Test (one)"
	d.Created = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d.AddPage()
	d.Text(54, 72, Bold, 18, Black, `Cost (per answer) \ 3×`)
	d.Line(54, 80, 558, 80, 0.5, Gray(0.6))
	d.Rect(54, 90, 100, 10, Gray(0.9))
	d.AddPage()
	d.Text(54, 72, Regular, 10, Black, "second — page →")
	b := d.Bytes()
	if err := Verify(b); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("%%EOF\n")) {
		t.Error("missing the EOF marker")
	}
	s := string(b)
	for _, want := range []string{`(Cost \(per answer\) \\ 3\327) Tj`, `(second \227 page ->) Tj`, "/Count 2", "/BaseFont /Helvetica-Bold", "/Encoding /WinAnsiEncoding", "/CreationDate (D:20260102030405Z)", "/Title (Test \\(one\\))"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	for i := 0; i < len(b); i++ {
		if b[i] > 126 && i > 20 {
			t.Fatalf("non-ASCII byte at %d", i)
		}
	}
}

func TestEncode(t *testing.T) {
	for in, want := range map[string]string{"é": "\xe9", "—": "\x97", "日本": "??", "a→b": "a->b", "x\ty": "x y"} {
		if got := string(Encode(in)); got != want {
			t.Errorf("Encode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWidthAndWrap(t *testing.T) {
	if w := Width(Regular, 10, "0"); w != 5.56 {
		t.Errorf("width of 0 = %v", w)
	}
	if Width(Bold, 10, "i") <= Width(Regular, 10, "i") {
		t.Error("bold i should be wider")
	}
	lines := Wrap(Regular, 10, "the quick brown fox jumps over the lazy dog", 80)
	if len(lines) < 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines {
		if Width(Regular, 10, l) > 80 {
			t.Errorf("line too wide: %q", l)
		}
	}
	long := Wrap(Regular, 10, strings.Repeat("x", 60), 50)
	for _, l := range long {
		if Width(Regular, 10, l) > 50 {
			t.Errorf("unbroken word too wide: %q", l)
		}
	}
	if strings.Join(long, "") != strings.Repeat("x", 60) {
		t.Error("splitting a long word lost characters")
	}
}

func TestVerifyCatchesBadOffsets(t *testing.T) {
	d := New()
	d.Text(54, 72, Regular, 10, Black, "x")
	b := d.Bytes()
	bad := bytes.Replace(b, []byte("1 0 obj"), []byte("1  0 obj"), 1)
	if Verify(bad) == nil {
		t.Error("shifted object passed")
	}
}
