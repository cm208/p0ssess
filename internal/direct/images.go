package direct

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// runImages uses the dedicated image API, for models not served on chat completions. Its documented
// provider fields are only/order/ignore/sort/allow_fallbacks, so data_collection is sent but not
// guaranteed to be honored there (docs/MEDIA.md).
func (c *Client) runImages(ctx context.Context, p *plan, res *Result, outDir string) error {
	body := map[string]any{
		"model":    p.model.ID,
		"prompt":   p.req.Prompt,
		"n":        1,
		"provider": c.provider(p),
	}
	if p.req.ImageAspect != "" {
		body["aspect_ratio"] = p.req.ImageAspect
	}
	if p.req.ImageResolution != "" {
		body["resolution"] = p.req.ImageResolution
	}
	var refs []any
	for _, in := range p.inputs {
		d, err := encodeFile(in.Path)
		if err != nil {
			return err
		}
		refs = append(refs, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + in.MIME + ";base64," + d}})
	}
	if len(refs) > 0 {
		body["input_references"] = refs
	}
	var ir struct {
		ID   string `json:"id"`
		Data []struct {
			B64       string `json:"b64_json"`
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
		} `json:"data"`
		Usage *struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			Cost             *float64 `json:"cost"`
		} `json:"usage"`
		Provider string `json:"provider"`
	}
	if err := c.post(ctx, "/images", body, &ir); err != nil {
		return err
	}
	if len(ir.Data) == 0 {
		return fmt.Errorf("openrouter: image API returned no images")
	}
	for i, d := range ir.Data {
		var f OutFile
		var err error
		switch {
		case d.B64 != "":
			var data []byte
			data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(d.B64))
			if err == nil {
				f, err = writeImage(data, d.MediaType, outDir, i+1)
			}
		case d.URL != "":
			f, err = c.saveImage(ctx, d.URL, outDir, i+1)
		default:
			err = fmt.Errorf("image %d has no data", i+1)
		}
		if err != nil {
			return err
		}
		res.Files = append(res.Files, f)
	}
	res.Text = fmt.Sprintf("generated %d image(s)", len(res.Files))
	res.Provider = ir.Provider
	if ir.ID != "" {
		res.IDs = append(res.IDs, ir.ID)
	}
	if ir.Usage != nil {
		res.Usage.PromptTokens = ir.Usage.PromptTokens
		res.Usage.CompletionTokens = ir.Usage.CompletionTokens
		if ir.Usage.Cost != nil {
			res.Usage.CostUSD, res.Usage.CostSource = *ir.Usage.Cost, "usage"
			return nil
		}
	}
	if ir.ID != "" {
		if cost, prov, err := c.generation(ctx, ir.ID); err == nil {
			res.Usage.CostUSD, res.Usage.CostSource = cost, "generation"
			if res.Provider == "" {
				res.Provider = prov
			}
			return nil
		}
	}
	res.Usage.CostSource = "unknown"
	res.Warnings = append(res.Warnings, "the image API reported no cost")
	return nil
}
