// Package notices is the notices file: what p0ssess keeps out of the conversation (retries, limit
// warnings, why a model was picked) and each run's receipt, one JSON record a line, for the status line
// and the p0ssess page.
package notices

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Record struct {
	TS    time.Time `json:"ts"`
	Run   string    `json:"run,omitempty"`
	Notes []string  `json:"notes,omitempty"`
	// Receipt is the line the conversation got, e.g. "(kimi-k3.1-pro, $0.10, saved ~$0.80)".
	Receipt  string  `json:"receipt,omitempty"`
	Model    string  `json:"model,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	SavedUSD float64 `json:"saved_usd,omitempty"`
}

// Append adds one record. A record with neither notes nor a receipt is not written.
func Append(path string, r Record) error {
	var notes []string
	for _, n := range r.Notes {
		if n = strings.TrimSpace(n); n != "" {
			notes = append(notes, n)
		}
	}
	r.Notes = notes
	if len(r.Notes) == 0 && r.Receipt == "" {
		return nil
	}
	if r.TS.IsZero() {
		r.TS = time.Now().UTC()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// tailBytes bounds how much of a long file Recent reads.
const tailBytes = 256 << 10

// Recent returns up to n records, newest first, from the end of the file.
func Recent(path string, n int) []Record {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := int64(0)
	if st.Size() > tailBytes {
		off = st.Size() - tailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(f)
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	var all []Record
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && !r.TS.IsZero() {
			all = append(all, r)
		}
	}
	out := make([]Record, 0, min(n, len(all)))
	for i := len(all) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		out = append(out, all[i])
	}
	return out
}

// For reports whether a record belongs to a ledger run id: the same id, or a relay session id whose
// run ids are "<session>/<turn>".
func (r Record) For(runID string) bool {
	return r.Run != "" && (r.Run == runID || strings.HasPrefix(runID, r.Run+"/"))
}
