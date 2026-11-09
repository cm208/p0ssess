package opencode

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The contract test checks every operation, parameter and field this client uses against the pinned spec.
// When OpenCode is upgraded, drop the new /openapi.json next to it and add the version to SupportedVersions.

type spec struct {
	Paths      map[string]map[string]operation `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

type operation struct {
	OperationID string `json:"operationId"`
	Parameters  []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

func loadSpec(t *testing.T) *spec {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi-2.2.5.json")
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// props returns the property names of a schema, following $ref, anyOf and allOf.
func (s *spec) props(raw json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	var node struct {
		Ref        string                     `json:"$ref"`
		Properties map[string]json.RawMessage `json:"properties"`
		AnyOf      []json.RawMessage          `json:"anyOf"`
		AllOf      []json.RawMessage          `json:"allOf"`
		Items      json.RawMessage            `json:"items"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return out
	}
	if node.Ref != "" {
		return s.props(s.Components.Schemas[node.Ref[strings.LastIndex(node.Ref, "/")+1:]])
	}
	for k, v := range node.Properties {
		out[k] = v
	}
	for _, sub := range append(node.AnyOf, node.AllOf...) {
		for k, v := range s.props(sub) {
			out[k] = v
		}
	}
	if len(node.Items) > 0 {
		for k, v := range s.props(node.Items) {
			out[k] = v
		}
	}
	return out
}

// hasPath checks a dotted field path such as "data.tokens.cache.read".
func (s *spec) hasPath(raw json.RawMessage, path string) bool {
	cur := raw
	for _, part := range strings.Split(path, ".") {
		p := s.props(cur)
		next, ok := p[part]
		if !ok {
			return false
		}
		cur = next
	}
	return true
}

