package rules

// Use-case kinds group use cases for rules, approvals and tendencies.
const (
	TaskMedia    = "media"    // reads audio, video, images or documents
	TaskGenerate = "generate" // makes images, audio, speech or video
	TaskAgent    = "agent"    // multi-step work in a sandboxed session
	TaskReview   = "review"   // reviews, critiques and judgments
	TaskCode     = "code"     // one-shot code work
	TaskText     = "text"     // everything else
)

// KindMatches reports whether a rule's kind covers a use case kind. "media" covers generation too;
// "all" or "" covers everything.
func KindMatches(rule, kind string) bool {
	switch rule {
	case "", "all", "*":
		return true
	case TaskMedia:
		return kind == TaskMedia || kind == TaskGenerate
	}
	return rule == kind
}
