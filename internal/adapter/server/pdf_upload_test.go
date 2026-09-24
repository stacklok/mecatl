package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfMemoryObjects struct{ data map[string][]byte }

func (m *pdfMemoryObjects) Put(_ context.Context, key string, source io.Reader) error {
	data, err := io.ReadAll(source)
	if err == nil {
		m.data[key] = data
	}
	return err
}
func (m *pdfMemoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.data[key]
	if !ok {
		return nil, errors.New("missing PDF object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *pdfMemoryObjects) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}
func (m *pdfMemoryObjects) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range m.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

type pdfFaultReader struct{}

func (pdfFaultReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestSDKPDFArtifacts_Scenario1_UploadPromptProvider pins the real upload
// boundary's opaque ID and bounded metadata. The same-named app test drives
// that reference through the selected native provider factory.
func TestSDKPDFArtifacts_Scenario1_UploadPromptProvider(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = metadata.Close() }()
	objects := &pdfMemoryObjects{data: make(map[string][]byte)}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  metadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID:        func() session.SessionID { return "s-pdf" },
		PDFArtifacts: pdfartifact.New(metadata, objects), DefaultCapabilities: port.ProviderCapabilities{PDF: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	owner, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte("%PDF-1.7\nprivate-payload\n%%EOF")
	meta, err := svc.UploadPdf(context.Background(), owner.ID, "report.pdf", bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.ID) != 48 {
		t.Fatalf("artifact ID length = %d, want opaque 24-byte hex ID", len(meta.ID))
	}
	if _, err := hex.DecodeString(meta.ID); err != nil {
		t.Fatalf("artifact ID is not hex: %v", err)
	}
	digest := sha256.Sum256(valid)
	if meta.Name != "report.pdf" || meta.Size != int64(len(valid)) || meta.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("upload metadata = %+v", meta)
	}
	if len(objects.data) != 1 {
		t.Fatalf("object count = %d, want one private PDF object", len(objects.data))
	}
	for _, data := range objects.data {
		if !bytes.Equal(data, valid) {
			t.Fatal("stored PDF object differs from uploaded bytes")
		}
	}
}

// TestSDKPDFArtifacts_Scenario1_RejectInvalidOrUnauthorized exercises the
// service against the real Redis/object lifecycle. Rejected inputs never reach
// a model call or publish a PDF record/object.
func TestSDKPDFArtifacts_Scenario1_RejectInvalidOrUnauthorized(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = metadata.Close() }()
	objects := &pdfMemoryObjects{data: make(map[string][]byte)}
	artifacts := pdfartifact.New(metadata, objects)
	llm := mockllm.New(mockllm.TextTurn("unexpected"))
	ids := []session.SessionID{"s-pdf", "s-other"}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}),
		Store:  metadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID:        func() session.SessionID { id := ids[0]; ids = ids[1:]; return id },
		PDFArtifacts: artifacts, DefaultCapabilities: port.ProviderCapabilities{PDF: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	owner, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte("%PDF-1.7\nprivate-payload\n%%EOF")
	meta, err := svc.UploadPdf(context.Background(), owner.ID, "report.pdf", bytes.NewReader(valid))
	if err != nil || meta.ID == "" || meta.Size != int64(len(valid)) {
		t.Fatalf("valid upload = %+v, %v", meta, err)
	}
	for _, tc := range []struct {
		name   string
		reader io.Reader
	}{
		{name: "unsafe name/../bad.pdf", reader: bytes.NewReader(valid)},
		{name: "bad-signature.pdf", reader: strings.NewReader("not-a-pdf\n%%EOF")},
		{name: "missing-eof.pdf", reader: strings.NewReader("%PDF-1.7\nno trailer")},
		{name: "too-large.pdf", reader: io.MultiReader(strings.NewReader("%PDF-"), strings.NewReader(strings.Repeat("x", pdfartifact.MaxPDFBytes+1)))},
		{name: "broken-stream.pdf", reader: io.MultiReader(strings.NewReader("%PDF-"), pdfFaultReader{})},
	} {
		name := strings.TrimPrefix(tc.name, "unsafe name/")
		if _, err := svc.UploadPdf(context.Background(), owner.ID, name, tc.reader); !errors.Is(err, server.ErrInvalidArgument) {
			t.Errorf("%s upload error = %v, want invalid argument", tc.name, err)
		}
	}
	badHTTP := httptest.NewRequest(http.MethodPost, "/v1/sessions/s-pdf/pdfs?name=bad.pdf", strings.NewReader("not-a-pdf\n%%EOF"))
	badHTTP.Header.Set("Content-Type", "application/pdf")
	badResponse := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(badResponse, badHTTP)
	if badResponse.Code != http.StatusBadRequest || !strings.Contains(badResponse.Body.String(), `"code":"invalid_argument"`) {
		t.Errorf("invalid PDF HTTP response = %d %s, want bounded invalid_argument", badResponse.Code, badResponse.Body.String())
	}
	if _, err := svc.UploadPdf(context.Background(), "unknown", "report.pdf", bytes.NewReader(valid)); !errors.Is(err, server.ErrNotFound) {
		t.Errorf("unknown session upload = %v, want not found", err)
	}
	for _, tc := range []struct {
		name string
		id   session.SessionID
		part session.Content
		want error
	}{
		{name: "foreign session", id: other.ID, part: session.Content{Kind: session.MediaPDF, MIMEType: "application/pdf", ArtifactID: meta.ID}, want: server.ErrNotFound},
		{name: "foreign artifact", id: owner.ID, part: session.Content{Kind: session.MediaPDF, MIMEType: "application/pdf", ArtifactID: "other-artifact"}, want: server.ErrNotFound},
		{name: "wrong declaration", id: owner.ID, part: session.Content{Kind: session.MediaPDF, MIMEType: "text/plain", ArtifactID: meta.ID}, want: server.ErrInvalidArgument},
		{name: "inline bytes", id: owner.ID, part: session.Content{Kind: session.MediaPDF, MIMEType: "application/pdf", ArtifactID: meta.ID, Data: valid}, want: server.ErrInvalidArgument},
	} {
		if _, err := svc.StartRunContent(context.Background(), tc.id, "read", []session.Content{tc.part}); !errors.Is(err, tc.want) {
			t.Errorf("%s prompt error = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := llm.Calls(); got != 0 {
		t.Fatalf("rejected PDF input started %d model calls", got)
	}
	if len(objects.data) != 1 {
		t.Fatalf("rejected PDF input left %d objects, want only the valid upload", len(objects.data))
	}
	count := 0
	for record, err := range metadata.PDFRecords(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if record.Record.ID != meta.ID || record.Record.Size != meta.Size {
			t.Fatalf("stored PDF record = %+v, want only valid metadata", record)
		}
	}
	if count != 1 {
		t.Fatalf("stored PDF records = %d, want 1", count)
	}
	t.Run("foreign principal HTTP upload", func(t *testing.T) {
		lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
		svc, owner := newOwnedPDFUploadService(t, lifecycle)
		handler := server.NewHTTPHandler(svc)
		request := func(ctx context.Context) *httptest.ResponseRecorder {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/s-pdf/pdfs?name=report.pdf", bytes.NewReader(valid)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/pdf")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec
		}
		foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser})
		denied := request(foreign)
		if denied.Code != http.StatusNotFound || bytes.Contains(denied.Body.Bytes(), valid) || lifecycle.calls != 0 || len(lifecycle.bytes) != 0 {
			t.Fatalf("foreign HTTP upload status=%d, stage calls=%d, staged bytes=%d", denied.Code, lifecycle.calls, len(lifecycle.bytes))
		}
		allowed := request(owner)
		if allowed.Code != http.StatusCreated || lifecycle.calls != 1 || !bytes.Equal(lifecycle.bytes, valid) {
			t.Fatalf("owner HTTP upload status=%d, stage calls=%d, staged=%q", allowed.Code, lifecycle.calls, lifecycle.bytes)
		}
	})
	t.Run("foreign principal gRPC upload", func(t *testing.T) {
		lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
		svc, _ := newOwnedPDFUploadService(t, lifecycle)
		auth := server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: map[string]session.Principal{
			"alice-token": {Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser},
			"bob-token":   {Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser},
		}}})
		defer auth.Close()
		client, closeClient := dialGRPCSecure(t, svc, auth)
		defer closeClient()
		upload := func(token string) (*mecatlv1.UploadPdfResponse, error) {
			t.Helper()
			stream, err := client.UploadPdf(bearerCtx(t.Context(), token))
			if err != nil {
				return nil, err
			}
			_ = stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "application/pdf"}}})
			_ = stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: valid}})
			return stream.CloseAndRecv()
		}
		denied, err := upload("bob-token")
		if status.Code(err) != codes.NotFound || denied != nil || lifecycle.calls != 0 || len(lifecycle.bytes) != 0 {
			t.Fatalf("foreign gRPC upload = %+v, %v; stage calls=%d, staged bytes=%d", denied, err, lifecycle.calls, len(lifecycle.bytes))
		}
		allowed, err := upload("alice-token")
		if err != nil || allowed.GetArtifactId() != "artifact" || lifecycle.calls != 1 || !bytes.Equal(lifecycle.bytes, valid) {
			t.Fatalf("owner gRPC upload = %+v, %v; stage calls=%d, staged=%q", allowed, err, lifecycle.calls, lifecycle.bytes)
		}
	})
}

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
		DefaultCapabilities: port.ProviderCapabilities{PDF: true},
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

