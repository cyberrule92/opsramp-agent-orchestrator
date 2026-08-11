package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opsramp"
)

// handleOpsRampGetConfig returns the current connector config with the secret
// masked (never returned to the browser).
func (s *Server) handleOpsRampGetConfig(w http.ResponseWriter, r *http.Request) {
	set := s.opsramp.Settings() // secret already cleared
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":              set.BaseURL,
		"tenant_id":             set.TenantID,
		"client_key":            set.ClientKey,
		"poll_interval_seconds": set.PollIntervalSeconds,
		"enabled":               s.opsramp.IsEnabled(),
		"secret_set":            s.opsramp.HasSecret(),
	})
}

// handleOpsRampSetConfig validates and applies new connector config. An empty
// client_secret keeps the previously stored one.
func (s *Server) handleOpsRampSetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL             string `json:"base_url"`
		TenantID            string `json:"tenant_id"`
		ClientKey           string `json:"client_key"`
		ClientSecret        string `json:"client_secret"`
		PollIntervalSeconds int    `json:"poll_interval_seconds"`
		Enabled             *bool  `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	set := model.OpsRampSettings{
		BaseURL:             trimURL(body.BaseURL),
		TenantID:            body.TenantID,
		ClientKey:           body.ClientKey,
		ClientSecret:        body.ClientSecret,
		PollIntervalSeconds: body.PollIntervalSeconds,
		Enabled:             enabled,
	}
	if err := s.opsramp.Reconfigure(r.Context(), set); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": s.opsramp.IsEnabled()})
}

// handleOpsRampStatus reports configured/authenticated state and inventory size.
func (s *Server) handleOpsRampStatus(w http.ResponseWriter, r *http.Request) {
	set := s.opsramp.Settings()
	resp := map[string]any{"configured": s.opsramp.IsEnabled(), "tenant_id": set.TenantID}
	if s.opsramp.IsEnabled() {
		if err := s.opsramp.Ping(r.Context()); err != nil {
			resp["authenticated"] = false
			resp["auth_error"] = err.Error()
		} else {
			resp["authenticated"] = true
		}
		if exp, ok := s.opsramp.TokenExpiry(); ok {
			resp["token_expires_at"] = exp.UTC().Format(time.RFC3339)
		}
	}
	if n, err := s.store.CountOpsRampAgents(r.Context()); err == nil {
		resp["inventory_count"] = n
	}
	writeJSON(w, http.StatusOK, resp)
}

// connSettingsBody is the connector config shape accepted by the set/test
// endpoints. Empty fields fall back to what is already stored.
type connSettingsBody struct {
	BaseURL             string `json:"base_url"`
	TenantID            string `json:"tenant_id"`
	ClientKey           string `json:"client_key"`
	ClientSecret        string `json:"client_secret"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

// handleOpsRampTest authenticates a set of connector settings and probes their
// tenant without saving anything, so an operator can validate credentials from
// the UI before committing them. With an empty body it tests the saved
// connection. A failed check is reported as ok:false with 200 — the request
// itself succeeded, and the UI renders the reason inline.
func (s *Server) handleOpsRampTest(w http.ResponseWriter, r *http.Request) {
	var body connSettingsBody
	if err := readJSON(r, &body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	check, err := s.opsramp.CheckSettings(r.Context(), model.OpsRampSettings{
		BaseURL:      trimURL(body.BaseURL),
		TenantID:     body.TenantID,
		ClientKey:    body.ClientKey,
		ClientSecret: body.ClientSecret,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "authenticated": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            check.TenantOK,
		"authenticated": true,
		"tenant_ok":     check.TenantOK,
		"tenant_error":  check.TenantError,
		"token_type":    "Bearer",
		"access_token":  check.Token,
		"expires_at":    check.ExpiresAt.UTC().Format(time.RFC3339),
		"expires_in":    int(time.Until(check.ExpiresAt).Seconds()),
	})
}

// handleOpsRampToken returns the connector's current bearer token, acquiring or
// refreshing it when it has expired ("refresh": true forces a new one). It
// hands out the credential on purpose — operators reproduce API calls with it —
// so it is a POST, which the admin token gates when one is configured.
func (s *Server) handleOpsRampToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Refresh bool `json:"refresh"`
	}
	if err := readJSON(r, &body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	token, expiry, err := s.opsramp.Token(r.Context(), body.Refresh)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type":   "Bearer",
		"access_token": token,
		"expires_at":   expiry.UTC().Format(time.RFC3339),
		"expires_in":   int(time.Until(expiry).Seconds()),
	})
}

// handleOpsRampAgents lists the synced OpsRamp agent inventory from the store.
func (s *Server) handleOpsRampAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListOpsRampAgents(r.Context())
	if mapStoreErr(w, err) {
		return
	}
	if agents == nil {
		agents = []model.OpsRampAgent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "count": len(agents)})
}

// handleOpsRampSync triggers an immediate inventory sync.
func (s *Server) handleOpsRampSync(w http.ResponseWriter, r *http.Request) {
	n, err := s.opsramp.SyncNow(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": n})
}

// requireClient returns the active client, with its access token checked and
// refreshed if expired, or writes a 503.
func (s *Server) requireClient(w http.ResponseWriter, r *http.Request) (*opsramp.Client, bool) {
	c, ok := s.opsramp.CurrentClient()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "OpsRamp connector is not enabled; configure it first")
		return nil, false
	}
	if err := c.EnsureToken(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "OpsRamp authentication failed: "+err.Error())
		return nil, false
	}
	return c, true
}

// handleOpsRampAgentInfo proxies GET /agents/{platform}/info.
func (s *Server) handleOpsRampAgentInfo(w http.ResponseWriter, r *http.Request) {
	c, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	info, err := c.GetAgentInfo(r.Context(), r.PathValue("platform"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeRawJSON(w, info)
}

// handleOpsRampUpdates proxies POST /agents/updates.
func (s *Server) handleOpsRampUpdates(w http.ResponseWriter, r *http.Request) {
	c, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	body := readAnyBody(w, r)
	out, err := c.ConfigureAutoUpdates(r.Context(), body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeRawJSON(w, out)
}

// handleOpsRampAssignPolicy proxies POST /agentPolicies/{policyId}/devices.
func (s *Server) handleOpsRampAssignPolicy(w http.ResponseWriter, r *http.Request) {
	c, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	body := readAnyBody(w, r)
	out, err := c.AssignPolicyDevices(r.Context(), r.PathValue("policyId"), body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeRawJSON(w, out)
}

// handleOpsRampAssignProfile proxies POST /agentProfiles/{profileId}/devices.
func (s *Server) handleOpsRampAssignProfile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	body := readAnyBody(w, r)
	out, err := c.AssignProfileDevices(r.Context(), r.PathValue("profileId"), body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeRawJSON(w, out)
}

// handleOpsRampDownload proxies an agent package download stream.
func (s *Server) handleOpsRampDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	pkg := r.PathValue("pkg")
	rc, ct, err := c.DownloadAgent(r.Context(), r.PathValue("platform"), pkg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer rc.Close()
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+pkg+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func readAnyBody(w http.ResponseWriter, r *http.Request) any {
	var body any
	if err := readJSON(r, &body); err != nil && err != io.EOF {
		// A malformed body is non-fatal for these passthroughs; send as nil.
		return nil
	}
	return body
}

func writeRawJSON(w http.ResponseWriter, raw json.RawMessage) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func trimURL(u string) string {
	for len(u) > 0 && (u[len(u)-1] == '/' || u[len(u)-1] == ' ') {
		u = u[:len(u)-1]
	}
	return u
}
