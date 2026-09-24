package session

import (
	"strings"
	"testing"
)

func TestPDFContentMetadataValidation(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		id   string
		file string
		size int64
		sha  string
	}{
		{name: "empty ID", file: "x.pdf", size: 1, sha: sha},
		{name: "unsafe name", id: "id", file: "../x.pdf", size: 1, sha: sha},
		{name: "empty name", id: "id", size: 1, sha: sha},
		{name: "zero size", id: "id", file: "x.pdf", sha: sha},
		{name: "too large", id: "id", file: "x.pdf", size: 20<<20 + 1, sha: sha},
		{name: "bad digest", id: "id", file: "x.pdf", size: 1, sha: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPDFContent(tc.id, tc.file, tc.size, tc.sha); err == nil {
				t.Fatal("invalid PDF content metadata accepted")
			}
			if _, err := NewPDFArtifactBlock(tc.id, tc.file, tc.size, tc.sha); err == nil {
				t.Fatal("invalid PDF artifact block metadata accepted")
			}
		})
	}
	pdf, err := NewPDFContent("id", "x.pdf", 1, sha)
	if err != nil {
		t.Fatal(err)
	}
	if pdf.MIMEType != "application/pdf" || pdf.ArtifactID != "id" || pdf.SHA256 != sha || len(pdf.Data) != 0 || pdf.URL != "" {
		t.Fatalf("PDF prompt must contain bounded reference metadata only: %+v", pdf)
	}
	block, err := NewPDFArtifactBlock("id", "x.pdf", 1, sha)
	if err != nil {
		t.Fatal(err)
	}
	if block.BlockKind != BlockPDFArtifact || block.Kind != "" || block.MIMEType != "application/pdf" || len(block.Data) != 0 || block.URL != "" || block.ArtifactID != "id" {
		t.Fatalf("PDF block must contain bounded reference metadata only: %+v", block)
	}
	if err := ValidateToolResultParts([]Content{NewTextBlock("before"), block}); err != nil {
		t.Fatalf("reference-only PDF result block rejected: %v", err)
	}
}

func TestPDFPromptSizeCountsReferences(t *testing.T) {
	sha := strings.Repeat("a", 64)
	pdf, err := NewPDFContent("id", "x.pdf", 12<<20, sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMediaParts([]Content{pdf}); err != nil {
		t.Fatalf("single bounded PDF rejected: %v", err)
	}
	if err := ValidateMediaParts([]Content{pdf, pdf}); err == nil {
		t.Fatal("combined PDF references exceeded 20 MiB but passed")
	}
}

func TestValidateToolResultParts_PDFMixedBudget(t *testing.T) {
	pdf := Content{BlockKind: BlockEmbeddedResource, MIMEType: "application/pdf", Data: make([]byte, MaxPDFBytes)}
	mixed := []Content{NewTextBlock("before"), pdf, NewTextBlock("after")}
	if err := ValidateToolResultParts(mixed); err != nil {
		t.Fatalf("20 MiB PDF with adjacent text rejected: %v", err)
	}
	if err := ValidateToolResultParts(append(mixed, Content{BlockKind: BlockImage, Data: []byte{1}})); err == nil {
		t.Fatal("extra binary byte beyond the 20 MiB PDF budget accepted")
	}
	if err := ValidateToolResultParts([]Content{
		{BlockKind: BlockEmbeddedResource, MIMEType: "application/pdf", Data: pdf.Data[:MaxPDFBytes/2]},
		{BlockKind: BlockEmbeddedResource, MIMEType: "application/pdf", Data: pdf.Data[:MaxPDFBytes/2+1]},
	}); err == nil {
		t.Fatal("multiple PDFs exceeded the shared binary budget but passed")
	}
	text := strings.Repeat("x", MaxToolResultTextBytes)
	tooMuchText := make([]Content, MaxToolResultBytes/len(text)+1)
	for i := range tooMuchText {
		tooMuchText[i] = NewTextBlock(text)
	}
	if err := ValidateToolResultParts(append([]Content{pdf}, tooMuchText...)); err == nil {
		t.Fatal("PDF allowed non-PDF text to bypass its aggregate cap")
	}
}
