package calibrate

import (
	"context"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
)

// StakesFor is the stakes a calibration call of a use case runs at: the low end of its range, at most 3.
func StakesFor(t *table.Table, uc string) int {
	if u := t.UseCase(uc); u != nil && len(u.StakesRange) > 0 {
		return min(max(u.StakesRange[0], 1), 3)
	}
	return 2
}

// ReportFor builds the savings report from the files on this machine. The picked models come from the
// route table, so the report answers offline and never waits on the network.
func ReportFor(d paths.Dirs, t *table.Table, pro bool, now time.Time) (Report, error) {
	rs, err := LoadResults(d)
	if err != nil {
		return Report{}, err
	}
	bs, err := LoadBaselines(d)
	if err != nil {
		return Report{}, err
	}
	kinds := map[string]bool{}
	for _, r := range rs.List {
		kinds[r.UseCase] = true
	}
	for _, b := range bs.Runs {
		kinds[b.UseCase] = true
	}
	picked := map[string]string{}
	for uc := range kinds {
		if e := t.Route(uc, StakesFor(t, uc), 1); e != nil {
			for _, o := range e.Options {
				if o.Route == "direct" && !strings.HasPrefix(o.Model, "anthropic/") {
					picked[uc] = o.Model
					break
				}
			}
		}
	}
	return BuildReport(ReportInput{Pro: pro, Now: now, Results: rs, Baselines: bs, Picked: picked,
		Task: func(uc string) string {
			if u := t.UseCase(uc); u != nil {
				return u.Task
			}
			return ""
		}}), nil
}

// SendPending retries an upload that could not reach the cloud; the MCP server calls it at start.
func SendPending(ctx context.Context, d paths.Dirs, pro bool, put func(context.Context, []byte) error) {
	if !pro || !LoadMeta(d).Pending {
		return
	}
	rs, err := LoadResults(d)
	if err != nil {
		return
	}
	if Upload(ctx, put, rs) == nil {
		m := LoadMeta(d)
		m.Pending = false
		SaveMeta(d, m)
	}
}
