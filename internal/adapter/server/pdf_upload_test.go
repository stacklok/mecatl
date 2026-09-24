package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfUploadLifecycle struct {
	*pdfPromptLifecycle
	bytes []byte
	calls int
}

func (p *pdfUploadLifecycle) Stage(_ context.Context, id session.SessionID, name string, source io.Reader) (server.PDFArtifact, error) {
	p.calls++
	if id != "s-pdf" || name != "report.pdf" {
		return server.PDFArtifact{}, server.ErrNotFound
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return server.PDFArtifact{}, err
	}
	p.bytes = data
	return server.PDFArtifact{ID: "artifact", Name: name, Size: int64(len(data)), SHA256: strings.Repeat("a", 64)}, nil
}

func newPDFUploadService(t *testing.T, lifecycle server.PDFArtifactLifecycle) *server.Service {
	t.Helper()
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  memstore.New(), PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "s-pdf" }, PDFArtifacts: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestPDFUploadGRPCStreamsChunksAndRejectsBadMetadata(t *testing.T) {
	lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
	client, closeClient := dialGRPC(t, newPDFUploadService(t, lifecycle))
	defer closeClient()
	stream, err := client.UploadPdf(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "application/pdf"}}}); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.7\n%%EOF")
	if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: pdf}}); err != nil {
		t.Fatal(err)
	}
	response, err := stream.CloseAndRecv()
	if err != nil || response.GetArtifactId() != "artifact" || !bytes.Equal(lifecycle.bytes, pdf) {
		t.Fatalf("upload = %+v, %v; staged=%q", response, err, lifecycle.bytes)
	}
	bad, err := client.UploadPdf(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "missing", Name: "report.pdf", MimeType: "application/pdf"}}})
	_, err = bad.CloseAndRecv()
	if status.Code(err) != codes.NotFound || lifecycle.calls != 1 {
		t.Fatalf("missing session upload = %v, stage calls=%d", err, lifecycle.calls)
	}
	bad, err = client.UploadPdf(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "text/plain"}}})
	_, err = bad.CloseAndRecv()
	if status.Code(err) != codes.InvalidArgument || lifecycle.calls != 1 {
		t.Fatalf("wrong MIME upload = %v, stage calls=%d", err, lifecycle.calls)
	}
}

func TestPDFUploadHTTPStreamsBody(t *testing.T) {
	lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
	handler := server.NewHTTPHandler(newPDFUploadService(t, lifecycle))
	pdf := []byte("%PDF-1.7\n%%EOF")
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/s-pdf/pdfs?name=report.pdf", bytes.NewReader(pdf))
	req.Header.Set("Content-Type", "application/pdf")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusCreated || body.ArtifactID != "artifact" || !bytes.Equal(lifecycle.bytes, pdf) {
		t.Fatalf("HTTP upload status=%d body=%s staged=%q err=%v", rec.Code, rec.Body.String(), lifecycle.bytes, err)
	}
}