func newOwnedPDFUploadService(t *testing.T, lifecycle server.PDFArtifactLifecycle) (*server.Service, context.Context) {
	t.Helper()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  memstore.New(), PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "s-pdf" }, PDFArtifacts: lifecycle,
		DefaultCapabilities: port.ProviderCapabilities{PDF: true}, OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(owner, session.ModeDefault, session.Limits{}); err != nil {
		svc.Close()
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, owner
}

func TestPDFUploadRejectsTextOnlySelectedSessionBeforeStorage(t *testing.T) {
	lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
	llm := mockllm.New(mockllm.TextTurn("unexpected"))
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}),
		Store:  memstore.New(), PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "s-pdf" }, PDFArtifacts: lifecycle,
		DefaultCapabilities: port.ProviderCapabilities{PDF: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPdf(context.Background(), sess.ID, "report.pdf", strings.NewReader("%PDF-1.7\n%%EOF")); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("text-only upload = %v, want invalid argument", err)
	}
	if lifecycle.calls != 0 || llm.Calls() != 0 {
		t.Fatalf("text-only upload reached storage (%d) or model (%d)", lifecycle.calls, llm.Calls())
	}
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

func TestPDFUploadGRPCRejectsMalformedFrameSequenceAndBounds(t *testing.T) {
	metadataFrame := func() *mecatlv1.UploadPdfRequest {
		return &mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "application/pdf"}}}
	}
	chunkFrame := func(data []byte) *mecatlv1.UploadPdfRequest {
		return &mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: data}}
	}
	for _, tc := range []struct {
		name       string
		frames     []*mecatlv1.UploadPdfRequest
		overflow   bool
		wantCode   codes.Code
		wantStages int
	}{
		{name: "chunk before metadata", frames: []*mecatlv1.UploadPdfRequest{chunkFrame([]byte("%PDF-1.7\n%%EOF")), metadataFrame(), chunkFrame([]byte("%PDF-1.7\n%%EOF"))}, wantCode: codes.InvalidArgument},
		{name: "repeated metadata", frames: []*mecatlv1.UploadPdfRequest{metadataFrame(), chunkFrame([]byte("%PDF-1.7\n%%EOF")), metadataFrame()}, wantCode: codes.InvalidArgument, wantStages: 1},
		{name: "empty chunk", frames: []*mecatlv1.UploadPdfRequest{metadataFrame(), chunkFrame([]byte("%PDF-1.7\n%%EOF")), chunkFrame(nil)}, wantCode: codes.InvalidArgument, wantStages: 1},
		{name: "oversized chunk", frames: []*mecatlv1.UploadPdfRequest{metadataFrame(), chunkFrame(bytes.Repeat([]byte("x"), 256<<10+1))}, wantCode: codes.InvalidArgument, wantStages: 1},
		{name: "total overflow", frames: []*mecatlv1.UploadPdfRequest{metadataFrame()}, overflow: true, wantCode: codes.ResourceExhausted, wantStages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &pdfUploadLifecycle{pdfPromptLifecycle: &pdfPromptLifecycle{}}
			client, closeClient := dialGRPC(t, newPDFUploadService(t, lifecycle))
			defer closeClient()
			stream, err := client.UploadPdf(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range tc.frames {
				_ = stream.Send(frame)
			}
			if tc.overflow {
				chunk := chunkFrame(bytes.Repeat([]byte("x"), 256<<10))
				for range 80 {
					_ = stream.Send(chunk)
				}
				_ = stream.Send(chunkFrame([]byte("x")))
			}
			response, err := stream.CloseAndRecv()
			if status.Code(err) != tc.wantCode || response != nil || lifecycle.calls != tc.wantStages || len(lifecycle.bytes) != 0 {
				t.Fatalf("malformed upload = %+v, %v; stage calls=%d, staged bytes=%d, want code=%v calls=%d", response, err, lifecycle.calls, len(lifecycle.bytes), tc.wantCode, tc.wantStages)
			}
		})
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

type pdfUploadProbe struct {
	*pdfPromptLifecycle
	first    chan struct{}
	release  chan struct{}
	finished chan error
}

func newPDFUploadProbe() *pdfUploadProbe {
	return &pdfUploadProbe{pdfPromptLifecycle: &pdfPromptLifecycle{}, first: make(chan struct{}), release: make(chan struct{}), finished: make(chan error, 1)}
}

func (p *pdfUploadProbe) Stage(ctx context.Context, _ session.SessionID, name string, source io.Reader) (server.PDFArtifact, error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(source, head); err != nil {
		p.finished <- err
		return server.PDFArtifact{}, err
	}
	close(p.first)
	select {
	case <-p.release:
	case <-ctx.Done():
		p.finished <- ctx.Err()
		return server.PDFArtifact{}, ctx.Err()
	}
	tail, err := io.ReadAll(source)
	p.finished <- err
	if err != nil {
		return server.PDFArtifact{}, err
	}
	return server.PDFArtifact{ID: "artifact", Name: name, Size: int64(len(head) + len(tail)), SHA256: strings.Repeat("a", 64)}, nil
}

// TestSDKPDFArtifacts_Scenario1_StreamTransports guards first-byte delivery to
// storage, completion backpressure, and cancellation in both server transports.
func TestSDKPDFArtifacts_Scenario1_StreamTransports(t *testing.T) {
	wait := func(t *testing.T, ch <-chan struct{}, where string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not reach storage before request EOF", where)
		}
	}
	t.Run("gRPC backpressure", func(t *testing.T) {
		probe := newPDFUploadProbe()
		client, closeClient := dialGRPC(t, newPDFUploadService(t, probe))
		defer closeClient()
		stream, err := client.UploadPdf(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "application/pdf"}}}); err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: []byte("%PDF-")}}); err != nil {
			t.Fatal(err)
		}
		wait(t, probe.first, "gRPC upload")
		if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: []byte("1.7\n%%EOF")}}); err != nil {
			t.Fatal(err)
		}
		type response struct {
			meta *mecatlv1.UploadPdfResponse
			err  error
		}
		completed := make(chan response, 1)
		go func() {
			meta, err := stream.CloseAndRecv()
			completed <- response{meta, err}
		}()
		select {
		case got := <-completed:
			t.Fatalf("gRPC upload completed before storage consumed tail: %+v", got)
		case <-time.After(30 * time.Millisecond):
		}
		close(probe.release)
		select {
		case got := <-completed:
			if got.err != nil || got.meta.GetArtifactId() != "artifact" {
				t.Fatalf("gRPC completion = %+v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("gRPC upload did not finish after storage resumed")
		}
	})
	t.Run("HTTP backpressure", func(t *testing.T) {
		probe := newPDFUploadProbe()
		handler := server.NewHTTPHandler(newPDFUploadService(t, probe))
		reader, writer := io.Pipe()
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/s-pdf/pdfs?name=report.pdf", reader)
		req.Header.Set("Content-Type", "application/pdf")
		rec := httptest.NewRecorder()
		completed := make(chan struct{})
		go func() { handler.ServeHTTP(rec, req); close(completed) }()
		if _, err := writer.Write([]byte("%PDF-")); err != nil {
			t.Fatal(err)
		}
		wait(t, probe.first, "HTTP upload")
		select {
		case <-completed:
			t.Fatal("HTTP upload completed before storage consumed tail")
		case <-time.After(30 * time.Millisecond):
		}
		close(probe.release)
		if _, err := writer.Write([]byte("1.7\n%%EOF")); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
		wait(t, completed, "HTTP completion")
		if rec.Code != http.StatusCreated {
			t.Fatalf("HTTP completion status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("gRPC cancellation", func(t *testing.T) {
		probe := newPDFUploadProbe()
		client, closeClient := dialGRPC(t, newPDFUploadService(t, probe))
		defer closeClient()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := client.UploadPdf(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Metadata{Metadata: &mecatlv1.UploadPdfMetadata{SessionId: "s-pdf", Name: "report.pdf", MimeType: "application/pdf"}}}); err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.UploadPdfRequest{Payload: &mecatlv1.UploadPdfRequest_Chunk{Chunk: []byte("%PDF-")}}); err != nil {
			t.Fatal(err)
		}
		wait(t, probe.first, "cancelled gRPC upload")
		cancel()
		select {
		case err := <-probe.finished:
			if err == nil {
				t.Fatal("cancelled gRPC storage stage succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled gRPC upload left storage stage running")
		}
	})
	t.Run("HTTP cancellation", func(t *testing.T) {
		probe := newPDFUploadProbe()
		handler := server.NewHTTPHandler(newPDFUploadService(t, probe))
		reader, writer := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/s-pdf/pdfs?name=report.pdf", reader).WithContext(ctx)
		req.Header.Set("Content-Type", "application/pdf")
		rec := httptest.NewRecorder()
		completed := make(chan struct{})
		go func() { handler.ServeHTTP(rec, req); close(completed) }()
		if _, err := writer.Write([]byte("%PDF-")); err != nil {
			t.Fatal(err)
		}
		wait(t, probe.first, "cancelled HTTP upload")
		cancel()
		_ = writer.CloseWithError(context.Canceled)
		select {
		case err := <-probe.finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled HTTP storage stage = %v, want context cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled HTTP upload left storage stage running")
		}
		wait(t, completed, "cancelled HTTP handler completion")
		if rec.Code == http.StatusCreated {
			t.Fatal("cancelled HTTP upload was published")
		}
	})
}
