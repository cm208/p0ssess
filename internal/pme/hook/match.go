package hook

import (
	"regexp"
	"strconv"
	"sync"
)

// work is one catalog use case as the prompt hook recognizes it: the kind it belongs to (for rules
// limited to kinds), whether the engine keeps it on Claude by default, a short name for the hint, and the
// patterns a prompt for that work matches (all of them, when there are several). The ids and kinds must
// match the route table's catalog (TestWorkMatchesCatalog).
type work struct {
	id    string
	kind  string
	keep  bool // the catalog keeps this work on Claude; a kind-wide rule does not pull it out
	label string
	pats  []string
	all   []*regexp.Regexp
}

func re(s string) string { return `(?i)` + s }

// near matches a then b (or b then a) within n characters on one line.
func near(a, b string, n int) string {
	return `(\b(` + a + `)\b[^\n]{0,` + strconv.Itoa(n) + `}\b(` + b + `))|(\b(` + b + `)\b[^\n]{0,` + strconv.Itoa(n) + `}\b(` + a + `))`
}

const (
	audioFile = `[\w./~-]*\.(mp3|wav|m4a|flac|ogg|opus|aac)\b|\b(voicemails?|voice ?memos?|podcast|recordings?)\b`
	videoFile = `[\w./~-]*\.(mp4|mov|mkv|webm|avi|m4v)\b|\b(video|clip|footage|screencast|screen ?recording)s?\b`
	imageWord = `images?|photos?|pictures?|screenshots?|logos?|png|jpe?g|webp`
	many      = `all|every|each|many|batch|bulk|whole|\d{2,}|hundreds?|thousands?`
)

