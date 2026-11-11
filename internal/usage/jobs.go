package usage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
)

// Job is one running delegation, as a file under the running directory. The
// relay creates it when work starts and removes it when work ends, so the
// status line can show what is running without asking anything.
type Job struct {
	ID      string    `json:"id"`
	Model   string    `json:"model"`
	UseCase string    `json:"use_case,omitempty"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

// MaxJobAge drops entries left behind by a crash.
const MaxJobAge = 6 * time.Hour

func StartJob(dir, model, useCase string, pid int) (Job, error) {
	var b [6]byte
	rand.Read(b[:])
	j := Job{ID: hex.EncodeToString(b[:]), Model: model, UseCase: useCase, PID: pid, Started: time.Now().UTC()}
	data, _ := json.Marshal(j)
	return j, paths.WriteFileAtomic(filepath.Join(dir, j.ID+".json"), data, 0o600)
}

func FinishJob(dir, id string) error {
	err := os.Remove(filepath.Join(dir, filepath.Base(id)+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Running lists live jobs, oldest first, and removes stale entries.
func Running(dir string, now time.Time) []Job {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Job
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(b, &j) != nil || now.Sub(j.Started) > MaxJobAge || (j.PID > 0 && !alive(j.PID)) {
			os.Remove(p)
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Started.Before(out[k].Started) })
	return out
}

func alive(pid int) bool { return proc.Alive(pid) }
