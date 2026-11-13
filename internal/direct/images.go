package direct

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
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
		Usage    *apiUsage `json:"usage"`
		Provider string    `json:"provider"`
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
	c.settleCost(ctx, res, p, ir.ID, ir.Usage, "the image API reported no cost")
	return nil
}

// imageResolution turns what a caller may say ("1024", "1024px", "1024x1024", "2k") into a value the
// route accepts. The image API takes 512, 768, 1K, 1.5K, 2K or 4K; Gemini on chat takes 1K, 2K or 4K. A
// pixel size picks the smallest value at least that large.
func imageResolution(v, route string) (string, error) {
	type opt struct {
		name string
		px   float64
	}
	opts := []opt{{"512", 512}, {"768", 768}, {"1K", 1024}, {"1.5K", 1536}, {"2K", 2048}, {"4K", 4096}}
	if route == "chat" {
		opts = []opt{{"1K", 1024}, {"2K", 2048}, {"4K", 4096}}
	}
	s := strings.ToLower(strings.TrimSpace(v))
	for _, o := range opts {
		if s == strings.ToLower(o.name) {
			return o.name, nil
		}
	}
	s = strings.TrimSuffix(s, "px")
	px := 0.0
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == '*' || r == '×' }) {
		part = strings.TrimSpace(part)
		mult := 1.0
		if strings.HasSuffix(part, "k") {
			part, mult = strings.TrimSuffix(part, "k"), 1024
		}
		f, err := strconv.ParseFloat(part, 64)
		if err != nil || f <= 0 {
			px = 0
			break
		}
		px = math.Max(px, f*mult)
	}
	if px == 0 {
		names := make([]string, len(opts))
		for i, o := range opts {
			names[i] = o.name
		}
		return "", fmt.Errorf("resolution %q: use one of %s, or a size in pixels", v, strings.Join(names, ", "))
	}
	for _, o := range opts {
		if px <= o.px {
			return o.name, nil
		}
	}
	return opts[len(opts)-1].name, nil
}
