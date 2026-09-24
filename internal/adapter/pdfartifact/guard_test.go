package pdfartifact

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestFailClosedResultProcessorRejectsPDFBlob(t *testing.T) {
	input := session.NewToolResultWithParts("call-1", "tool summary", []session.Content{{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", Data: []byte("%PDF-private-%%EOF")}})
	_, err := (FailClosedResultProcessor{}).ProcessToolResult(context.Background(), "s1", input)
	if err == nil {
		t.Fatal("PDF blob passed without externalization")
	}
	plain := session.NewToolResult("call-1", "safe")
	got, err := (FailClosedResultProcessor{}).ProcessToolResult(context.Background(), "s1", plain)
	if err != nil || got.Content != plain.Content {
		t.Fatalf("plain result = %+v, %v", got, err)
	}
}
