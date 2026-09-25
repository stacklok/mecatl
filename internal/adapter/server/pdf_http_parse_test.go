package server

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestHTTPPDFPromptPartRequiresReferenceOnly(t *testing.T) {
	parts, err := toContentParts([]promptContentBody{{Kind: "pdf", MimeType: "application/pdf", ArtifactID: "artifact"}})
	if err != nil || len(parts) != 1 || parts[0].Kind != session.MediaPDF || parts[0].ArtifactID != "artifact" {
		t.Fatalf("PDF reference = %+v, %v", parts, err)
	}
	if _, err := toContentParts([]promptContentBody{{Kind: "pdf", MimeType: "application/pdf", ArtifactID: "artifact", Data: []byte("inline")}}); err == nil {
		t.Fatal("inline PDF accepted")
	}
}
