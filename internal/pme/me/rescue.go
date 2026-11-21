package me

import (
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/budget"
	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/rescue"
)

// rescuePeriod is one rescue period with what p0ssess handled in it, from the receipts.
type rescuePeriod struct {
	Window   string    `json:"window"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Active   bool      `json:"active"`
	Runs     int       `json:"runs"`
	CostUSD  float64   `json:"cost_usd"`
	SavedUSD float64   `json:"saved_usd"`
}

// rescueView is the page's rescue section: whether it is on, and the periods, newest first.
type rescueView struct {
	Enabled   bool           `json:"enabled"`
	Pro       bool           `json:"pro"`
	Threshold float64        `json:"threshold"`
	Periods   []rescuePeriod `json:"periods"`
}

func rescueOf(d paths.Dirs, c budget.Rescue, now time.Time) rescueView {
	v := rescueView{Enabled: c.Enabled, Pro: account.Current(d).Pro(), Threshold: c.Level(), Periods: []rescuePeriod{}}
	st := rescue.Load(d)
	recs := notices.Recent(d.NoticesFile(), 500)
	add := func(p rescue.Period, active bool) {
		end := p.End
		if active {
			end = p.Until
		}
		rp := rescuePeriod{Window: p.Window, Start: p.Start, End: end, Active: active}
		for _, r := range recs {
			if r.Receipt != "" && !r.TS.Before(p.Start) && (active || !r.TS.After(end)) {
				rp.Runs++
				rp.CostUSD += r.CostUSD
				rp.SavedUSD += r.SavedUSD
			}
		}
		v.Periods = append(v.Periods, rp)
	}
	if p, on := rescue.Active(d, now); on {
		add(p, true)
	}
	for i := len(st.Done) - 1; i >= 0; i-- {
		add(st.Done[i], false)
	}
	return v
}
