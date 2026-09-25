package server

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSDKPDFArtifacts_FoundationContentWire proves the scaffold carries
// reference-only PDF metadata across the domain and generated wire boundary.
func TestSDKPDFArtifacts_FoundationContentWire(t *testing.T) {
	sha := strings.Repeat("a", 64)
	pdf, err := session.NewPDFContent("opaque-id", "report.pdf", 123, sha)
	if err != nil {
		t.Fatal(err)
	}
	if pdf.Kind != session.MediaPDF || len(pdf.Data) != 0 || pdf.URL != "" {
		t.Fatalf("PDF prompt content is not reference-only: %+v", pdf)
	}
	pb := contentToProto([]session.Content{pdf})
	if len(pb) != 1 || pb[0].GetKind() != mecatlv1.Content_KIND_PDF || pb[0].GetArtifactId() != "opaque-id" || pb[0].GetSha256() != sha {
		t.Fatalf("PDF prompt wire projection lost reference: %+v", pb)
	}
	// Client input contains an ID only. Metadata on projections is server-filled.
	back, err := contentFromProto([]*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_PDF, MimeType: "application/pdf", ArtifactId: "opaque-id"}})
	if err != nil || len(back) != 1 || back[0].ArtifactID != "opaque-id" || back[0].SHA256 != "" {
		t.Fatalf("PDF prompt input = %+v, %v", back, err)
	}
	if _, err := contentFromProto(pb); err == nil {
		t.Fatal("client-supplied PDF metadata was accepted")
	}
	block, err := session.NewArtifactBlock("opaque-id", "report.pdf", "application/pdf", 123, sha)
	if err != nil {
		t.Fatal(err)
	}
	blocks := blocksToProto([]session.Content{block})
	if len(blocks) != 1 || blocks[0].GetKind() != mecatlv1.ContentBlock_KIND_ARTIFACT || blocks[0].GetArtifactId() != "opaque-id" || blocks[0].GetMimeType() != "application/pdf" {
		t.Fatalf("PDF result block wire projection lost reference: %+v", blocks)
	}
	if got := blocksFromProto(blocks); len(got) != 1 || got[0].BlockKind != session.BlockArtifact || got[0].SHA256 != sha || len(got[0].Data) != 0 || got[0].URL != "" {
		t.Fatalf("PDF result block roundtrip = %+v", got)
	}
	if got := toProtoSession(session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now()), ResolvedModel{}, nil, port.ProviderCapabilities{PDF: true}).GetSessionCapabilities().GetPdf(); !got {
		t.Fatal("session capability projection dropped PDF")
	}
	service := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService")
	for _, name := range []protoreflect.Name{"UploadArtifact", "DownloadArtifact"} {
		if service.Methods().ByName(name) == nil {
			t.Fatalf("generated service lacks %s", name)
		}
	}
	if field := (&mecatlv1.UploadArtifactResponse{}).ProtoReflect().Descriptor().Fields().ByName("mime_type"); field == nil || field.Number() != 5 {
		t.Fatalf("upload response MIME field = %v, want number 5", field)
	}
	if field := (&mecatlv1.ServerCapabilities{}).ProtoReflect().Descriptor().Fields().ByName("artifacts"); field == nil || field.Number() != 31 {
		t.Fatalf("artifact deployment capability = %v, want number 31", field)
	}
}
