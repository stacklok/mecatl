package port

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPDFArtifactBlockRoutesAsText(t *testing.T) {
	block, err := session.NewPDFArtifactBlock("pdf-id", "tool.pdf", 42, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if got := session.ToolBlockText(block); got != "PDF artifact: tool.pdf (42 bytes)" {
		t.Fatalf("model-visible PDF summary = %q", got)
	}
	parts := RouteToolResultParts(session.ToolResult{Parts: []session.Content{session.NewTextBlock("before"), block}}, ProviderCapabilities{})
	if len(parts) != 2 || parts[1].ArtifactID != "pdf-id" || len(parts[1].Data) != 0 {
		t.Fatalf("mixed PDF tool result lost its reference-only block: %+v", parts)
	}
}
