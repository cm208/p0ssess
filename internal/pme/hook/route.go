package hook

import (
	"fmt"
	"regexp"
	"slices"
)

// keepRe is the user keeping the work on Claude: no routing hint for this prompt.
var keepRe = regexp.MustCompile(`(?i)\b(keep (this|it|that|the work) (on|with|in) claude|(do|run) (it|this) (yourself|on claude|here)|(don'?t|do not|no) (use |route (it |this )?(to |through )?)?p0ssess|without p0ssess|not (through|via) p0ssess)\b`)

// currentRe is work that needs information newer than any model's training: news, prices, recent periods.
var currentRe = regexp.MustCompile(`(?i)\b(latest|newest|recent|current) (news|headlines|developments|trends?|prices?|rates?|research|releases?|versions?|events|figures|numbers|reports?|data|studies|polls?)\b` +
	`|\b(trending|today'?s (news|headlines|prices?|rates?|weather)|as of today|(this|last) (week|month|quarter|year)'s|exchange rates?)\b` +
	`|\b(news|headlines?)\b` +
	`|\b(past|last|previous|recent) (\d+|few|several|couple of|two|three|four|five|six|nine|twelve|eighteen) (days?|weeks?|months?|quarters?|years?)\b` +
	`|\b(live|current) (scores?|standings|weather|market)\b` +
	`|\bmarket (trends?|data|insights?|research|conditions)\b` +
	`|\b(stock|share|crypto|bitcoin) prices?\b`)

// NeedsCurrentInfo reports whether a task needs current information from the web (news, prices, the last
// few months), which no model knows from training alone.
func NeedsCurrentInfo(task string) bool {
	if len(task) > matchHead {
		task = task[:matchHead]
	}
	return currentRe.MatchString(task)
}

// autoRouted are the use cases that go to p0ssess by default inside the user's envelope (decision 7):
// research, news, summaries, bulk data work and documents. Coding stays on Claude unless a rule says
// otherwise.
var autoRouted = []string{"deep-web-research", "web-search-answer", "summarize-long-doc", "log-digest",
	"repo-offload-summary", "science-paper-explain", "bulk-classify-tag", "batch-offline-map",
	"data-extract-structured", "data-transform-format", "translate-text", "record-dedup", "huge-pdf",
	"ocr-degraded-image", "pdf-table-extract", "invoice-receipt-extract", "image-alt-text", "subtitles-srt",
	"i18n-catalog-sync"}

// autoWork is the first matched use case p0ssess takes by default, or nil.
func autoWork(p string) *work {
	for _, w := range matchWork(p) {
		if !w.keep && slices.Contains(autoRouted, w.id) {
			return w
		}
	}
	return nil
}

// onFailure is what Claude does when p0ssess cannot finish routed work.
const onFailure = ` If p0ssess returns an error, send the agent the request again with the fix the error names (SendMessage), or tell the user the gap in one line; do not do the work yourself unless the user says so.`

// routeHint is the route-by-default hint for work p0ssess takes without being asked.
func routeHint(w *work, current bool) string {
	what := w.label + " (use case " + w.id + ")"
	web := ""
	if current {
		web = `, and say it needs current web data`
	}
	return fmt.Sprintf(`p0ssess: this is %s, which p0ssess runs by default inside the user's limits. `+
		`Hand it to the p0ssess agent now (Agent tool, subagent_type "p0ssess:p0ssess"): one sentence with the request and absolute paths, plus "use case %s"%s. `+
		`Do not do it yourself, ask first, or price it. If the user said to keep it on Claude, or the request is about this conversation or code you are editing, ignore this line.`+
		` Write nothing while it runs; when it returns, give its answer and its receipt line.`, what, w.id, web) + onFailure
}

// routeDefault replaces a Claude agent the user asked for while Claude's limit runs low.
const routeDefault = ` Route by default: hand this to the p0ssess agent (Agent tool, subagent_type "p0ssess:p0ssess") instead of a Claude agent, with no question first; it runs on a cheaper model inside the user's limits and spares the Claude limit. Use a Claude agent only if p0ssess finds no model that fits, or the user insists after you say this in one line.` + onFailure
