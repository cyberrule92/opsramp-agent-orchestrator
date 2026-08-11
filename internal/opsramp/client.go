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

// Token lifetime handling.
const (
	// tokenSkew refreshes a token slightly before it expires so an in-flight
	// request never carries a token that dies on the wire.
	tokenSkew = time.Minute
	// maxTokenTTL caps how long a token is trusted regardless of the expires_in
	// the token endpoint reports. OpsRamp hands out a tenant-wide token and
	// reports its *remaining* life, so an implausibly large value would
	// otherwise pin an already-dead token in the cache indefinitely.
	maxTokenTTL = 2 * time.Hour
	// minTokenTTL is used when the endpoint reports a non-positive expires_in:
	// re-check soon rather than trusting a token that claims to be expired.
	minTokenTTL = 5 * time.Minute
)

// Client is a thread-safe OpsRamp API client with transparent token caching.
// The cached token is refreshed before it expires, and re-fetched on demand
// when the API rejects it (see do).
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
// When force is true the cached token is discarded and a new one is fetched.
func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.tokenValidLocked() {
		return c.token, nil
	}
	return c.fetchTokenLocked(ctx)
}

// tokenValidLocked reports whether a cached token exists and is not about to
// expire. Caller holds mu.
func (c *Client) tokenValidLocked() bool {
	return c.token != "" && time.Now().Before(c.tokenExpiry.Add(-tokenSkew))
}

// fetchTokenLocked requests a new token and caches it. On failure the cached
// token is cleared so a later call re-authenticates rather than reusing a token
// that is likely dead. Caller holds mu.
func (c *Client) fetchTokenLocked(ctx context.Context) (string, error) {
	c.token = ""
	c.tokenExpiry = time.Time{}

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
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	switch {
	case ttl <= 0:
		ttl = minTokenTTL
	case ttl > maxTokenTTL:
		ttl = maxTokenTTL
	}
	c.tokenExpiry = time.Now().Add(ttl)
	return c.token, nil
}

// EnsureToken checks the cached access token and refreshes it when it is
// missing or expired. Call it before starting an operation that would be
// disruptive to fail partway through on an auth error; ordinary API calls
// refresh on their own.
func (c *Client) EnsureToken(ctx context.Context) error {
	_, err := c.accessToken(ctx, false)
	return err
}

// RefreshToken discards the cached token and acquires a new one.
func (c *Client) RefreshToken(ctx context.Context) error {
	_, err := c.accessToken(ctx, true)
	return err
}

// TokenExpiry reports when the cached token expires, and whether one is cached.
func (c *Client) TokenExpiry() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return time.Time{}, false
	}
	return c.tokenExpiry, true
}

// Ping verifies connectivity and credentials by acquiring a token.
func (c *Client) Ping(ctx context.Context) error {
	return c.EnsureToken(ctx)
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

// isAuthStatus reports whether a response status means the access token was
// rejected. OpsRamp answers an invalid or expired token with 407 and an
// InvalidTokenException body rather than the usual 401.
func isAuthStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusProxyAuthRequired
}

// do performs an authenticated request, returning the raw response for the
// caller to handle (JSON or binary). The caller must close resp.Body.
//
// A token can stop working before the expiry the token endpoint advertised —
// it may be revoked, or the tenant-wide token rotated — so a rejected token is
// re-fetched and the request retried once. body is kept as bytes so the retry
// can replay it.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, contentType, accept string) (*http.Response, error) {
	resp, err := c.doOnce(ctx, method, path, query, body, contentType, accept, false)
	if err != nil || !isAuthStatus(resp.StatusCode) {
		return resp, err
	}
	resp.Body.Close()
	return c.doOnce(ctx, method, path, query, body, contentType, accept, true)
}

// doOnce issues a single attempt; refresh forces a new token first.
func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body []byte, contentType, accept string, refresh bool) (*http.Response, error) {
	tok, err := c.accessToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	u := c.cfg.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
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
	var raw []byte
	ct := ""
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		raw = b
		ct = "application/json"
	}
	resp, err := c.do(ctx, method, path, query, raw, ct, "application/json")
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
