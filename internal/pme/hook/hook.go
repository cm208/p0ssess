// Package hook is the UserPromptSubmit hook. It prints one line of context
// only when routing matters (attached audio or video, a Claude limit that is
// close, cheap bulk work, or a route the user's history favors) and nothing
// otherwise. It reads only local files and never touches the network.
package hook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

type Input struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Prompt    string `json:"prompt"`
	Event     string `json:"hook_event_name"`
}

var (
	videoRe  = regexp.MustCompile(`(?i)(\.(mp4|mov|mkv|webm|avi|m4v)\b|youtube\.com/watch|youtu\.be/|vimeo\.com/\d)`)
	audioRe  = regexp.MustCompile(`(?i)\.(mp3|wav|m4a|flac|ogg|opus|aac)\b`)
	genRe    = regexp.MustCompile(`(?i)\b(generate|create|make|render|draw)\b.{0,40}\b(image|picture|illustration|logo|video|clip|voiceover|narration|music|song)\b`)
	bulkRe   = regexp.MustCompile(`(?i)\b(transcribe|summari[sz]e|translate|classify|tag|label|caption|extract)\b.{0,60}\b(all|every|each|thousands?|hundreds?|\d{3,}|batch|bulk|whole)\b|\b(all|every|each|\d{3,})\b.{0,60}\b(transcri|summar|translat|classif|caption|extract)`)
	reviewRe = regexp.MustCompile(`(?i)\b(second opinion|review (this|the|my)|cross-check|red.?team)\b`)
)

// BulkChars is the pasted-text size at which a prompt counts as bulk.
const BulkChars = 40_000

// Hint returns the one-line hint, or "".
func Hint(in Input, d paths.Dirs, now time.Time) string {
	if paths.Private(in.Cwd) {
		return ""
	}
	p := in.Prompt
	switch {
	case videoRe.MatchString(p):
		return "p0ssess: video in this prompt. Claude cannot watch video; ask the engine (recommend, use_case video-understand) before working around it." + favored(d, "media")
	case audioRe.MatchString(p):
		return "p0ssess: audio in this prompt. Claude cannot hear audio; ask the engine (recommend, use_case audio-transcribe or audio-events)." + favored(d, "media")
	case genRe.MatchString(p):
		return "p0ssess: Claude cannot generate images, audio or video; ask the engine (recommend, kind generate)." + favored(d, "generate")
	}
	if line := limitLine(d, now); line != "" {
		return line
	}
	if len(p) >= BulkChars || bulkRe.MatchString(p) {
		return "p0ssess: this looks like bulk one-shot work; a cheap model may do it for cents. Ask the engine (recommend) before doing it on Claude."
	}
	if reviewRe.MatchString(p) {
		if f := favored(d, "review"); f != "" {
			return "p0ssess:" + f
		}
	}
	return ""
}

func limitLine(d paths.Dirs, now time.Time) string {
	st, err := usage.Load(d.UsageFile())
	if err != nil {
		return ""
	}
	pr := st.Project(now, 0.15, 2*time.Hour)
	if !pr.Known || (pr.Projected < 0.9 && pr.Used < 0.8) {
		return ""
	}
	return fmt.Sprintf("p0ssess: Claude's %s limit is %d%% used (projected %d%% by reset); route bulk and media work through the engine (recommend).",
		pr.Window, int(pr.Used*100+0.5), int(pr.Projected*100+0.5))
}

// favored names a route the history strongly favors for a kind.
func favored(d paths.Dirs, kind string) string {
	es, err := history.Read(d.HistoryFile())
	if err != nil || len(es) == 0 {
		return ""
	}
	for _, t := range history.NewLedger(es).Tendencies() {
		if t.Favored && (t.Kind == kind || kind == "media" && t.Kind == "generate") {
			return " You usually accept " + history.Short(t.Model) + fmt.Sprintf(" here (%d/%d).", t.Counts.Accepted, t.Counts.Offers())
		}
	}
	return ""
}

// Run handles one hook call: it records the session for the MCP server and
// returns the text to print.
func Run(raw []byte, d paths.Dirs, now time.Time) string {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	if in.SessionID != "" {
		usage.Update(d.UsageFile(), func(st *usage.State) {
			st.TouchSession(usage.Session{ID: in.SessionID, Cwd: in.Cwd, Project: paths.ProjectRoot(in.Cwd), Seen: now.UTC()})
		})
	}
	return strings.TrimSpace(Hint(in, d, now))
}
