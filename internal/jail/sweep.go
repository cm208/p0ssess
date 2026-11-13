package jail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/proc"
)

// Owner names a process by pid and start time, so a reused pid is not taken for the owner.
type Owner struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start,omitempty"`
}

// Self is the current process.
func Self() Owner {
	pid := os.Getpid()
	return Owner{PID: pid, Start: procStart(pid)}
}

// procStart is the process's start time in clock ticks since boot (field 22 of /proc/<pid>/stat), or 0
// where /proc does not exist.
func procStart(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0
	}
	n, _ := strconv.ParseUint(f[19], 10, 64)
	return n
}

// Alive reports whether the owning process still runs.
func (o Owner) Alive() bool {
	if o.PID <= 0 {
		return false
	}
	if !proc.Alive(o.PID) {
		return false
	}
	return o.Start == 0 || procStart(o.PID) == o.Start
}

// Stale reports whether a sweep may remove the jail: its owner died (and it was not detached on purpose),
// or it was detached and its keep time has passed.
func (j *Jail) Stale(now time.Time) bool {
	if j.Detached {
		return now.After(j.KeepUntil)
	}
	return !j.Owner.Alive()
}

type podInfo struct {
	Name    string
	Created time.Time
	Labels  map[string]string
}

func listPods(ctx context.Context, label string) ([]podInfo, error) {
	out, err := podman(ctx, nil, nil, "pod", "ps", "--filter", "label="+label, "--format", "json")
	if err != nil {
		return nil, err
	}
	var pods []podInfo
	if s := strings.TrimSpace(out); s != "" && s != "null" {
		if err := json.Unmarshal([]byte(s), &pods); err != nil {
			return nil, fmt.Errorf("podman pod ps: %v", err)
		}
	}
	return pods, nil
}

// Sweep removes what a crashed or finished owner left behind: pods whose jail is stale, pods with no
// record and records with no pod, all within this state directory. It never touches a jail whose owner
// is alive. Jails keep no volumes, so removing the pod removes everything the model's session wrote.
func Sweep(ctx context.Context, log io.Writer) ([]string, error) {
	now := time.Now()
	pods, err := listPods(ctx, StateLabel())
	if err != nil {
		return nil, err
	}
	var removed []string
	live := map[string]bool{}
	for _, p := range pods {
		live[p.Name] = true
		j, err := Load(p.Name)
		switch {
		case err != nil && now.Sub(p.Created) > orphanAge:
			j = &Jail{Name: p.Name}
		case err != nil || !j.Stale(now):
			continue
		}
		if err := j.Remove(ctx); err != nil {
			fmt.Fprintf(log, "p0ssess: removing stale jail %s: %v\n", p.Name, err)
			continue
		}
		removed = append(removed, p.Name)
	}
	ents, _ := os.ReadDir(Dir())
	for _, e := range ents {
		if live[e.Name()] || !strings.HasPrefix(e.Name(), "p0ssess-") {
			continue
		}
		j, err := Load(e.Name())
		if err == nil && j.Owner.Alive() && now.Sub(j.Created) < orphanAge {
			continue // still being created
		}
		if err == nil && j.Cloud && j.Owner.Alive() {
			continue // a cloud session has no pod; its owner removes it
		}
		if err != nil {
			if st, serr := os.Stat(jailDir(e.Name())); serr == nil && now.Sub(st.ModTime()) < orphanAge {
				continue
			}
		}
		if os.RemoveAll(jailDir(e.Name())) == nil {
			removed = append(removed, e.Name()+" (record)")
		}
	}
	return removed, nil
}

// SweepLegacy removes the shared per-repository pods of p0ssess 0.3 and their state volumes, and the
// volumes no pod uses any more. Run it only when no p0ssess 0.3 process is still running.
func SweepLegacy(ctx context.Context) ([]string, error) {
	pods, err := listPods(ctx, "p0ssess=1")
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, p := range pods {
		if p.Labels["p0ssess.run"] == "1" {
			continue
		}
		if _, err := podman(ctx, nil, nil, "pod", "rm", "-f", "-t", "5", "--ignore", p.Name); err != nil {
			return removed, err
		}
		removed = append(removed, p.Name)
	}
	out, err := podman(ctx, nil, nil, "volume", "ls", "--filter", "label=p0ssess=1", "--format", "{{.Name}}")
	if err != nil {
		return removed, err
	}
	for _, v := range strings.Fields(out) {
		if strings.HasPrefix(v, "p0ssess-") && strings.HasSuffix(v, "-state") {
			if _, err := podman(ctx, nil, nil, "volume", "rm", v); err == nil {
				removed = append(removed, v)
			}
		}
	}
	return removed, nil
}
