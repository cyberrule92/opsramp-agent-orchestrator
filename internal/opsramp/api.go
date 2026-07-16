package opsramp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// --- Resources Search (agent inventory) ---
// GET /api/v2/tenants/{tenantId}/resources/search

// Resource is one resource (device) returned by the search API. OpsRamp returns
// many more fields; the raw payload is preserved for callers that need them.
type Resource struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	HostName       string          `json:"hostName"`
	AliasName      string          `json:"aliasName"`
	IPAddress      string          `json:"ipAddress"`
	ResourceType   string          `json:"resourceType"`
	ResourceName   string          `json:"resourceName"`
	AgentInstalled bool            `json:"agentInstalled"`
	AgentVersion   string          `json:"agentVersion"`
	State          string          `json:"state"`  // e.g. ACTIVE / MAINTENANCE
	Status         string          `json:"status"` // connectivity/monitoring status when present
	ClientUniqueID string          `json:"clientUniqueId"`
	Raw            json.RawMessage `json:"-"`
}

// SearchResult is the paged envelope returned by the search API.
type SearchResult struct {
	Results         []Resource `json:"results"`
	TotalResults    int        `json:"totalResults"`
	PageNo          int        `json:"pageNo"`
	PageSize        int        `json:"pageSize"`
	TotalPages      int        `json:"totalPages"`
	NextPage        bool       `json:"nextPage"`
	DescendingOrder bool       `json:"descendingOrder"`
}

// SearchResources runs a resource search. queryString uses OpsRamp query syntax,
// e.g. "agentInstalled:true". pageNo is 1-based.
func (c *Client) SearchResources(ctx context.Context, queryString string, pageNo, pageSize int) (*SearchResult, error) {
	if pageNo <= 0 {
		pageNo = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	q := url.Values{}
	if queryString != "" {
		q.Set("queryString", queryString)
	}
	q.Set("pageNo", strconv.Itoa(pageNo))
	q.Set("pageSize", strconv.Itoa(pageSize))
	q.Set("sortName", "name")
	q.Set("isDescendingOrder", "false")

	// Decode into a shape that also captures each result's raw JSON.
	var envelope struct {
		Results         []json.RawMessage `json:"results"`
		TotalResults    int               `json:"totalResults"`
		PageNo          int               `json:"pageNo"`
		PageSize        int               `json:"pageSize"`
		TotalPages      int               `json:"totalPages"`
		NextPage        bool              `json:"nextPage"`
		DescendingOrder bool              `json:"descendingOrder"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.tenantPath("resources/search"), q, nil, &envelope); err != nil {
		return nil, err
	}
	out := &SearchResult{
		TotalResults:    envelope.TotalResults,
		PageNo:          envelope.PageNo,
		PageSize:        envelope.PageSize,
		TotalPages:      envelope.TotalPages,
		NextPage:        envelope.NextPage,
		DescendingOrder: envelope.DescendingOrder,
	}
	for _, raw := range envelope.Results {
		var r Resource
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		r.Raw = raw
		out.Results = append(out.Results, r)
	}
	return out, nil
}

// ListAgentResources returns all resources with an agent installed, following
// pagination. It is the inventory the orchestrator uses to monitor agents.
func (c *Client) ListAgentResources(ctx context.Context) ([]Resource, error) {
	var all []Resource
	for page := 1; ; page++ {
		res, err := c.SearchResources(ctx, "agentInstalled:true", page, 200)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Results...)
		if !res.NextPage || len(res.Results) == 0 || page >= res.TotalPages {
			break
		}
	}
	return all, nil
}

// DeleteResource removes a resource (device) from the tenant. Used when
// decommissioning a host after its agent has been uninstalled.
// DELETE /api/v2/tenants/{tenantId}/resources/{resourceId}
func (c *Client) DeleteResource(ctx context.Context, resourceID string) error {
	return c.doJSON(ctx, http.MethodDelete, c.tenantPath("resources/"+url.PathEscape(resourceID)), nil, nil, nil)
}

// --- Agents management (develop.opsramp.com/v2/api/agents) ---

// GetAgentInfo returns agent package metadata for a platform (e.g. "linux").
// GET /api/v2/tenants/{tenantId}/agents/{platform}/info
func (c *Client) GetAgentInfo(ctx context.Context, platform string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.doJSON(ctx, http.MethodGet, c.tenantPath("agents/"+url.PathEscape(platform)+"/info"), nil, nil, &out)
	return out, err
}

// DownloadAgent streams an agent package.
// GET /api/v2/tenants/{tenantId}/agents/{platform}/download/{package-name}
// The caller owns the returned ReadCloser.
func (c *Client) DownloadAgent(ctx context.Context, platform, packageName string) (io.ReadCloser, string, error) {
	path := c.tenantPath("agents/" + url.PathEscape(platform) + "/download/" + url.PathEscape(packageName))
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil, "", "*/*")
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, "", &APIError{Status: resp.StatusCode, Body: string(body), Path: path}
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// GetDeployScript downloads the Linux agent installation shell script.
// GET /api/v2/tenants/{tenantId}/agents/deployAgentsScript?scriptType=SHELL
// The scriptType is required (the API 500s without it); SHELL yields the POSIX
// deployAgent.sh, the installer this orchestrator runs over SSH.
func (c *Client) GetDeployScript(ctx context.Context) ([]byte, error) {
	q := url.Values{"scriptType": {"SHELL"}}
	resp, err := c.do(ctx, http.MethodGet, c.tenantPath("agents/deployAgentsScript"), q, nil, "", "*/*")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(data), Path: "agents/deployAgentsScript"}
	}
	return data, nil
}

// ConfigureAutoUpdates sets agent auto-update behavior.
// POST /api/v2/tenants/{tenantId}/agents/updates
func (c *Client) ConfigureAutoUpdates(ctx context.Context, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.doJSON(ctx, http.MethodPost, c.tenantPath("agents/updates"), nil, body, &out)
	return out, err
}

// AssignPolicyDevices assigns resources to an agent resources policy.
// POST /api/v2/tenants/{tenantId}/agentPolicies/{policyId}/devices
func (c *Client) AssignPolicyDevices(ctx context.Context, policyID string, body any) (json.RawMessage, error) {
	var out json.RawMessage
	path := c.tenantPath("agentPolicies/" + url.PathEscape(policyID) + "/devices")
	err := c.doJSON(ctx, http.MethodPost, path, nil, body, &out)
	return out, err
}

// AssignProfileDevices assigns resources to a master agent profile.
// POST /api/v2/tenants/{tenantId}/agentProfiles/{profileId}/devices
func (c *Client) AssignProfileDevices(ctx context.Context, profileID string, body any) (json.RawMessage, error) {
	var out json.RawMessage
	path := c.tenantPath("agentProfiles/" + url.PathEscape(profileID) + "/devices")
	err := c.doJSON(ctx, http.MethodPost, path, nil, body, &out)
	return out, err
}

// AgentDownloadURL returns the (unauthenticated-looking) API URL for a package
// download, useful for surfacing in the UI. The actual request still needs auth.
func (c *Client) AgentDownloadURL(platform, packageName string) string {
	return fmt.Sprintf("%s%s", c.cfg.BaseURL,
		c.tenantPath("agents/"+url.PathEscape(platform)+"/download/"+url.PathEscape(packageName)))
}
