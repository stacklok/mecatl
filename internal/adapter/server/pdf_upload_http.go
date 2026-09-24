package server

import (
	"net/http"
	"net/url"

	"github.com/stacklok/mecatl/engine/session"
)

// uploadPDF receives a raw application/pdf body and streams it into the
// session-owned lifecycle. The only client metadata is the safe basename.
func (h *HTTPHandler) uploadPDF(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/pdf" {
		writeServiceError(w, ErrInvalidArgument)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["name"]) != 1 || values.Get("name") == "" {
		writeServiceError(w, ErrInvalidArgument)
		return
	}
	if r.ContentLength > maxPDFUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "PDF upload exceeds 20 MiB")
		return
	}
	// The service authorizes ownership before Stage performs any storage I/O.
	artifact, err := h.svc.UploadPdf(r.Context(), session.SessionID(r.PathValue("id")), values.Get("name"), r.Body)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ArtifactID string `json:"artifact_id"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		SHA256     string `json:"sha256"`
	}{ArtifactID: artifact.ID, Name: artifact.Name, Size: artifact.Size, SHA256: artifact.SHA256})
}

// downloadPDF reserves the reviewed binary route. The download slice replaces
// this fail-closed handler with a streaming reader; ownership is already checked
// by Service.DownloadPdf before any content could be returned.
func (h *HTTPHandler) downloadPDF(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DownloadPdf(r.Context(), session.SessionID(r.PathValue("id")), r.PathValue("artifact_id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeServiceError(w, ErrPDFArtifactsUnavailable)
}
