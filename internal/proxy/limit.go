package proxy

import (
	"encoding/json"
	"math"
)

// Rates are a model's list prices in USD per million tokens, for sizing a call's output limit.
type Rates struct {
	InPerM  float64 `json:"usd_in_per_m,omitempty"`
	OutPerM float64 `json:"usd_out_per_m,omitempty"`
}

// MinOutputTokens is the smallest output limit CapOutput sets: below it a reasoning model can spend the
// whole limit thinking. A run with less room than that is stopped by its cost cap instead.
const MinOutputTokens = 1024

// bytesPerToken is a conservative ratio for the prompt a request carries.
const bytesPerToken = 4.0

// OutputLimit is the most output tokens a call may ask for so that it fits restUSD: what is left after
// its prompt, at the model's output price. 0 means no limit can be worked out (no price, or no cap).
func OutputLimit(restUSD float64, promptBytes int, r Rates) int {
	if !(r.OutPerM > 0) || !(restUSD > 0) || math.IsInf(restUSD, 0) {
		return 0
	}
	room := restUSD - float64(promptBytes)/bytesPerToken*r.InPerM/1e6
	n := int(math.Floor(room / (r.OutPerM / 1e6)))
	return max(n, MinOutputTokens)
}

// CapOutput lowers a model request's output limit to n tokens, or sets one when the request has none.
// Without a limit OpenRouter reserves the model's whole maximum output against the key, which can be
// dollars for a run capped at cents, and refuses the call when the key's limit cannot cover it. A limit
// already at or below n is kept. path picks the field: the Responses API's max_output_tokens, else
// max_tokens and max_completion_tokens (both are lowered when both are sent).
func CapOutput(method, path string, body []byte, n int) []byte {
	if n <= 0 || !ModelCall(method, path) {
		return body
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	fields := []string{"max_tokens", "max_completion_tokens"}
	if path == "/api/v1/responses" {
		fields = []string{"max_output_tokens"}
	}
	set := false
	changed := false
	for _, f := range fields {
		raw, ok := req[f]
		if !ok || string(raw) == "null" {
			continue
		}
		set = true
		var v float64
		if json.Unmarshal(raw, &v) == nil && v > 0 && v <= float64(n) {
			continue
		}
		req[f], _ = json.Marshal(n)
		changed = true
	}
	if !set {
		req[fields[0]], _ = json.Marshal(n)
		changed = true
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}
