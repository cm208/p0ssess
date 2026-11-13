package cloudsandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"encoding/json"
)

// DefaultCloudURL is the hosted service's base URL; P0SSESS_CLOUD_URL overrides it.
const DefaultCloudURL = "https://cloud.p0ssess.me"

// ErrNotPro means the cloud sandbox needs a Pro or Team plan and this install has none.
var ErrNotPro = errors.New("the cloud sandbox needs p0ssess Pro")

// CloudURL is the accounts and engine base URL.
func CloudURL() string {
	if v := strings.TrimRight(os.Getenv("P0SSESS_CLOUD_URL"), "/"); v != "" {
		return v
	}
	return DefaultCloudURL
}

// DeviceToken reads the device token file; "" when the install is not linked to an account.
func DeviceToken(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(b))
	if !strings.HasPrefix(t, "p0d_") || strings.ContainsAny(t, " \t\r\n") {
		return ""
	}
	return t
}

// Ticket is a short-lived sandbox ticket and the host it is for.
type Ticket struct {
	Ticket     string    `json:"ticket"`
	ExpiresAt  time.Time `json:"expires_at"`
	SandboxURL string    `json:"sandbox_url"`
}

// FetchTicket trades the device token for a sandbox ticket (auth.md, "Sandbox hosts").
func FetchTicket(ctx context.Context, hc *http.Client, base, token string) (*Ticket, error) {
	if token == "" {
		return nil, ErrNotPro
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/account/ticket", bytes.NewReader([]byte(`{"aud":"sandbox"}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("p0ssess cloud: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, ErrNotPro
	default:
		return nil, readAPIError(resp)
	}
	var t Ticket
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(t.Ticket, "p0t1.") {
		return nil, errors.New("p0ssess cloud: malformed ticket")
	}
	return &t, nil
}
