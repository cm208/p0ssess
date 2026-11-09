package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/opencode"
)

// SessionRecord is what the relay remembers about an OpenCode session it created.
type SessionRecord struct {
	SessionID string    `json:"session_id"`
	Repo      string    `json:"repo"`
	Cwd       string    `json:"cwd"`
	Location  string    `json:"location"`
	Mode      string    `json:"mode"`
	Model     string    `json:"model"`
	Kind      string    `json:"kind,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	BaseRev   string    `json:"base_rev,omitempty"`
	Created   time.Time `json:"created"`

	// Current prompt: where its events start, when it was sent, and the session totals before it.
	PromptID     string          `json:"prompt_id,omitempty"`
	PromptSeq    int64           `json:"prompt_seq"`
	PromptAt     time.Time       `json:"prompt_at"`
	PromptHash   string          `json:"prompt_hash,omitempty"`
	CostBefore   float64         `json:"cost_before"`
	TokensBefore opencode.Tokens `json:"tokens_before"`
	Recorded     bool            `json:"recorded"`
	Turns        int             `json:"turns"`
}

var storeMu sync.Mutex

func recordFile(sid string) string { return config.Path("sessions", sid+".json") }

func RunDir(sid string) string { return config.Path("runs", sid) }

func loadRecord(sid string) (*SessionRecord, error) {
	b, err := os.ReadFile(recordFile(sid))
	if err != nil {
		return nil, err
	}
	var r SessionRecord
	return &r, json.Unmarshal(b, &r)
}

func (r *SessionRecord) save() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(recordFile(r.SessionID)), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := recordFile(r.SessionID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, recordFile(r.SessionID))
}

// HistoryEntry is one line of ~/.config/p0ssess/history.jsonl. It never holds prompt text, only a hash.
type HistoryEntry struct {
	Time      time.Time        `json:"time"`
	Kind      string           `json:"kind,omitempty"`
	Model     string           `json:"model"`
	Mode      string           `json:"mode"`
	Outcome   string           `json:"outcome"`
	CostUSD   float64          `json:"cost_usd"`
	Tokens    *opencode.Tokens `json:"tokens,omitempty"`
	DurationS float64          `json:"duration_s"`
	Session   string           `json:"session"`
	Turn      int              `json:"turn"`
	TaskHash  string           `json:"task_hash"`
	Rejected  int              `json:"asks_rejected,omitempty"`
	Escalated int              `json:"asks_escalated,omitempty"`
}

func appendHistory(h HistoryEntry) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	p := config.Path("history.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(h)
	_, err = f.Write(append(b, '\n'))
	return err
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

func subTokens(a, b opencode.Tokens) opencode.Tokens {
	a.Input -= b.Input
	a.Output -= b.Output
	a.Reasoning -= b.Reasoning
	a.Cache.Read -= b.Cache.Read
	a.Cache.Write -= b.Cache.Write
	return a
}
