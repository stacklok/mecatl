package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newPDFDownloadFixture(t *testing.T, objects pdfartifact.ObjectStore, ownership bool) (*server.Service, *redisstore.Store) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  metadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		PDFArtifacts: pdfartifact.New(metadata, objects), DefaultCapabilities: port.ProviderCapabilities{PDF: true},
		OwnershipEnforced: ownership,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, metadata
}

func pdfDownloadBytes() []byte {
	return append(append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("private-pdf-content\n"), 40_000)...), []byte("%%EOF")...)
}

func TestSDKPDFArtifacts_Scenario3_StreamDownload(t *testing.T) {
	objects := &pdfMemoryObjects{data: make(map[string][]byte)}
	svc, _ := newPDFDownloadFixture(t, objects, false)
	created, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("pdf-download"))
	if err != nil {
		t.Fatal(err)
	}
	want := pdfDownloadBytes()
	meta, err := svc.UploadPdf(t.Context(), created.ID, "report.pdf", bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(want)
	if meta.Size != int64(len(want)) || meta.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("stored metadata = %+v", meta)
	}

	client, closeClient := dialGRPC(t, svc)
	defer closeClient()
	stream, err := client.DownloadPdf(t.Context(), &mecatlv1.DownloadPdfRequest{SessionId: string(created.ID), ArtifactId: meta.ID})
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for {
		frame, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if n := len(frame.GetChunk()); n == 0 || n > 256<<10 {
			t.Fatalf("gRPC chunk size = %d", n)
		}
		chunks = append(chunks, frame.GetChunk())
	}
	if len(chunks) < 2 || !bytes.Equal(bytes.Join(chunks, nil), want) {
		t.Fatalf("gRPC chunks = %d; downloaded bytes mismatch", len(chunks))
	}

	writer := &pdfChunkWriter{header: make(http.Header)}
	request := httptest.NewRequest(http.MethodGet, "/v1/sessions/pdf-download/pdfs/"+meta.ID, nil)
	server.NewHTTPHandler(svc).ServeHTTP(writer, request)
	if writer.status != http.StatusOK || writer.header.Get("Content-Type") != "application/pdf" || writer.header.Get("Cache-Control") != "private, no-store" || writer.header.Get("Content-Disposition") != `attachment; filename="report.pdf"` {
		t.Fatalf("HTTP status=%d headers=%v", writer.status, writer.header)
	}
	if writer.flushes < 2 || len(writer.chunks) < 2 || !bytes.Equal(bytes.Join(writer.chunks, nil), want) {
		t.Fatalf("HTTP chunks=%d flushes=%d; downloaded bytes mismatch", len(writer.chunks), writer.flushes)
	}
	for _, chunk := range writer.chunks {
		if len(chunk) > 256<<10 {
			t.Fatalf("HTTP chunk size = %d", len(chunk))
		}
	}
}

func TestPDFDownloadHTTPRejectsCorruptObjectCompletion(t *testing.T) {
	objects := &pdfMemoryObjects{data: make(map[string][]byte)}
	svc, _ := newPDFDownloadFixture(t, objects, false)
	created, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("pdf-corrupt"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := svc.UploadPdf(t.Context(), created.ID, "report.pdf", bytes.NewReader(pdfDownloadBytes()))
	if err != nil {
		t.Fatal(err)
	}
	for key, data := range objects.data {
		corrupt := bytes.Clone(data)
		corrupt[10] ^= 1
		objects.data[key] = corrupt
	}
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	response, err := httpServer.Client().Get(httpServer.URL + "/v1/sessions/pdf-corrupt/pdfs/" + meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("corrupt object download completion = %v, want truncated HTTP body", err)
	}
}

type pdfChunkWriter struct {
	header  http.Header
	status  int
	chunks  [][]byte
	flushes int
}

func (w *pdfChunkWriter) Header() http.Header    { return w.header }
func (w *pdfChunkWriter) WriteHeader(status int) { w.status = status }
func (w *pdfChunkWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.chunks = append(w.chunks, bytes.Clone(data))
	return len(data), nil
}
func (w *pdfChunkWriter) Flush() { w.flushes++ }

func TestSDKPDFArtifacts_Scenario3_OwnershipAndCancellation(t *testing.T) {
	objects := &pdfCancellationObjects{pdfMemoryObjects: &pdfMemoryObjects{data: make(map[string][]byte)}, closed: make(chan struct{})}
	svc, metadata := newPDFDownloadFixture(t, objects, true)
	alice := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser})
	owned, err := svc.CreateSessionWithProfile(alice, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("pdf-owned"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateSessionWithProfile(alice, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("pdf-other"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := svc.UploadPdf(alice, owned.ID, "report.pdf", bytes.NewReader(pdfDownloadBytes()))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		ctx      context.Context
		id       session.SessionID
		artifact string
	}{
		{"different principal", bob, owned.ID, meta.ID},
		{"different session", alice, other.ID, meta.ID},
		{"unknown artifact", alice, owned.ID, strings.Repeat("a", 48)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, reader, err := svc.DownloadPdf(test.ctx, test.id, test.artifact)
			if reader != nil {
				_ = reader.Close()
				t.Fatal("unauthorized download opened a reader")
			}
			if !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("download error = %v", err)
			}
		})
	}
	if objects.openCount() != 0 {
		t.Fatalf("rejected downloads opened %d object readers", objects.openCount())
	}
	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/v1/sessions/pdf-owned/pdfs/"+meta.ID, nil).WithContext(bob)
	server.NewHTTPHandler(svc).ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusNotFound || bytes.Contains(unauthorized.Body.Bytes(), []byte("%PDF-")) || objects.openCount() != 0 {
		t.Fatalf("foreign HTTP download status=%d opened=%d", unauthorized.Code, objects.openCount())
	}

	ctx, cancel := context.WithCancel(alice)
	writer := &pdfCancelWriter{pdfChunkWriter: pdfChunkWriter{header: make(http.Header)}, first: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(http.MethodGet, "/v1/sessions/pdf-owned/pdfs/"+meta.ID, nil).WithContext(ctx)
		server.NewHTTPHandler(svc).ServeHTTP(writer, request)
	}()
	select {
	case <-writer.first:
	case <-time.After(3 * time.Second):
		t.Fatal("authorized stream did not deliver its first chunk")
	}
	cancel()
	select {
	case <-objects.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled stream did not close object reader")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled handler did not stop")
	}
	if len(writer.chunks) != 1 {
		t.Fatalf("cancelled stream wrote %d chunks", len(writer.chunks))
	}

	meta2, reader, err := svc.DownloadPdf(alice, owned.ID, meta.ID)
	if err != nil || meta2.ID != meta.ID {
		t.Fatalf("authorized open = %+v, %v", meta2, err)
	}
	if err := metadata.Delete(alice, owned.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(remaining, pdfDownloadBytes()) {
		t.Fatalf("authorized stream after deletion: %v, bytes=%d", err, len(remaining))
	}
	_, reader, err = svc.DownloadPdf(alice, owned.ID, meta.ID)
	if reader != nil {
		_ = reader.Close()
		t.Fatal("deleted session opened a reader")
	}
	if !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("deleted session download = %v", err)
	}
	t.Run("gRPC cancellation", testPDFGRPCDownloadCancellation)
}

