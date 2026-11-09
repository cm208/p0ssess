package opencode

import (
	"os"
	"strings"
	"testing"
)

// testdata/sse/edit-run.sse is a recorded /api/event stream from OpenCode 2.2.5: one edit task with
// tool calls, a shell run and four steps. Paths were rewritten to /work/demo.

func readFixture(t *testing.T, name string) []Frame {
	t.Helper()
	f, err := os.Open("testdata/sse/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var frames []Frame
	if err := ReadFrames(f, func(fr Frame) bool { frames = append(frames, fr); return true }); err != nil {
		t.Fatal(err)
	}
	return frames
}

func TestRecordedEditRun(t *testing.T) {
	frames := readFixture(t, "edit-run.sse")
	var sid string
	counts := map[string]int{}
	var cost float64
	var final string
	terminal := 0
	for _, fr := range frames {
		ev, ok := ParseEvent(fr)
		if !ok {
			continue
		}
		counts[ev.Type]++
		switch ev.Type {
		case "session.created":
			sid = ev.SessionID()
		case EvStepEnded:
			var d StepEnded
			if err := ev.Decode(&d); err != nil {
				t.Fatal(err)
			}
			cost += d.Cost
			if d.SessionID != sid || d.AssistantMessageID == "" {
				t.Errorf("step.ended: %+v", d)
			}
		case EvTextEnded:
			var d TextEnded
			ev.Decode(&d)
			final = d.Text
		case EvToolInputStarted:
			var d ToolInputStarted
			ev.Decode(&d)
			if d.Name == "" || d.ID == "" {
				t.Errorf("tool.input.started: %+v", d)
			}
		}
		if ev.IsTerminal() {
			terminal++
			if ev.Type != EvExecutionSucceeded || ev.Durable == nil || ev.Durable.Seq <= 0 {
				t.Errorf("terminal event: %+v", ev)
			}
		}
	}
	if counts[EvServerConnected] != 1 || !strings.HasPrefix(sid, "ses") {
		t.Fatalf("connected=%d sid=%q", counts[EvServerConnected], sid)
	}
	if counts[EvStepEnded] != 4 || counts[EvToolInputStarted] != 5 || terminal != 1 {
		t.Errorf("counts: %v terminal=%d", counts, terminal)
	}
	if cost <= 0 || final == "" {
		t.Errorf("cost=%v final=%q", cost, final)
	}
}

func TestReadFramesComments(t *testing.T) {
	in := ": heartbeat\n\nevent: x\nid: 7\ndata: {\"type\":\"a\",\ndata: \"data\":{}}\n\ndata: tail"
	var got []Frame
	ReadFrames(strings.NewReader(in), func(f Frame) bool { got = append(got, f); return true })
	if len(got) != 3 || got[0].Event != "comment" || got[1].Event != "x" || got[1].ID != "7" || got[2].Data != "tail" {
		t.Fatalf("%+v", got)
	}
	if got[1].Data != "{\"type\":\"a\",\n\"data\":{}}" {
		t.Errorf("multi-line data: %q", got[1].Data)
	}
}

func TestLogSyncedMarker(t *testing.T) {
	ev, ok := ParseEvent(Frame{Data: `{"type":"log.synced","aggregateID":"ses_x","seq":26}`})
	if !ok || ev.Type != EvLogSynced || ev.Seq == nil || *ev.Seq != 26 {
		t.Fatalf("%+v", ev)
	}
}
