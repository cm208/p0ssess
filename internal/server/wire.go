package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/funnel"
	"p0ssess/internal/notices"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/hook"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/relay"
)

// claudeBaseline is the model a receipt's saving is measured against: what the same tokens would cost
// on Claude.
const claudeBaseline = "anthropic/claude-sonnet-5.5"

// Wire gives a relay engine the direct path (for read-only work that needs the web or names no
// repository) and the receipt line with its saving.
func Wire(re *relay.Engine, cfg config.Config, eng *local.Engine) {
	re.SetDirect(directRunner(cfg, eng))
	re.SetReceipt(func(r *relay.Result) string {
		line, saved := receiptSaved(eng, r.Model, r.CostUSD, r.CostEstimated, r.Tokens)
		record(eng, notices.Record{Run: r.SessionID + r.RunID, Notes: append(append([]string{}, r.Retries...), r.Note, r.Chosen),
			Receipt: line, Model: r.Model, CostUSD: r.CostUSD, SavedUSD: saved})
		return line
	})
}

// receipt is the one line that goes with an answer: (model, $cost, saved ~$X).
func receipt(eng *local.Engine, model string, cost float64, estimated bool, t *opencode.Tokens) string {
	line, _ := receiptSaved(eng, model, cost, estimated, t)
	return line
}

func receiptSaved(eng *local.Engine, model string, cost float64, estimated bool, t *opencode.Tokens) (string, float64) {
	r := relay.DefaultReceipt(&relay.Result{Model: model, CostUSD: cost, CostEstimated: estimated})
	if r == "" || t == nil || eng == nil {
		return r, 0
	}
	claude, ok := eng.TokenCost(claudeBaseline, t.Input, t.Output+t.Reasoning, t.Cache.Read, t.Cache.Write)
	if !ok || claude-cost < 0.005 {
		return r, 0
	}
	return strings.TrimSuffix(r, ")") + fmt.Sprintf(", saved ~$%.2f)", claude-cost), claude - cost
}

// notice keeps a run's notes out of the conversation: they go to the notices file the status line and
// the p0ssess page read.
func notice(eng *local.Engine, id string, lines []string, more ...string) {
	if eng == nil {
		return
	}
	for _, m := range more {
		if strings.TrimSpace(m) != "" {
			lines = append(lines, m)
		}
	}
	if len(lines) == 0 {
		return
	}
	record(eng, notices.Record{Run: id, Notes: lines})
}

func record(eng *local.Engine, r notices.Record) {
	if eng == nil {
		return
	}
	notices.Append(eng.Dirs.NoticesFile(), r)
	if r.Receipt != "" {
		funnel.Note(eng.Dirs, funnel.FirstRoute, eng.Now())
	}
	if r.SavedUSD > 0 {
		funnel.Note(eng.Dirs, funnel.FirstSaving, eng.Now())
	}
}

// directRunner runs relay work on the direct path: the task as the prompt, the files it names that the
// direct path takes, web search when it needs current information. With no model or use case, the
// task's own use case is tried, then the generic ones for its shape.
func directRunner(cfg config.Config, eng *local.Engine) relay.DirectRunner {
	return func(ctx context.Context, t relay.DirectTask) (*relay.Result, error) {
		req := direct.Request{Model: t.Model, Prompt: t.Task, Web: t.Web}
		var skipped []string
		for _, f := range t.Files {
			if _, err := direct.Inspect(f, direct.Limits{}); err == nil {
				req.Files = append(req.Files, f)
			} else {
				skipped = append(skipped, filepath.Base(f))
			}
		}
		if t.MaxCostUSD > 0 {
			req.MaxCostUSD, req.MaxGiven = t.MaxCostUSD, true
		}
		var cands []string
		for _, uc := range []string{t.UseCase, hook.UseCaseFor(t.Task)} {
			if uc != "" && uc != "web-search-answer" {
				cands = append(cands, uc)
			}
		}
		if t.Web {
			cands = append(cands, "deep-web-research")
		} else {
			cands = append(cands, "summarize-long-doc", "qa-factual")
		}
		if t.Model != "" {
			cands = []string{t.UseCase}
		}
		start := time.Now()
		var res *direct.Result
		var runID string
		var err error
		for _, uc := range cands {
			res, runID, err = RunDirect(ctx, cfg, eng, req, DirectJob{UseCase: uc, Stakes: t.Stakes})
			if err == nil || res != nil && res.Billed() || !strings.Contains(err.Error(), "no model fits") && !strings.Contains(err.Error(), "unknown use case") {
				break
			}
		}
		if res == nil {
			if err == nil {
				err = errors.New("the direct path returned nothing")
			}
			return nil, err
		}
		out := &relay.Result{Answer: strings.TrimSpace(res.Text), Status: "done", Model: res.Model, Mode: "read", CostUSD: res.Usage.CostUSD,
			CostEstimated: !res.Measured(), Route: "direct", RunID: runID, DurationS: time.Since(start).Round(100 * time.Millisecond).Seconds(),
			Tokens: &opencode.Tokens{Input: float64(res.Usage.PromptTokens), Output: float64(res.Usage.CompletionTokens), Reasoning: float64(res.Usage.ReasoningTokens)}}
		for _, f := range res.Files {
			out.Files = append(out.Files, direct.FileHint{Path: f.Path, Kind: direct.FileKind(f.Path, f.MIME)})
		}
		out.Retries = append(out.Retries, res.Warnings...)
		if len(skipped) > 0 {
			out.Retries = append(out.Retries, "not attached (the direct path cannot take them): "+strings.Join(skipped, ", "))
		}
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
		}
		return out, err
	}
}
