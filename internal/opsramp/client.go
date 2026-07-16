// Package opsramp is a client for the OpsRamp v2 REST API, focused on the
// agents surface documented at https://develop.opsramp.com/v2/api/agents plus
// the Resources Search API used to inventory installed agents.
//
// OpsRamp agents are managed through this REST/OAuth API — they do not speak
// OpAMP — so this package is how the orchestrator "monitors" and manages them.
package opsramp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config holds OpsRamp connection settings.
type Config struct {
	// BaseURL is the API host, e.g. https://pod7.api.opsramp.com (no trailing slash).
	BaseURL string
	// TenantID is the client/tenant (MSP) id used in the /tenants/{tenantId}/ path.
	TenantID string
	// ClientKey / ClientSecret come from an OpsRamp Integration (OAuth2 client credentials).
	ClientKey    string
	ClientSecret string

	// HTTPClient is optional; a sane default is used when nil.
	HTTPClient *http.Client
}

// Enabled reports whether enough config is present to talk to OpsRamp.
func (c Config) Enabled() bool {
	return c.BaseURL != "" && c.TenantID != "" && c.ClientKey != "" && c.ClientSecret != ""
}

// Client is a thread-safe OpsRamp API client with transparent token caching.
type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// New builds a Client. It does not perform any network calls.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

// TenantID exposes the configured tenant id.
func (c *Client) TenantID() string { return c.cfg.TenantID }

// tokenResponse is the OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

// accessToken returns a valid bearer token, fetching/refreshing as needed.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Refresh a minute before expiry to avoid races on the boundary.
	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-time.Minute)) {
		return c.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.cfg.ClientKey)
	form.Set("client_secret", c.cfg.ClientSecret)

	endpoint := c.cfg.BaseURL + "/tenancy/auth/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth token: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("oauth token decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("oauth token: empty access_token")
	}
	c.token = tr.AccessToken
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	c.tokenExpiry = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.token, nil
}

// Ping verifies connectivity and credentials by acquiring a token.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.accessToken(ctx)
	return err
}

// APIError carries a non-2xx OpsRamp API response.
type APIError struct {
	Status int
	Body   string
	Path   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("opsramp api %s: status %d: %s", e.Path, e.Status, e.Body)
}

// tenantPath builds an /api/v2/tenants/{tenantId}/... path.
func (c *Client) tenantPath(suffix string) string {
	return fmt.Sprintf("/api/v2/tenants/%s/%s", c.cfg.TenantID, strings.TrimLeft(suffix, "/"))
}

// do performs an authenticated request, returning the raw response for the
// caller to handle (JSON or binary). The caller must close resp.Body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType, accept string) (*http.Response, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	u := c.cfg.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if accept == "" {
		accept = "*/*"
	}
	req.Header.Set("Accept", accept)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// doJSON performs an authenticated JSON request and decodes into out (may be nil).
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, reqBody any, out any) error {
	var rdr io.Reader
	ct := ""
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
		ct = "application/json"
	}
	resp, err := c.do(ctx, method, path, query, rdr, ct, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data)), Path: path}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}