// jsonFields lists the JSON names of a struct type's fields.
func jsonFields(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

func TestContract(t *testing.T) {
	s := loadSpec(t)
	cases := []struct {
		method, path string
		query        []string
		body         []string
		resp         []string
	}{
		{"get", "/api/info", nil, nil, []string{"version", "pid", "urls"}},
		{"post", "/api/session", nil, jsonFields(CreateSession{}), []string{
			"data.id", "data.cost", "data.tokens.input", "data.tokens.output", "data.tokens.reasoning",
			"data.tokens.cache.read", "data.tokens.cache.write", "data.outcome", "data.time.busy", "data.time.idle",
			"data.location.directory", "data.model.id", "data.model.providerID", "data.permissions", "data.metadata"}},
		{"get", "/api/session/{sessionID}", nil, nil, []string{"data.id", "data.cost", "data.outcome"}},
		{"post", "/api/session/{sessionID}/prompt", nil, jsonFields(Prompt{}), []string{"data.id", "data.sessionID", "data.type", "data.delivery"}},
		{"post", "/api/session/{sessionID}/model", nil, []string{"model"}, nil},
		{"post", "/api/session/{sessionID}/interrupt", []string{"resume"}, nil, []string{"interrupted"}},
		{"post", "/api/experimental/session/{sessionID}/wait", nil, nil, nil},
		{"get", "/api/session/{sessionID}/message", []string{"limit", "order", "type", "cursor"}, nil, []string{
			"data.id", "data.type", "data.content", "data.finish", "data.cost", "data.tokens", "data.error", "data.text", "cursor.next"}},
		{"get", "/api/session/{sessionID}/diff", nil, nil, []string{"data.file", "data.patch", "data.additions", "data.deletions", "data.status"}},
		{"get", "/api/session/{sessionID}/permission", nil, nil, []string{"data.id", "data.sessionID", "data.action", "data.resources", "data.save"}},
		{"post", "/api/session/{sessionID}/permission/{requestID}/reply", nil, []string{"decision", "message"}, nil},
		{"get", "/api/event", []string{"types"}, nil, nil},
		{"get", "/api/experimental/session/{sessionID}/log", []string{"after", "follow"}, nil, nil},
	}
	for _, c := range cases {
		op, ok := s.Paths[c.path][c.method]
		if !ok {
			t.Errorf("%s %s: not in spec", c.method, c.path)
			continue
		}
		for _, q := range c.query {
			if !slices.ContainsFunc(op.Parameters, func(p struct {
				Name string `json:"name"`
				In   string `json:"in"`
			}) bool {
				return p.Name == q && p.In == "query"
			}) {
				t.Errorf("%s %s: query parameter %q not in spec", c.method, c.path, q)
			}
		}
		if len(c.body) > 0 {
			if op.RequestBody == nil {
				t.Errorf("%s %s: spec has no request body", c.method, c.path)
				continue
			}
			bp := s.props(op.RequestBody.Content["application/json"].Schema)
			for _, f := range c.body {
				if _, ok := bp[f]; !ok {
					t.Errorf("%s %s: body field %q not in spec", c.method, c.path, f)
				}
			}
		}
		for _, f := range c.resp {
			found := false
			for code, r := range op.Responses {
				if strings.HasPrefix(code, "2") && s.hasPath(r.Content["application/json"].Schema, f) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s: response field %q not in spec", c.method, c.path, f)
			}
		}
	}
}

func TestContractNestedShapes(t *testing.T) {
	s := loadSpec(t)
	sc := s.Components.Schemas
	check := func(schema string, fields []string) {
		p := s.props(sc[schema])
		for _, f := range fields {
			if _, ok := p[f]; !ok {
				t.Errorf("%s: field %q not in spec", schema, f)
			}
		}
	}
	check("Model.Ref", jsonFields(ModelRef{}))
	check("Permission.Rule", jsonFields(Rule{}))
	check("Location.PublicRef", jsonFields(Location{}))
	check("PromptInput.FileAttachment", jsonFields(FileAttachment{}))
	check("FileDiff.Info", jsonFields(FileDiff{}))
	check("Permission.Request", jsonFields(PermissionRequest{}))
	check("Session.StructuredError", []string{"type", "message", "status"})
	check("Session.Message.Assistant.Tool", []string{"type", "id", "name", "state"})
	check("Session.Message.ToolState.Completed", []string{"status", "input", "content"})
	check("Session.Message.ToolState.Error", []string{"status", "input", "error"})
	check("Session.Message.Assistant.Text", []string{"type", "text"})

	var reply struct {
		Enum []string `json:"enum"`
	}
	_ = json.Unmarshal(sc["Permission.Reply"], &reply)
	for _, d := range []string{"once", "reject", "always"} {
		if !slices.Contains(reply.Enum, d) {
			t.Errorf("Permission.Reply: %q missing", d)
		}
	}
	var effect struct {
		Enum []string `json:"enum"`
	}
	_ = json.Unmarshal(sc["Permission.Effect"], &effect)
	for _, e := range []string{"allow", "deny", "ask"} {
		if !slices.Contains(effect.Enum, e) {
			t.Errorf("Permission.Effect: %q missing", e)
		}
	}
}

func TestContractEventTypes(t *testing.T) {
	raw, err := os.ReadFile("testdata/openapi-2.2.5.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	b := doc.Paths["/api/event"]["get"]
	for _, ev := range []string{
		EvExecutionStarted, EvExecutionSucceeded, EvExecutionFailed, EvExecutionInterrupted,
		EvStepStarted, EvStepEnded, EvStepFailed, EvTextEnded, EvToolInputStarted, EvToolCalled,
		EvToolSuccess, EvToolFailed, EvRetryScheduled, EvUsageUpdated, EvPermissionAsked,
		EvPermissionReplied, EvFormCreated, EvSessionStatus,
	} {
		if !strings.Contains(string(b), `"`+ev+`"`) {
			t.Errorf("event type %q not in the /api/event types enum", ev)
		}
	}
}