// catalog lists the use cases a prompt can be recognized as. Use cases missing here (generic or internal
// ones such as qa-factual, quick-code-question or tool-dispatch-router) never trigger a rule hint; a rule
// on them still applies to work Claude chooses to delegate.
var catalog = []work{
	// Code
	{id: "sql-query-write", kind: "code", label: "SQL writing", pats: []string{re(`\bsql\b|\bschema change\b|\b(write|optimi[sz]e)\b[^\n]{0,30}\bquer(y|ies)\b`)}},
	{id: "devops-config", kind: "code", label: "Dockerfile, CI and deployment config", pats: []string{re(`\b(dockerfile|docker-?compose|compose file|kubernetes|k8s|helm chart|github actions?|gitlab ci|ci (config|pipeline|workflow))\b`)}},
	{id: "error-triage", kind: "code", label: "error and incident triage", pats: []string{re(near(`root cause|triage|diagnos\w*|what caused|why`, `logs?|errors?|incidents?|crash(es|ed)?|stack ?traces?|tracebacks?|exceptions?|panics?|outages?`, 120))}},
	{id: "small-scoped-task", kind: "code", keep: true, label: "small scoped edits", pats: []string{re(`\b(typos?|lint (errors?|warnings?)|fix the lint)\b`)}},
	{id: "bulk-refactor", kind: "code", label: "mechanical renames and API migrations", pats: []string{re(near(`rename|replace|migrate|switch`, `across|everywhere|all (files|call ?sites|uses|usages|callers)|codebase|whole repo`, 60))}},
	{id: "call-trace", kind: "code", label: "call tracing", pats: []string{re(`\b(call (graph|flow|chain|trace)|who calls|callers of|trace (the )?(calls?|flow|dependencies))\b`)}},
	{id: "code-analysis", kind: "code", label: "architecture and code-quality reports", pats: []string{re(`\b(architecture (review|report|overview)|code quality|quality report|tech(nical)? debt)\b`)}},
	{id: "commit-pr-text", kind: "code", keep: true, label: "commit messages and PR descriptions", pats: []string{re(`\b(commit messages?|pr description|pull request description)\b`)}},
	{id: "security-critical-code", kind: "code", keep: true, label: "auth, crypto and payment code", pats: []string{re(near(`write|change|implement|fix|add`, `auth\w*|login|sessions?|crypto\w*|encrypt\w*|permission checks?|payments?`, 40))}},
	{id: "prod-db-migration", kind: "code", keep: true, label: "production database migrations", pats: []string{re(near(`migrations?|migrate|drop|truncate`, `prod(uction)?|database|db|tables?`, 40))}},
	{id: "iac-firewall-change", kind: "code", keep: true, label: "infrastructure, firewall and IAM changes", pats: []string{re(`\b(terraform|opentofu|firewall rules?|network acls?|iam polic(y|ies))\b`)}},
	{id: "test-generate-filter", kind: "code", label: "bulk test generation for coverage", pats: []string{re(`\b(raise|increase|improve|boost) (the )?(test )?coverage\b|\b(kill|catch) (the )?mutants?\b`)}},
	{id: "fuzz-harness-gen", kind: "code", label: "fuzz harnesses", pats: []string{re(`\bfuzz(ing|er|ers)?\b|\bfuzz (targets?|harness(es)?)\b`)}},

	// Agent work
	{id: "code-implement-feature", kind: "agent", label: "feature implementation", pats: []string{re(near(`implement|add|build`, `features?|functions?|endpoints?|commands?|flags?|options?|support for`, 40))}},
	{id: "code-debug-fix", kind: "agent", label: "bug fixing", pats: []string{re(near(`fix|debug`, `bugs?|failing|failures?|crash(es)?|errors?|tests?|issues?|regressions?`, 40))}},
	{id: "code-file-edits", kind: "agent", label: "many-file edits", pats: []string{re(`\b(across|in) (all|every|many|multiple|several) (the )?(files|modules|packages)\b`)}},
	{id: "background-long-task", kind: "agent", label: "long background tasks", pats: []string{re(`\bin the background\b`)}},
	{id: "rescue-debug", kind: "agent", label: "stuck bugs", pats: []string{re(near(`stuck|can'?t figure out|cannot figure out|no idea why`, `bugs?|errors?|failures?|crash(es)?|tests?|issues?`, 60))}},
	{id: "app-scaffold", kind: "agent", label: "project scaffolding", pats: []string{re(`\bscaffold\b|\b(set up|bootstrap|start|create) (a |an )?(new )?(project|app|service|repo)\b`)}},
	{id: "repo-scan-explore", kind: "agent", label: "repository exploration", pats: []string{re(`\bwhere (is|are|do|does)\b[^\n]{0,40}\b(defined|handled|implemented|used|called|set)\b|\bhow does\b[^\n]{0,40}\bwork\b|\bfind (all )?(the )?(uses|usages|callers|references)\b`)}},
	{id: "test-generation", kind: "agent", label: "unit-test writing", pats: []string{re(near(`write|add|create|generate|draft|cover`, `(unit |integration |regression |table-driven |e2e )?tests?|test (cases|suites?|files?)`, 40))}},
	{id: "docgen", kind: "agent", label: "docstrings and API docs", pats: []string{re(`\b(docstrings?|jsdoc|godoc|javadoc|api (docs?|documentation)|doc comments?)\b`)}},
	{id: "security-audit", kind: "agent", label: "security audits", pats: []string{re(`\b(security (audit|review)|vulnerabilit(y|ies)|pen ?test|exploitable|injection flaws?)\b`)}},
	{id: "migration-test-gated", kind: "agent", label: "test-gated migrations", pats: []string{re(`\bmigrat(e|ion)\b[^\n]{0,30}\bfrom\b[^\n]{0,40}\bto\b`)}},

	// Review
	{id: "code-review-diff", kind: "review", label: "code review", pats: []string{re(`\bcode review\b|\breview\b[^\n]{0,40}\b(diff|pr|pull request|branch|changes?|commits?|patch|code|files?|modules?)\b|\b(check|find|look for|hunt for|spot)\b[^\n]{0,40}\bbugs?\b`)}},
	{id: "precommit-check", kind: "review", label: "pre-commit checks", pats: []string{re(`\b(pre-?commit (check|review)|before (i|we) (commit|merge))\b`)}},
	{id: "dual-review-high-risk", kind: "review", label: "high-risk change review", pats: []string{re(near(`review|check`, `auth\w*|migrations?|deletions?|delete paths?`, 40))}},
	{id: "adversarial-design-review", kind: "review", label: "design challenges", pats: []string{re(near(`challenge|poke holes|critique|stress-?test`, `design|approach|architecture|assumptions`, 40))}},
	{id: "plan-redteam", kind: "review", label: "plan red-teaming", pats: []string{re(near(`red.?team|poke holes`, `plan`, 30))}},
	{id: "consensus-decision", kind: "review", label: "multi-model consensus", pats: []string{re(`\b(poll|ask) (2|3|two|three|several|multiple|other) models\b|\bconsensus\b`)}},
	{id: "anti-sycophancy-check", kind: "review", label: "push-back checks", pats: []string{re(`\b(push back on|devil'?s advocate|am i wrong)\b`)}},
	{id: "thinkdeep-edge-cases", kind: "review", label: "edge-case reasoning", pats: []string{re(`\b(think (hard|deeply|through)|reason)\b[^\n]{0,40}\bedge cases?\b|\bwhat edge cases\b`)}},
	{id: "debug-second-hypothesis", kind: "review", label: "second root-cause hypotheses", pats: []string{re(`\b(second|another|independent|alternative) (root.?cause )?(hypothes[ie]s|opinion on (the|this) (bug|cause|crash|failure))\b`)}},
	{id: "cross-family-judge", kind: "review", label: "output judging", pats: []string{re(near(`judge|score|grade|rate`, `outputs?|answers?|responses?|completions?`, 30))}},
	{id: "scheduled-check", kind: "review", label: "recurring checks", pats: []string{re(near(`every (day|morning|hour|night|week)|daily|nightly|hourly|recurring`, `checks?|reports?|alerts?`, 40))}},
	{id: "legal-text-review", kind: "review", keep: true, label: "license and terms review", pats: []string{re(near(`licen[cs]es?|terms of (service|use)|eula|privacy policy`, `review|risks?|summari[sz]e|compatib\w*`, 40))}},

	// Text
	{id: "library-docs-lookup", kind: "text", label: "library docs lookup", pats: []string{re(`\b(look up|fetch|check)\b[^\n]{0,30}\b(docs|documentation|api reference)\b`)}},
	{id: "tech-explainer", kind: "text", label: "technical explanations", pats: []string{re(`\bexplain\b`)}},
	{id: "repo-offload-summary", kind: "text", label: "repository summaries", pats: []string{re(near(`summari[sz]e|overview of`, `repo|repository|directory|folder|codebase`, 40))}},
	{id: "brainstorm", kind: "text", label: "brainstorming", pats: []string{re(`\bbrainstorm\w*\b|\b(alternatives|other options|other approaches) (for|to)\b`)}},
	{id: "plan-decompose", kind: "text", label: "task decomposition", pats: []string{re(`\b(break (this|it) (down|into steps)|decompose|order the steps)\b`)}},
	{id: "math-reasoning", kind: "text", label: "math and calculation", pats: []string{re(`\b(derive|derivation|prove that|integral|solve (the|this) equation|calculate|compute the)\b`)}},
	{id: "web-search-answer", kind: "text", label: "web search answers", pats: []string{re(`\b(search the web|look (it|this|that) up online|search online|latest news)\b`)}},
	{id: "deep-web-research", kind: "text", label: "cited research reports", pats: []string{re(`\b(deep research|research report|literature review|market research)\b|\bresearch\b[^\n]{0,60}\bwith (sources|citations)\b`)}},
	{id: "bulk-classify-tag", kind: "text", label: "bulk classification and triage", pats: []string{re(near(`classify|classification|categori[sz]e|tag|label|triage|sort`, `tickets?|issues?|emails?|messages?|items?|records?|rows?|entries|reviews?|feedback|requests?|inbox|logs?|files?`, 60))}},
	{id: "data-extract-structured", kind: "media", label: "structured data extraction", pats: []string{re(near(`extract|pull out|parse|normali[sz]e|capture`, `schema|json|fields?|terms|attributes|entities|key (data|details|values)`, 100) + `|\b(terms|fields|clauses)\b[^\n]{0,60}\bfrom\b[^\n]{0,40}\b(contracts?|agreements?|documents?|pdfs?|forms?|filings?)\b`)}},
	{id: "data-transform-format", kind: "text", label: "data conversion and normalization", pats: []string{re(near(`convert|transform|normali[sz]e|reformat`, `csv|json|yaml|xml|tsv|records?|rows?|formats?|schema|listings?|feed`, 60))}},
	{id: "batch-offline-map", kind: "text", label: "large batch jobs", pats: []string{re(`\b(thousands|\d{4,}) (of )?(items|rows|records|documents|files|entries|tickets|messages)\b`)}},
	{id: "summarize-long-doc", kind: "text", label: "document summaries", pats: []string{re(`\bsummari[sz](e|ing)\b|\bsummary of\b|\btl;?dr\b`)}},
	{id: "log-digest", kind: "text", label: "log digests", pats: []string{re(near(`summari[sz]e|condense|digest|key events|skim`, `logs?|ci output|build output`, 60))}},
	{id: "translate-text", kind: "text", label: "translation", pats: []string{re(`\btranslat(e|ing|ion)\b`)}},
	{id: "content-writing", kind: "text", label: "docs and posts", pats: []string{re(near(`write|draft`, `readme|docs|documentation|blog posts?|articles?|guides?|tutorials?`, 30))}},
	{id: "support-reply-draft", kind: "text", keep: true, label: "support replies", pats: []string{re(near(`draft|write`, `repl(y|ies)|responses?`, 30)), re(`\b(tickets?|issues?|customers?|emails?|support)\b`)}},
	{id: "embeddings-semantic-search", kind: "text", label: "embeddings and semantic search", pats: []string{re(`\b(embeddings?|semantic search|vector (search|index))\b`)}},
	{id: "finance-data-analysis", kind: "text", keep: true, label: "financial data analysis", pats: []string{re(near(`financial|trading|stocks?|portfolio|p&l|revenue|ledger`, `analy[sz]e|analysis`, 40))}},
	{id: "academic-writing", kind: "text", label: "academic writing", pats: []string{re(`\b(abstract for|bibliography|citation (format|style)|format (the )?citations)\b`)}},
	{id: "marketing-seo-copy", kind: "text", label: "marketing copy", pats: []string{re(`\b(landing (page )?copy|meta descriptions?|seo|marketing copy|ad copy|taglines?)\b`)}},
	{id: "npc-dialogue-roleplay", kind: "text", label: "game dialogue", pats: []string{re(`\b(npcs?|in-character|dialogue lines?)\b`)}},
	{id: "creative-fiction", kind: "text", label: "fiction", pats: []string{re(`\b(short story|novel|fiction|story about)\b`)}},
	{id: "incident-postmortem", kind: "text", label: "incident postmortems", pats: []string{re(`\b(post-?mortem|incident (report|timeline|review))\b`)}},
	{id: "release-notes", kind: "text", label: "release notes", pats: []string{re(`\b(release notes|changelog)\b`)}},
	{id: "i18n-catalog-sync", kind: "text", label: "locale file syncing", pats: []string{re(near(`locales?|i18n|l10n|translations?|po files?|xliff|strings files?|language files?`, `missing|new|add|sync|update|behind|untranslated|translate`, 100))}},
	{id: "record-dedup", kind: "text", label: "record deduplication", pats: []string{re(`\b(dedup\w*|de-dup\w*|duplicates?)\b`)}},
	{id: "rerank-retrieval", kind: "text", label: "reranking", pats: []string{re(`\bre-?rank\w*\b`)}},
	{id: "untrusted-content-screen", kind: "text", label: "prompt-injection screening", pats: []string{re(`\b(prompt injections?|screen (this|the) (input|content|text))\b`)}},
	{id: "pii-redaction", kind: "text", label: "PII redaction", pats: []string{re(`\b(redact\w*|pii|anonymi[sz]e)\b`)}},

	// Media
	{id: "frontend-ui-build", kind: "media", keep: true, label: "UI building", pats: []string{re(near(`build|make|create|implement`, `ui|components?|landing page|front-?end|pages? from`, 30))}},
	{id: "science-paper-explain", kind: "media", label: "paper explanations", pats: []string{re(near(`explain|summari[sz]e|extract`, `papers?|arxiv|preprints?`, 40))}},
	{id: "ocr-degraded-image", kind: "media", label: "OCR", pats: []string{re(`\bocr\b|\b(read|extract|transcribe) (the )?text (from|in|off)\b[^\n]{0,20}\b(` + imageWord + `|scans?)\b`)}},
	{id: "ui-grounding", kind: "media", label: "UI element locating", pats: []string{re(`\bbounding box(es)?\b|\b(pixel )?coordinates of\b`)}},
	{id: "huge-pdf", kind: "media", label: "huge PDFs", pats: []string{re(`\.pdf\b|\bpdfs?\b`), re(`\b(\d{3,} pages|huge|too (big|large)|scanned)\b`)}},
	{id: "audio-transcribe", kind: "media", label: "audio transcription", pats: []string{re(audioFile + `|\btranscri(be|ption|pt)\b`)}},
	{id: "audio-transcribe-noisy", kind: "media", label: "noisy audio transcription", pats: []string{re(audioFile), re(`\b(radio|phone|field|archival|noisy|voicemails?|scanner|calls?)\b`)}},
	{id: "subtitles-srt", kind: "media", label: "subtitles", pats: []string{re(`\b(subtitles?|captions? file)\b|\.(srt|vtt)\b`)}},
	{id: "audio-events", kind: "media", label: "audio event detection", pats: []string{re(`\b(sound|audio) events\b|\b(clicks|alarms|beeps|music cues)\b`)}},
	{id: "audio-tone", kind: "media", label: "tone and delivery judging", pats: []string{re(near(`tone|delivery|mood`, `voice ?-?overs?|takes?|recordings?|audio`, 30))}},
	{id: "video-understand", kind: "media", label: "video understanding", pats: []string{videoRe.String()}},
	{id: "video-bug-repro", kind: "media", label: "bug repro from screen recordings", pats: []string{re(`\b(screen ?recordings?|screencasts?)\b`)}},
	{id: "video-url-digest", kind: "media", label: "video link digests", pats: []string{videoLinkRe.String()}},
	{id: "video-timestamp-ocr", kind: "media", label: "on-screen text at a timestamp", pats: []string{re(videoFile), re(`\b(at|around) \d{1,2}:\d{2}\b`)}},
	{id: "video-structure-scan", kind: "media", label: "video scene scans", pats: []string{re(`\bscene (changes|cuts|detection)\b`)}},
	{id: "video-transcript-timestamped", kind: "media", label: "timestamped video transcripts", pats: []string{re(`\btimestamped transcripts?\b`)}},
	{id: "invoice-receipt-extract", kind: "media", label: "invoice and receipt extraction", pats: []string{re(near(`invoices?|receipts?|purchase orders?`, `extract\w*|pull|parse|capture|fields?|line items?|totals?|into`, 120))}},
	{id: "pdf-table-extract", kind: "media", label: "PDF table extraction", pats: []string{re(near(`tables?`, `pdfs?|statements?`, 40))}},
	{id: "image-alt-text", kind: "media", label: "alt text", pats: []string{re(`\balt[- ]?text\b`)}},

	// Generate
	{id: "image-generate", kind: "generate", label: "image generation", pats: []string{genRe.String()}},
	{id: "image-edit", kind: "generate", label: "image editing", pats: []string{re(near(`edit|retouch|recolou?r|remove the background|change the background|upscale`, imageWord, 40))}},
	{id: "tts-narration", kind: "generate", label: "voiceover narration", pats: []string{re(`\b(voice ?-?over|narrat(e|ion)|text.to.speech|tts|read (it|this) aloud)\b`)}},
	{id: "music-sfx", kind: "generate", label: "music and sound effects", pats: []string{re(`\b(jingle|sound effects?|sfx|notification sound|music track)\b`)}},
	{id: "video-generate", kind: "generate", label: "video generation", pats: []string{re(near(`generate|make|create|produce`, `(promo )?videos?|b-roll|promo clips?`, 30))}},
	{id: "image-to-video", kind: "generate", label: "image animation", pats: []string{re(near(`animate`, `images?|stills?|photos?|pictures?`, 30))}},
	{id: "video-dub", kind: "generate", label: "video dubbing", pats: []string{re(`\b(dub|dubbing|re-?voice)\b`)}},
}

