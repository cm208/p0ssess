package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	clientID     = "p0ssess-cli"
	deviceGrant  = "urn:ietf:params:oauth:grant-type:device_code"
	requestLimit = 15 * time.Second
)

// Client talks to the accounts API.
type Client struct {
	Base    string
	HTTP    *http.Client
	Version string
}

// NewClient returns a client for BaseURL(). version is the p0ssess version sent as part of client_name.
func NewClient(version string) *Client {
	return &Client{Base: BaseURL(), HTTP: &http.Client{Timeout: requestLimit}, Version: version}
}

// APIError is an error answer from the server.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("the server answered %d %s", e.Status, e.Code)
}

// IsInvalidToken reports a 401 invalid_token.
func IsInvalidToken(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// ErrNotEntitled is the answer to a ticket request on the free plan.
var ErrNotEntitled = errors.New("this needs p0ssess Pro")

type Entitlements struct {
	Plan                 string `json:"plan"`
	Status               string `json:"status"`
	Interval             string `json:"interval"`
	RenewsAt             string `json:"renews_at"`
	EndsAt               string `json:"ends_at"`
	SandboxHoursPerMonth int    `json:"sandbox_hours_per_month"`
	OrgID                string `json:"org_id"`
}

type AccountResponse struct {
	Account struct {
		ID string `json:"id"`
	} `json:"account"`
	Entitlements Entitlements `json:"entitlements"`
	Links        struct {
		Renew  string `json:"renew"`
		Manage string `json:"manage"`
	} `json:"links"`
}

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type Plan struct {
	ID          string `json:"id"`
	Tier        string `json:"tier"`
	Interval    string `json:"interval"`
	Recommended bool   `json:"recommended"`
	Price       struct {
		Display string `json:"display"`
	} `json:"price"`
	Placeholder bool  `json:"placeholder"`
	PerSeat     bool  `json:"per_seat"`
	Available   *bool `json:"available"`
}

// OK is false for a plan that is shown but cannot be bought yet.
func (p Plan) OK() bool { return p.Available == nil || *p.Available }

type Offer struct {
	Checkout string `json:"checkout"`
	Plans    []Plan `json:"plans"`
	StartURL string `json:"start_url"`
}

func (c *Client) do(ctx context.Context, method, path, token string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, requestLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestLimit}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error, Message string }
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.ToLower(strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "_"))
		}
		return resp.StatusCode, &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Message}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unreadable answer from the server: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Do sends one request to the accounts API and decodes a 2xx answer into out. An error answer is an
// *APIError carrying the server's code.
func (c *Client) Do(ctx context.Context, method, path, token string, in, out any) (int, error) {
	return c.do(ctx, method, path, token, in, out)
}

// ClientName is what the confirm page shows: version and platform only.
func (c *Client) ClientName() string {
	v := c.Version
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("p0ssess %s on %s/%s", v, runtime.GOOS, runtime.GOARCH)
}

func (c *Client) DeviceCode(ctx context.Context) (DeviceCode, error) {
	var out DeviceCode
	_, err := c.do(ctx, "POST", "/v1/device/code", "", map[string]string{"client_id": clientID, "client_name": c.ClientName()}, &out)
	if err == nil && (out.DeviceCode == "" || out.UserCode == "" || out.VerificationURIComplete == "") {
		err = errors.New("the server's link answer was incomplete")
	}
	return out, err
}

// DeviceToken polls once. The access token is returned on success; otherwise the error is an *APIError whose
// Code is authorization_pending, slow_down, access_denied or expired_token.
func (c *Client) DeviceToken(ctx context.Context, deviceCode string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_, err := c.do(ctx, "POST", "/v1/device/token", "", map[string]string{
		"grant_type": deviceGrant, "device_code": deviceCode, "client_id": clientID}, &out)
	if err == nil && out.AccessToken == "" {
		err = errors.New("the server's token answer was empty")
	}
	return out.AccessToken, err
}

func (c *Client) Account(ctx context.Context, token string) (AccountResponse, error) {
	var out AccountResponse
	_, err := c.do(ctx, "GET", "/v1/account", token, nil, &out)
	return out, err
}

// RevokeDevice signs this device out on the server.
func (c *Client) RevokeDevice(ctx context.Context, token string) error {
	_, err := c.do(ctx, "DELETE", "/v1/account/device", token, nil, nil)
	return err
}

func (c *Client) Offer(ctx context.Context) (Offer, error) {
	var out Offer
	_, err := c.do(ctx, "GET", "/v1/pro/offer", "", nil, &out)
	return out, err
}

func (c *Client) Waitlist(ctx context.Context, email, plan string) error {
	_, err := c.do(ctx, "POST", "/v1/waitlist", "", map[string]string{"email": email, "plan": plan, "source": "client"}, nil)
	return err
}

// Ticket is a short-lived signed ticket for another service, and where that service is.
type Ticket struct {
	Ticket     string    `json:"ticket"`
	ExpiresAt  time.Time `json:"expires_at"`
	SandboxURL string    `json:"sandbox_url"`
}

// Ticket trades the device token for a ticket for a service ("sandbox").
func (c *Client) Ticket(ctx context.Context, token, aud string) (Ticket, error) {
	var out Ticket
	_, err := c.do(ctx, "POST", "/v1/account/ticket", token, map[string]string{"aud": aud}, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
		err = ErrNotEntitled
	}
	return out, err
}
