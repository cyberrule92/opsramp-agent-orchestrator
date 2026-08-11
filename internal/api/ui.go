package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

// indexETag identifies this build's dashboard. The page is embedded in the
// binary, so it changes only when the binary does. Serving a validator together
// with Cache-Control: no-cache makes the browser revalidate on every load and
// take a cheap 304 when nothing changed; without one, a heuristically cached
// copy can outlive a redeploy and hide the new UI entirely.
var indexETag = func() string {
	sum := sha256.Sum256(indexHTML)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}()

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", indexETag)
	w.Header().Set("Cache-Control", "no-cache")
	// ServeContent handles conditional requests against the ETag above.
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(indexHTML))
}