// unmatched are catalog use cases with no prompt pattern: too generic or internal to recognize from a
// prompt (TestWorkMatchesCatalog keeps this list and catalog in step with the route table).
var unmatched = []string{"quick-code-question", "shell-command-exec", "agentic-multifile-change", "long-agent-session",
	"limit-fallback-coding", "over-1m-context", "session-memory-compaction", "stop-hook-review-gate", "qa-factual",
	"tool-dispatch-router", "yes-no-gate", "agent-workflow-exec", "voice-prompt"}

var compileOnce sync.Once

// compileCatalog compiles the patterns on first use, so the tool hook never pays for them.
func compileCatalog() {
	for i := range catalog {
		for _, p := range catalog[i].pats {
			catalog[i].all = append(catalog[i].all, regexp.MustCompile(p))
		}
	}
}

// matchHead is how much of a prompt is matched: the request is at its start, and pasted bulk text after it
// would only add false matches (and time).
const matchHead = 3000

// matchWork returns the catalog entries a prompt looks like, in catalog order.
func matchWork(p string) []*work {
	compileOnce.Do(compileCatalog)
	if len(p) > matchHead {
		p = p[:matchHead]
	}
	var out []*work
	for i := range catalog {
		w := &catalog[i]
		ok := true
		for _, r := range w.all {
			if !r.MatchString(p) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, w)
		}
	}
	return out
}
