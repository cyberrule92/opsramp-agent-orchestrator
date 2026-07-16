package api

import (
	"net/http"

	"github.com/opsramp/opamp-orchestrator/internal/model"
	"github.com/opsramp/opamp-orchestrator/internal/opampserver"
)

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.store.GetGroup(r.Context(), r.PathValue("name"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleUpsertGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Description string            `json:"description"`
		Selector    map[string]string `json:"selector"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	g := model.Group{Name: name, Description: body.Description, Selector: body.Selector}
	if err := s.store.UpsertGroup(r.Context(), g); mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteGroup(r.Context(), r.PathValue("name")); mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetGroupConfig(w http.ResponseWriter, r *http.Request) {
	cv, err := s.store.GetCurrentConfig(r.Context(), r.PathValue("name"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, cv)
}

func (s *Server) handleListConfigVersions(w http.ResponseWriter, r *http.Request) {
	vers, err := s.store.ListConfigVersions(r.Context(), r.PathValue("name"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vers})
}

func (s *Server) handleCreateConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Files map[string]model.ConfigFile `json:"files"`
		Note  string                      `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Files) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one config file is required")
		return
	}
	if _, err := s.store.GetGroup(r.Context(), name); mapStoreErr(w, err) {
		return
	}
	hash := opampserver.ConfigMapHashHex(body.Files)
	cv, err := s.store.CreateConfigVersion(r.Context(), name, body.Files, hash, body.Note, actor(r))
	if mapStoreErr(w, err) {
		return
	}
	pushed := s.mgr.PushGroup(r.Context(), name)
	writeJSON(w, http.StatusCreated, map[string]any{"version": cv, "pushed": pushed})
}

func (s *Server) handleRollbackConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Version int `json:"version"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetCurrentConfigVersion(r.Context(), name, body.Version); mapStoreErr(w, err) {
		return
	}
	pushed := s.mgr.PushGroup(r.Context(), name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "current_version": body.Version, "pushed": pushed})
}

func (s *Server) handleListGroupPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.store.ListGroupPackages(r.Context(), r.PathValue("name"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func (s *Server) handleAssignPackage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Package string `json:"package"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.GetPackage(r.Context(), body.Package); mapStoreErr(w, err) {
		return
	}
	if err := s.store.AssignPackage(r.Context(), name, body.Package); mapStoreErr(w, err) {
		return
	}
	pushed := s.mgr.PushGroup(r.Context(), name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pushed": pushed})
}

func (s *Server) handleUnassignPackage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.UnassignPackage(r.Context(), name, r.PathValue("pkg")); mapStoreErr(w, err) {
		return
	}
	pushed := s.mgr.PushGroup(r.Context(), name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pushed": pushed})
}

func actor(r *http.Request) string {
	if u := r.Header.Get("X-Actor"); u != "" {
		return u
	}
	return "admin-api"
}