func testPDFGRPCDownloadCancellation(t *testing.T) {
	objects := &pdfCancellationObjects{
		pdfMemoryObjects: &pdfMemoryObjects{data: make(map[string][]byte)},
		closed:           make(chan struct{}), blocked: make(chan struct{}),
	}
	svc, _ := newPDFDownloadFixture(t, objects, true)
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	owner := session.WithPrincipal(t.Context(), alice)
	created, err := svc.CreateSessionWithProfile(owner, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("pdf-grpc-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := svc.UploadPdf(owner, created.ID, "report.pdf", bytes.NewReader(pdfDownloadBytes()))
	if err != nil {
		t.Fatal(err)
	}
	auth := server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: map[string]session.Principal{"alice-token": *alice}}})
	defer auth.Close()
	var sent atomic.Int32
	finished := make(chan struct{})
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.ChainStreamInterceptor(auth.StreamInterceptor(), func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if info.FullMethod != mecatlv1.HarnessService_DownloadPdf_FullMethodName {
			return handler(srv, stream)
		}
		defer close(finished)
		return handler(srv, &pdfTrackedServerStream{ServerStream: stream, sent: &sent})
	}))
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///pdf-grpc-cancel", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		gs.Stop()
		_ = lis.Close()
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(); gs.Stop(); _ = lis.Close() }()
	ctx, cancel := context.WithCancel(bearerCtx(t.Context(), "alice-token"))
	defer cancel()
	stream, err := mecatlv1.NewHarnessServiceClient(conn).DownloadPdf(ctx, &mecatlv1.DownloadPdfRequest{SessionId: string(created.ID), ArtifactId: meta.ID})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || len(first.GetChunk()) == 0 {
		t.Fatalf("first gRPC download chunk = %+v, %v", first, err)
	}
	select {
	case <-objects.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("gRPC download did not reach the blocked object read")
	}
	cancel()
	select {
	case <-objects.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled gRPC download did not close its object reader")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled gRPC download handler did not finish")
	}
	next, err := stream.Recv()
	if next != nil || grpcstatus.Code(err) != codes.Canceled || sent.Load() != 1 || objects.openCount() != 1 {
		t.Fatalf("cancelled gRPC download next=%+v, err=%v, server chunks=%d, readers=%d", next, err, sent.Load(), objects.openCount())
	}
}

type pdfTrackedServerStream struct {
	grpc.ServerStream
	sent *atomic.Int32
}

func (s *pdfTrackedServerStream) SendMsg(msg any) error {
	err := s.ServerStream.SendMsg(msg)
	if err == nil {
		if _, ok := msg.(*mecatlv1.DownloadPdfResponse); ok {
			s.sent.Add(1)
		}
	}
	return err
}

type pdfCancellationObjects struct {
	*pdfMemoryObjects
	mu      sync.Mutex
	opens   int
	closed  chan struct{}
	blocked chan struct{}
}

func (o *pdfCancellationObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	reader, err := o.pdfMemoryObjects.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.opens++
	first := o.opens == 1
	o.mu.Unlock()
	if !first {
		return reader, nil
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil, err
	}
	return &pdfCancellationReader{data: data, closed: o.closed, blocked: o.blocked}, nil
}

func (o *pdfCancellationObjects) openCount() int { o.mu.Lock(); defer o.mu.Unlock(); return o.opens }

type pdfCancellationReader struct {
	data    []byte
	first   bool
	closed  chan struct{}
	blocked chan struct{}
	once    sync.Once
}

func (r *pdfCancellationReader) Read(dst []byte) (int, error) {
	if !r.first {
		r.first = true
		return copy(dst, r.data), nil
	}
	if r.blocked != nil {
		close(r.blocked)
		r.blocked = nil
	}
	<-r.closed
	return 0, io.EOF
}
func (r *pdfCancellationReader) Close() error { r.once.Do(func() { close(r.closed) }); return nil }

type pdfCancelWriter struct {
	pdfChunkWriter
	first chan struct{}
}

func (w *pdfCancelWriter) Write(data []byte) (int, error) {
	n, err := w.pdfChunkWriter.Write(data)
	if len(w.chunks) == 1 {
		close(w.first)
	}
	return n, err
}
