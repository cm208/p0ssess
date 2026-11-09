package opencode

import "encoding/json"

// Shapes follow the OpenCode 2.2.5 OpenAPI document in testdata/openapi-2.2.5.json.

type ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func (m ModelRef) String() string { return m.ProviderID + "/" + m.ID }

type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

type Location struct {
	Directory string `json:"directory"`
}

type Tokens struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

type CreateSession struct {
	Model       *ModelRef      `json:"model,omitempty"`
	Location    *Location      `json:"location,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Permissions []Rule         `json:"permissions,omitempty"`
}

type SessionTime struct {
	Created float64 `json:"created"`
	Updated float64 `json:"updated"`
	Busy    float64 `json:"busy,omitempty"`
	Idle    float64 `json:"idle,omitempty"`
}

type Session struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"projectID"`
	Model       *ModelRef      `json:"model,omitempty"`
	Cost        float64        `json:"cost"`
	Tokens      Tokens         `json:"tokens"`
	Outcome     string         `json:"outcome,omitempty"`
	Time        SessionTime    `json:"time"`
	Location    Location       `json:"location"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Permissions []Rule         `json:"permissions,omitempty"`
}

type FileAttachment struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

type Prompt struct {
	Text     string           `json:"text"`
	Files    []FileAttachment `json:"files,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
	Delivery string           `json:"delivery,omitempty"`
}

type InboxItem struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Delivery  string `json:"delivery"`
}

type PermissionRequest struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	Action    string         `json:"action"`
	Resources []string       `json:"resources"`
	Save      []string       `json:"save,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Message   string         `json:"message,omitempty"`
}

type PermissionReply struct {
	Decision string  `json:"decision"`
	Message  *string `json:"message,omitempty"`
}

type FileDiff struct {
	File      string `json:"file"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

type StructuredError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

// Message is the union Session.Message.Info; fields not used by a given type stay empty.
type Message struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	Agent    string           `json:"agent,omitempty"`
	Model    *ModelRef        `json:"model,omitempty"`
	Content  []ContentPart    `json:"content,omitempty"`
	Finish   string           `json:"finish,omitempty"`
	Cost     float64          `json:"cost,omitempty"`
	Tokens   *Tokens          `json:"tokens,omitempty"`
	Error    *StructuredError `json:"error,omitempty"`
	Time     map[string]any   `json:"time,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

type ContentPart struct {
	Type  string     `json:"type"`
	Text  string     `json:"text,omitempty"`
	ID    string     `json:"id,omitempty"`
	Name  string     `json:"name,omitempty"`
	State *ToolState `json:"state,omitempty"`
}

type ToolState struct {
	Status  string           `json:"status"`
	Input   json.RawMessage  `json:"input,omitempty"`
	Content []ToolContent    `json:"content,omitempty"`
	Error   *StructuredError `json:"error,omitempty"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// FinalText returns the concatenated text parts of an assistant message.
func (m Message) FinalText() string {
	s := ""
	for _, c := range m.Content {
		if c.Type == "text" {
			s += c.Text
		}
	}
	return s
}

type ServerInfo struct {
	Version string   `json:"version"`
	PID     int      `json:"pid"`
	URLs    []string `json:"urls"`
}
