package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

// downloadPDF streams one authorized object through the reviewed binary route.
func (h *HTTPHandler) downloadPDF(w http.ResponseWriter, r *http.Request) {
	meta, reader, err := h.svc.DownloadPdf(r.Context(), session.SessionID(r.PathValue("id")), r.PathValue("artifact_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safePDFAttachmentName(meta.Name)))
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.WriteHeader(http.StatusOK)
	_ = streamPDF(r.Context(), meta, reader, func(chunk []byte) error {
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	})
}

func safePDFAttachmentName(name string) string {
	if len(name) == 0 || len(name) > 255 {
		return "artifact.pdf"
	}
	var safe strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('_')
		}
	}
	if safe.String() == "." || safe.String() == ".." {
		return "artifact.pdf"
	}
	return safe.String()
}
