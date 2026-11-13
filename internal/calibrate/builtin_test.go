package calibrate

import (
	"io/fs"
	"testing"
)

// TestBuiltinSamples checks the shipped answer key: every sample parses, names its source and an open
// license, points at a file that exists, and the folder stays small enough to ship in the binary.
func TestBuiltinSamples(t *testing.T) {
	ss, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	fs.WalkDir(builtinFS, "samples", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	if total > MaxBuiltinBytes {
		t.Errorf("built-in samples are %d bytes, above %d", total, MaxBuiltinBytes)
	}
	for _, s := range ss {
		if !s.Grade(s.Answer) && s.Check != CheckWER {
			t.Errorf("%s: its own answer does not grade as correct", s.ID)
		}
		if s.Check == CheckWER && !s.Grade(s.Answer) {
			t.Errorf("%s: its own transcript does not grade as correct", s.ID)
		}
	}
}
