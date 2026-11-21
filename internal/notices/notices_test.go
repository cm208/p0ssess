package notices

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendRecent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "notices.jsonl")
	if err := Append(p, Record{Run: "x", Notes: []string{" ", ""}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		Append(p, Record{TS: time.Unix(int64(1000+i), 0), Run: "d-" + string(rune('a'+i)), Notes: []string{"note"}})
	}
	Append(p, Record{Run: "ses_q", Receipt: "(glm-5.3, $0.62)"})
	rs := Recent(p, 3)
	if len(rs) != 3 || rs[0].Receipt == "" || rs[1].Run != "d-e" {
		t.Fatalf("%+v", rs)
	}
	if !rs[0].For("ses_q/2") || !rs[0].For("ses_q") || rs[0].For("ses_qq/1") || (Record{}).For("") {
		t.Error("For")
	}
	if all := Recent(p, 0); len(all) != 6 {
		t.Errorf("empty records are not written: %d", len(all))
	}
	big := strings.Repeat("y", 1000)
	for i := 0; i < 400; i++ {
		Append(p, Record{Run: "b", Notes: []string{big}})
	}
	if rs := Recent(p, 1); len(rs) != 1 || rs[0].Run != "b" {
		t.Errorf("tail read: %+v", rs)
	}
}
