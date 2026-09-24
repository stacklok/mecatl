package pdfartifact

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// FailClosedResultProcessor is the temporary processor installed whenever PDF
// storage is enabled before PDF tool-result externalization is available. It
// stops recognized inline PDF blobs before any durable or model-facing record.
type FailClosedResultProcessor struct{}

var _ port.ToolResultProcessor = FailClosedResultProcessor{}

var errPDFExternalizationUnavailable = errors.New("PDF tool result externalization is unavailable")

func (FailClosedResultProcessor) ProcessToolResult(_ context.Context, _ session.SessionID, result session.ToolResult) (session.ToolResult, error) {
	for _, part := range result.Parts {
		if part.BlockKind == session.BlockEmbeddedResource && len(part.Data) > 0 && (strings.EqualFold(part.MIMEType, "application/pdf") || strings.HasPrefix(string(part.Data), "%PDF-")) {
			return session.ToolResult{}, errPDFExternalizationUnavailable
		}
	}
	return result, nil
}
