package api

import (
	"errors"
	"net/http"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opampserver"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if mapStoreErr(w, err) {
		return
	}
	connected := s.mgr.ConnectedUIDs()
	for i := range agents {
		agents[i].ResolvedGroup = s.mgr.ResolveGroup(r.Context(), &agents[i])
		if connected[agents[i].InstanceUID] {
			agents[i].Status = "connected"
		} else if agents[i].Status == "connected" {
			// DB says connected but no live conn (e.g. after restart): correct it.
			agents[i].Status = "disconnected"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "count": len(agents)})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	agent, err := s.store.GetAgent(r.Context(), uid)
	if mapStoreErr(w, err) {
		return
	}
	agent.ResolvedGroup = s.mgr.ResolveGroup(r.Context(), agent)
	if s.mgr.ConnectedUIDs()[uid] {
		agent.Status = "connected"
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleSetAgentGroup(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var body struct {
		Group *string `json:"group"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetAgent(r.Context(), uid); mapStoreErr(w, err) {
		return
	}
	if body.Group != nil && *body.Group != "" {
		if _, err := s.store.GetGroup(r.Context(), *body.Group); mapStoreErr(w, err) {
			return
		}
	}
	if err := s.store.SetAgentGroup(r.Context(), uid, body.Group); mapStoreErr(w, err) {
		return
	}
	_ = s.store.AddEvent(r.Context(), uid, "group-assigned", groupLabel(body.Group))

	pushed := s.tryPush(r, uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pushed": pushed})
}

func (s *Server) handlePushAgent(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	err := s.mgr.PushToAgent(r.Context(), uid)
	if errors.Is(err, opampserver.ErrNotConnected) {
		writeJSON(w, http.StatusOK, map[string]any{"pushed": false, "reason": "agent not connected; will reconcile on next poll"})
		return
	}
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pushed": true})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(r.Context(), 200)
	if mapStoreErr(w, err) {
		return
	}
	if events == nil {
		events = []model.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// tryPush attempts a best-effort proactive push, returning whether it landed.
func (s *Server) tryPush(r *http.Request, uid string) bool {
	err := s.mgr.PushToAgent(r.Context(), uid)
	if err != nil {
		if !errors.Is(err, opampserver.ErrNotConnected) {
			s.log.Slog().Error("push after change", "uid", uid, "err", err)
		}
		return false
	}
	return true
}

func groupLabel(g *string) string {
	if g == nil || *g == "" {
		return "(auto)"
	}
	return *g
}
