// Package hook is the UserPromptSubmit hook. It prints one line of context
// only when routing matters (attached audio or video, a Claude limit that is
// close, cheap bulk work, or a route the user's history favors) and nothing
// otherwise. It reads only local files and never touches the network.
package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/calibrate"
	"p0ssess/internal/config"
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
	videoRe     = regexp.MustCompile(`(?i)(\.(mp4|mov|mkv|webm|avi|m4v)\b|youtube\.com/watch|youtu\.be/|vimeo\.com/\d)`)
	videoFileRe = regexp.MustCompile(`(?i)\.(mp4|mov|mkv|webm|avi|m4v)\b`)
	videoLinkRe = regexp.MustCompile(`(?i)(youtube\.com/watch|youtu\.be/|vimeo\.com/\d)`)
	videoAskRe  = regexp.MustCompile(`(?i)\b(watch|view|see what happens in)\b[^.?!\n]{0,40}\b(video|clip|screencast|screen ?recording|footage|recording)\b`)
	audioRe     = regexp.MustCompile(`(?i)\.(mp3|wav|m4a|flac|ogg|opus|aac)\b`)
	audioAskRe  = regexp.MustCompile(`(?i)\b(listen to|hear)\b|\btranscribe\b[^.?!\n]{0,40}\b(audio|recording|call|podcast|interview|memo|meeting)\b|\b(podcast|voice ?memo|voicemail)\b`)
	genRe       = regexp.MustCompile(`(?i)\b(generate|create|make|render|draw|design|paint|sketch)\b[^.?!\n]{0,40}\b(image|picture|pic|illustration|logo|photo|poster|wallpaper|artwork|drawing|sticker|banner|thumbnail|mockup)s?\b|\b(an?|the) (image|picture|photo|illustration|drawing) of\b`)
	bulkRe      = regexp.MustCompile(`(?i)\b(transcribe|summari[sz]e|translate|classify|tag|label|caption|extract)\b.{0,60}\b(all|every|each|thousands?|hundreds?|\d{3,}|batch|bulk|whole)\b|\b(all|every|each|\d{3,})\b.{0,60}\b(transcri|summar|translat|classif|caption|extract)`)
	reviewRe    = regexp.MustCompile(`(?i)\b(second opinion|review (this|the|my)|cross-check|red.?team)\b`)
)

// BulkChars is the pasted-text size at which a prompt counts as bulk.
const BulkChars = 40_000

// act is how a hint tells Claude to act: one hand-off to the p0ssess agent, no talk around it.
const act = ` p0ssess is set up: hand it to the p0ssess agent now (Agent tool, subagent_type "p0ssess:p0ssess"), one sentence with the request%s. ` +
	`Write nothing before or while it runs (end your turn after the Agent call with no text); when it returns, give its answer as it came plus "(model, cost)", nothing else.`

// Hint returns the one-line hint, or "". ready says whether p0ssess has an OpenRouter key to spend with.
func Hint(in Input, d paths.Dirs, now time.Time) string {
	return hint(in, d, now, keyReady())
}

func hint(in Input, d paths.Dirs, now time.Time, ready bool) string {
	if paths.Private(in.Cwd) {
		return ""
	}
	p := in.Prompt
	media := func(what, how, kind string) string {
		if !ready {
			return "p0ssess: " + what + " p0ssess could do it but has no OpenRouter key yet; tell the user to run /p0ssess:p0ssess-me."
		}
		return "p0ssess: " + what + fmt.Sprintf(act, how) + favored(d, kind)
	}
	switch {
	case videoRe.MatchString(p) || videoAskRe.MatchString(p):
		how := ` and the video's absolute path (use case video-understand)`
		if videoLinkRe.MatchString(p) && !videoFileRe.MatchString(p) {
			how += " (it takes local files only: download the link first, e.g. with yt-dlp if it is installed)"
		}
		return media("the user wants a video watched; Claude cannot watch video.", how, "media")
	case audioRe.MatchString(p) || audioAskRe.MatchString(p):
		return media("the user wants audio heard; Claude cannot hear audio.",
			` and the audio's absolute path (use case audio-transcribe; audio-transcribe-noisy for noisy or hard audio such as radio, phone or field recordings; audio-tone for delivery, audio-events for sounds)`, "media")
	case genRe.MatchString(p):
		return media("if the user wants an image made, Claude cannot draw one.",
			` (use case image-generate; image-edit with a reference image's path). Present the file it returns and print its absolute path`, "generate")
	}
	if line := limitLine(d, now); line != "" {
		return line
	}
	if len(p) >= BulkChars || bulkRe.MatchString(p) {
		return "p0ssess: this looks like bulk one-shot work a cheap model may do for cents; if it is, hand it to the p0ssess agent (Agent tool, subagent_type \"p0ssess:p0ssess\") rather than doing it here."
	}
	if reviewRe.MatchString(p) {
		if f := favored(d, "review"); f != "" {
			return "p0ssess:" + f
		}
	}
	return ""
}

// keyReady reports whether the OpenRouter key file exists; it is never read here.
func keyReady() bool {
	c, err := config.Load()
	if err != nil {
		return false
	}
	st, err := os.Stat(c.OpenRouterKeyFile)
	return err == nil && st.Size() > 0
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
	return fmt.Sprintf("p0ssess: Claude's %s limit is %d%% used (projected %d%% by reset); bulk and long agent work may be worth handing to the p0ssess agent.",
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
		proj := os.Getenv("CLAUDE_PROJECT_DIR")
		if proj == "" {
			proj = paths.ProjectRoot(in.Cwd)
		}
		usage.Update(d.UsageFile(), func(st *usage.State) {
			st.TouchSession(usage.Session{ID: in.SessionID, Cwd: in.Cwd, Project: proj, Seen: now.UTC()})
		})
		markMainCalls(d, in.SessionID, in.Prompt)
	}
	if os.Getenv(calibrate.BaselineEnv) == calibrate.ModeAlone {
		// A Claude-alone baseline run: p0ssess stays out of it.
		return ""
	}
	out := strings.TrimSpace(Hint(in, d, now))
	if n := account.Notice(d); n != "" {
		out = strings.TrimSpace(out + " p0ssess: tell the user this once, in one line, as written: " + n)
	} else if n := calibrationOffer(d); n != "" {
		out = strings.TrimSpace(out + " p0ssess: tell the user this once, in one line, as written: " + n)
	}
	return out
}

// calibrationOffer is said once after Pro turns on, when nothing has been calibrated yet (D37).
func calibrationOffer(d paths.Dirs) string {
	if !account.Current(d).Pro() {
		return ""
	}
	m := calibrate.LoadMeta(d)
	if m.Offered {
		return ""
	}
	m.Offered = true
	if calibrate.SaveMeta(d, m) != nil {
		return ""
	}
	if rs, err := calibrate.LoadResults(d); err != nil || len(rs.List) > 0 {
		return ""
	}
	return "p0ssess Pro can tune its routes to how models do on your own key, with a short calibration run; /p0ssess:calibrate shows the cost before anything runs."
}
