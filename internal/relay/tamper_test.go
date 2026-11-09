package relay

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"p0ssess/internal/opencode"
)

func newTamperWatch() *watch {
	e := &Engine{log: io.Discard, watches: map[string]*watch{}}
	c := opencode.New("http://127.0.0.1:1", "pw") // interrupt attempts fail fast; tamper state is what we check
	ctx, cancel := context.WithCancel(context.Background())
	return &watch{e: e, c: c, rec: &SessionRecord{SessionID: "ses_1"}, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), poke: make(chan struct{}, 1),
		toolNames: map[string]string{}, seen: map[string]bool{}, issued: map[string]bool{}}
}

func repliedEvent(reqID string) opencode.Event {
	return opencode.Event{Type: opencode.EvPermissionReplied,
		Data: json.RawMessage(`{"sessionID":"ses_1","requestID":"` + reqID + `"}`)}
}

func TestTamperOnForeignReply(t *testing.T) {
	w := newTamperWatch()
	w.handle(repliedEvent("req_evil"))
	if !w.tampered || w.tamperReason == "" {
		t.Fatalf("a reply the relay did not issue should trip the tamper check: %+v", w.tampered)
	}
}

func TestNoTamperOnRelayReply(t *testing.T) {
	w := newTamperWatch()
	w.markIssued("req_ours")
	w.handle(repliedEvent("req_ours"))
	if w.tampered {
		t.Fatal("a reply the relay issued must not trip the tamper check")
	}
}

func TestTamperOnForeignInterrupt(t *testing.T) {
	w := newTamperWatch()
	w.handle(opencode.Event{Type: opencode.EvExecutionInterrupted,
		Data: json.RawMessage(`{"sessionID":"ses_1","reason":"unknown"}`)})
	if !w.tampered {
		t.Fatal("an interrupt the relay did not request should trip the tamper check")
	}
}

func TestNoTamperOnRelayInterrupt(t *testing.T) {
	w := newTamperWatch()
	w.markRelayInterrupt()
	w.handle(opencode.Event{Type: opencode.EvExecutionInterrupted,
		Data: json.RawMessage(`{"sessionID":"ses_1","reason":"caller"}`)})
	if w.tampered {
		t.Fatal("a relay-requested interrupt must not be flagged as tampering")
	}
}
