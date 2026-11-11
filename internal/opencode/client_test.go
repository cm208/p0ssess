package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A prompt that failed with an empty 500 used to read "HTTP 500: " and nothing else.
func TestAPIErrorNamesStatusAndBody(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, "pw")
	_, err := c.Prompt(context.Background(), "ses_1", Prompt{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500 Internal Server Error: (empty body)") {
		t.Errorf("empty body: %v", err)
	}
	body = strings.Repeat("e", 500)
	_, err = c.Prompt(context.Background(), "ses_1", Prompt{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), strings.Repeat("e", 300)+"…") || strings.Contains(err.Error(), strings.Repeat("e", 301)) {
		t.Errorf("long body: %v", err)
	}
}
