package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.store.ListPackages(r.Context())
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func (s *Server) handleGetPackage(w http.ResponseWriter, r *http.Request) {
	pkg, err := s.store.GetPackage(r.Context(), r.PathValue("name"))
	if mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (s *Server) handleUpsertPackage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		Type          int32  `json:"type"` // 0 top-level, 1 addon
		Version       string `json:"version"`
		ContentBase64 string `json:"content_base64"`
		Signature     string `json:"signature"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "package name is required")
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.ContentBase64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "content_base64 is not valid base64")
		return
	}
	sum := sha256.Sum256(content)
	pkg := model.Package{
		Name:        body.Name,
		Type:        body.Type,
		Version:     body.Version,
		ContentHash: hex.EncodeToString(sum[:]),
		Signature:   body.Signature,
		Size:        int64(len(content)),
	}
	if err := s.store.UpsertPackage(r.Context(), pkg, content); mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (s *Server) handleDeletePackage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePackage(r.Context(), r.PathValue("name")); mapStoreErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePackageContent serves raw package bytes. This is the download URL handed
// to agents; it is intentionally readable without the admin token.
func (s *Server) handlePackageContent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	content, err := s.store.GetPackageContent(r.Context(), name)
	if mapStoreErr(w, err) {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
