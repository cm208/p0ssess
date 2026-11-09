package direct

import (
	"context"
	"fmt"
	"strings"
)

func (c *Client) runSTT(ctx context.Context, p *plan, res *Result) error {
	in := p.inputs[0]
	d, err := encodeFile(in.Path)
	if err != nil {
		return err
	}
	body := map[string]any{
		"model":       p.model.ID,
		"input_audio": map[string]any{"data": d, "format": in.Format},
		"provider":    c.provider(p),
	}
	if p.req.Language != "" {
		body["language"] = p.req.Language
	}
	if p.req.Timestamps {
		body["response_format"] = "verbose_json"
		body["timestamp_granularities"] = []string{"segment"}
	}
	var tr struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Provider string `json:"provider"`
		Segments []struct {
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Text    string  `json:"text"`
			Speaker *int    `json:"speaker"`
		} `json:"segments"`
		Usage *apiUsage `json:"usage"`
	}
	if err := c.post(ctx, "/audio/transcriptions", body, &tr); err != nil {
		return err
	}
	if len(tr.Segments) > 0 {
		var b strings.Builder
		for _, s := range tr.Segments {
			fmt.Fprintf(&b, "[%s–%s]", clock(s.Start), clock(s.End))
			if s.Speaker != nil {
				fmt.Fprintf(&b, " speaker %d:", *s.Speaker)
			}
			b.WriteString(" " + strings.TrimSpace(s.Text) + "\n")
		}
		res.Text = strings.TrimRight(b.String(), "\n")
	} else {
		res.Text = tr.Text
	}
	res.Provider = tr.Provider
	if tr.ID != "" {
		res.IDs = append(res.IDs, tr.ID)
	}
	if res.Usage.add(tr.Usage) {
		return nil
	}
	if tr.ID != "" {
		c.lookupCost(ctx, res, tr.ID)
		return nil
	}
	res.Usage.CostSource = "unknown"
	res.Warnings = append(res.Warnings, "the transcription API reported no cost")
	return nil
}
