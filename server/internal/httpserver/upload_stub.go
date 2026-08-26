package httpserver

import (
	"net/http"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// UploadFile is the Phase 1.3 contract landing ahead of its pipeline. The
// route-level gates run first — this 501 is only ever a statement about
// missing code, never an authorization answer — and the matrix registers the
// operation on the deliberate not-implemented list, so the handler cannot
// land without its row being tightened into real per-channel outcomes.
func (s *apiServer) UploadFile(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	writeError(w, r, http.StatusNotImplemented, codeNotImplemented,
		"file upload has not shipped yet")
}
