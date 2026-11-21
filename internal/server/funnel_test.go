package server

import (
	"testing"

	"p0ssess/internal/funnel"
	"p0ssess/internal/notices"
)

func TestReceiptsNoteFirstRouteAndSaving(t *testing.T) {
	_, eng, _ := directEnv(t, "")
	record(eng, notices.Record{Run: "d-1", Receipt: "(m, $0.01)", Model: "m"})
	if funnel.Show(eng.Dirs, eng.Now()).ID != "" {
		t.Fatal("counted with usage counts off")
	}
	if _, err := eng.SetSetting("funnel.send", "true"); err != nil {
		t.Fatal(err)
	}
	record(eng, notices.Record{Run: "d-2", Receipt: "(m, $0.01)", Model: "m"})
	record(eng, notices.Record{Run: "d-3", Receipt: "(m, $0.01, saved ~$0.40)", Model: "m", SavedUSD: 0.4})
	record(eng, notices.Record{Run: "d-4", Notes: []string{"retry"}})
	c := funnel.Show(eng.Dirs, eng.Now()).ThisWeek.Counts
	if c[funnel.FirstRoute] != 1 || c[funnel.FirstSaving] != 1 {
		t.Fatalf("counts %v", c)
	}
}
