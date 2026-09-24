package pdfartifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const toolResultPDFName = "artifact.pdf"

var errPDFResultUnsafe = errors.New("PDF tool result cannot be externalized")

// ResultProcessor replaces PDF embedded-resource blobs with private artifact
// references after post-tool hooks have selected the effective result.
type ResultProcessor struct {
	Artifacts server.PDFArtifactLifecycle
}

var _ port.ToolResultProcessor = ResultProcessor{}

// ProcessToolResult publishes PDF blobs and returns only safe references or an
// error, leaving non-PDF results unchanged.
func (p ResultProcessor) ProcessToolResult(ctx context.Context, id session.SessionID, result session.ToolResult) (session.ToolResult, error) {
	var pdfIndexes []int
	for i, block := range result.Parts {
		if block.BlockKind != session.BlockEmbeddedResource || len(block.Data) == 0 {
			continue
		}
		if strings.EqualFold(block.MIMEType, "application/pdf") {
			pdfIndexes = append(pdfIndexes, i)
			continue
		}
		// A misdeclared PDF must not slip through to a durable inline result.
		if bytes.HasPrefix(block.Data, []byte("%PDF-")) {
			return session.ToolResult{}, errPDFResultUnsafe
		}
	}
	if len(pdfIndexes) == 0 {
		return result, nil
	}
	if p.Artifacts == nil {
		return session.ToolResult{}, errPDFResultUnsafe
	}
	if err := session.ValidateToolResultParts(result.Parts); err != nil {
		return session.ToolResult{}, errPDFResultUnsafe
	}
	for _, index := range pdfIndexes {
		if len(result.Parts[index].Data) > MaxPDFBytes || len(result.Parts[index].Data) == 0 {
			return session.ToolResult{}, errPDFResultUnsafe
		}
		if duplicatedPDFPayload(result, index) {
			return session.ToolResult{}, errPDFResultUnsafe
		}
	}
	result.Parts = append([]session.Content(nil), result.Parts...)
	for _, index := range pdfIndexes {
		block := result.Parts[index]
		meta, err := p.Artifacts.Stage(ctx, id, toolResultPDFName, bytes.NewReader(block.Data))
		if err != nil {
			return session.ToolResult{}, errPDFResultUnsafe
		}
		reference, err := session.NewPDFArtifactBlock(meta.ID, meta.Name, meta.Size, meta.SHA256)
		if err != nil {
			return session.ToolResult{}, errPDFResultUnsafe
		}
		result.Parts[index] = reference
	}
	summary, ok := boundedPDFResultSummary(result.Parts)
	if !ok {
		return session.ToolResult{}, errPDFResultUnsafe
	}
	result.Content = summary
	return result, nil
}

func duplicatedPDFPayload(result session.ToolResult, pdfIndex int) bool {
	blob := result.Parts[pdfIndex].Data
	encoded := base64.StdEncoding.EncodeToString(blob)
	if containsPDFBytes(result.Content, blob, encoded) {
		return true
	}
	for i, block := range result.Parts {
		if i == pdfIndex {
			continue
		}
		if block.BlockKind == session.BlockEmbeddedResource && len(block.Data) > 0 && strings.EqualFold(block.MIMEType, "application/pdf") {
			// This block is also replaced. Even identical PDF attachments are
			// allowed; the guard concerns bytes left in surviving fields.
			continue
		}
		if bytes.Contains(block.Data, blob) || containsPDFBytes(block.Text, blob, encoded) ||
			containsPDFBytes(block.URL, blob, encoded) || containsPDFBytes(block.Name, blob, encoded) ||
			containsPDFBytes(block.Title, blob, encoded) || containsPDFBytes(block.Description, blob, encoded) {
			return true
		}
	}
	return false
}

func containsPDFBytes(s string, blob []byte, encoded string) bool {
	return (len(s) >= len(blob) && strings.Contains(s, string(blob))) ||
		(len(s) >= len(encoded) && strings.Contains(s, encoded))
}

func boundedPDFResultSummary(parts []session.Content) (string, bool) {
	var other, pdf []string
	for _, block := range parts {
		text := session.ToolBlockText(block)
		if text == "" {
			continue
		}
		if block.BlockKind == session.BlockPDFArtifact {
			pdf = append(pdf, text)
		} else {
			other = append(other, text)
		}
	}
	pdfText := strings.Join(pdf, "\n")
	if len(pdfText) > session.MaxToolResultTextBytes {
		return "", false
	}
	budget := session.MaxToolResultTextBytes - len(pdfText)
	if pdfText != "" && len(other) != 0 {
		budget-- // separator
	}
	if budget < 0 {
		return "", false
	}
	otherText := strings.Join(other, "\n")
	if len(otherText) > budget {
		otherText = otherText[:budget]
		for !utf8.ValidString(otherText) {
			otherText = otherText[:len(otherText)-1]
		}
	}
	if otherText == "" {
		return pdfText, true
	}
	if pdfText == "" {
		return otherText, true
	}
	return otherText + "\n" + pdfText, true
}
