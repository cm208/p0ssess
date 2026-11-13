package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	startxrefRE = regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n?$`)
	streamRE    = regexp.MustCompile(`<< /Length (\d+) >>\nstream\n`)
)

// Verify checks the structure this package writes: header, xref offsets, trailer and stream lengths.
func Verify(b []byte) error {
	if !bytes.HasPrefix(b, []byte("%PDF-1.")) {
		return errors.New("missing %PDF- header")
	}
	m := startxrefRE.FindSubmatch(b)
	if m == nil {
		return errors.New("missing startxref or %%EOF")
	}
	at, _ := strconv.Atoi(string(m[1]))
	if at >= len(b) || !bytes.HasPrefix(b[at:], []byte("xref\n")) {
		return fmt.Errorf("startxref %d does not point at the xref table", at)
	}
	lines := strings.Split(string(b[at:]), "\n")
	var first, count int
	if _, err := fmt.Sscanf(lines[1], "%d %d", &first, &count); err != nil || first != 0 || count < 1 || len(lines) < count+2 {
		return fmt.Errorf("bad xref subsection %q", lines[1])
	}
	if lines[2] != "0000000000 65535 f " {
		return fmt.Errorf("bad free entry %q", lines[2])
	}
	for i := 1; i < count; i++ {
		e := lines[2+i]
		if len(e) != 19 || !strings.HasSuffix(e, " 00000 n ") {
			return fmt.Errorf("xref entry %d is %q", i, e)
		}
		off, _ := strconv.Atoi(e[:10])
		if off >= len(b) || !bytes.HasPrefix(b[off:], []byte(strconv.Itoa(i)+" 0 obj\n")) {
			return fmt.Errorf("object %d is not at offset %d", i, off)
		}
	}
	if !bytes.Contains(b, []byte("trailer\n<< /Size "+strconv.Itoa(count)+" /Root 1 0 R")) {
		return errors.New("trailer /Size does not match the xref")
	}
	for _, sm := range streamRE.FindAllSubmatchIndex(b, -1) {
		n, _ := strconv.Atoi(string(b[sm[2]:sm[3]]))
		if sm[1]+n > len(b) || !bytes.HasPrefix(b[sm[1]+n:], []byte("endstream")) {
			return fmt.Errorf("stream at %d has the wrong /Length %d", sm[1], n)
		}
	}
	return nil
}
