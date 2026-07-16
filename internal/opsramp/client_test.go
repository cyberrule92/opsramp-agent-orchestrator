package opsramp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newTestServer returns a fake OpsRamp API and a client pointed at it.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(Config{
		BaseURL:      srv.URL,
		TenantID:     "client_42",
		ClientKey:    "KEY",
		ClientSecret: "SECRET",
		HTTPClient:   srv.Client(),
	})
	return c, srv
}

func writeToken(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: "test-token", TokenType: "bearer", ExpiresIn: 7199, Scope: "global:manage",
	})
}

func TestOAuthAndSearch(t *testing.T) {
	var tokenCalls int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tenancy/auth/oauth/token":
			atomic.AddInt32(&tokenCalls, 1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if got := r.Form.Get("grant_type"); got != "client_credentials" {
				t.Errorf("grant_type = %q", got)
			}
			if r.Form.Get("client_id") != "KEY" || r.Form.Get("client_secret") != "SECRET" {
				t.Errorf("bad client creds: %v", r.Form)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("content-type = %q", ct)
			}
			writeToken(w)

		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/tenants/client_42/resources/search":
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
				t.Errorf("authorization = %q", auth)
			}
			if qs := r.URL.Query().Get("queryString"); qs != "agentInstalled:true" {
				t.Errorf("queryString = %q", qs)
			}
			_, _ = w.Write([]byte(`{
				"results":[
					{"id":"r1","name":"web-01","hostName":"web-01","ipAddress":"10.0.0.1","resourceType":"LINUX","agentInstalled":true,"agentVersion":"21.0.0","state":"ACTIVE"},
					{"id":"r2","name":"web-02","agentInstalled":true,"agentVersion":"20.1.0","status":"UP"}
				],
				"totalResults":2,"pageNo":1,"pageSize":100,"totalPages":1,"nextPage":false}`))

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	})

	ctx := context.Background()
	res, err := c.SearchResources(ctx, "agentInstalled:true", 1, 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.TotalResults != 2 || len(res.Results) != 2 {
		t.Fatalf("got %d results (total %d)", len(res.Results), res.TotalResults)
	}
	if res.Results[0].ID != "r1" || res.Results[0].AgentVersion != "21.0.0" {
		t.Errorf("unexpected first result: %+v", res.Results[0])
	}
	if len(res.Results[0].Raw) == 0 {
		t.Errorf("raw payload not preserved")
	}

	// A second call must reuse the cached token (no new token request).
	if _, err := c.SearchResources(ctx, "agentInstalled:true", 1, 100); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if n := atomic.LoadInt32(&tokenCalls); n != 1 {
		t.Errorf("expected token to be cached (1 token call), got %d", n)
	}
}

func TestListAgentResourcesPaginates(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tenancy/auth/oauth/token" {
			writeToken(w)
			return
		}
		page := r.URL.Query().Get("pageNo")
		switch page {
		case "1":
			_, _ = w.Write([]byte(`{"results":[{"id":"a","agentInstalled":true}],"totalResults":2,"pageNo":1,"pageSize":200,"totalPages":2,"nextPage":true}`))
		case "2":
			_, _ = w.Write([]byte(`{"results":[{"id":"b","agentInstalled":true}],"totalResults":2,"pageNo":2,"pageSize":200,"totalPages":2,"nextPage":false}`))
		default:
			t.Errorf("unexpected pageNo %q", page)
		}
	})

	all, err := c.ListAgentResources(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].ID != "a" || all[1].ID != "b" {
		t.Fatalf("pagination failed: %+v", all)
	}
}

func TestAPIErrorSurfacesStatus(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tenancy/auth/oauth/token" {
			writeToken(w)
			return
		}
		http.Error(w, `{"code":"401","message":"unauthorized"}`, http.StatusUnauthorized)
	})
	_, err := c.GetAgentInfo(context.Background(), "linux")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != http.StatusUnauthorized || !strings.Contains(apiErr.Body, "unauthorized") {
		t.Errorf("unexpected api error: %+v", apiErr)
	}
}
