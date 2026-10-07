package mcpbrokerserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSingletonBrokerRemediation_Scenario3_PublicListenerBoundsRejectBeforeCallbackSideEffects(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	var executes, callbacks atomic.Int32
	runtime := &boundedSessionService{executes: &executes}
	transport := mcpbrokergrpc.DefaultConfig()
	transport.ExecuteDeadline = 250 * time.Millisecond
	server, err := newBrokerHost(t.Context(), hostConfig{
		WorkloadJWT: productionOIDC(issuer, time.Minute),
		Runtime: func(context.Context) (brokerRuntime, error) {
			return brokerRuntime{SessionAPI: runtime, Handlers: mcpbroker.HandlerBundle{Callback: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				callbacks.Add(1)
				w.WriteHeader(http.StatusNoContent)
			})}, CallbackPath: "/callback"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bounds := DefaultPublicListenerConfig()
	bounds.ExecuteDeadline = transport.ExecuteDeadline
	bounds.ReadHeaderTimeout = 100 * time.Millisecond
	bounds.ReadTimeout = 6 * time.Second
	bounds.WriteTimeout = 6 * time.Second
	bounds.IdleTimeout = 100 * time.Millisecond
	bounds.CallbackTimeout = 80 * time.Millisecond
	bounds.MaxHeaderBytes = 1024
	bounds.MaxCallbackBytes = 32
	public, err := newPublicListener(listener, server, &tls.Config{Certificates: []tls.Certificate{fCertificate(t, productionOIDC(issuer, time.Minute))}, MinVersion: tls.VersionTLS13}, bounds)
	if err != nil {
		t.Fatal(err)
	}
	public.Serve()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = server.close(ctx)
	})

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(issuer.caPEM())
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true}, Timeout: time.Second}
	baseURL := "https://" + listener.Addr().String()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/callback", strings.NewReader(strings.Repeat("x", 33)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("bounded HTTP/2 callback = HTTP/%d %d", response.ProtoMajor, response.StatusCode)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, baseURL+"/callback", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported callback status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	assertSlowOrIncompleteRejected(t, listener.Addr().String(), roots, false)
	assertSlowOrIncompleteRejected(t, listener.Addr().String(), roots, true)
	if callbacks.Load() != 0 {
		t.Fatalf("rejected callbacks reached mounted handler %d times", callbacks.Load())
	}

	tokenPath := filepath.Join(t.TempDir(), "token")
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(tokenPath, []byte(issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, issuer.caPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := mcpbrokergrpc.NewSessionRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{Target: listener.Addr().String(), CAFile: caPath, ServerName: "example.com", TokenFile: tokenPath, Transport: transport})
	remote, closeRemote, err := factory(t.Context())
	if err != nil {
		t.Fatalf("production remote factory: %v", err)
	}
	defer func() { _ = closeRemote() }()
	snapshot, err := remote.OpenSession(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := remote.InvokeTool(t.Context(), snapshot.Ref, snapshot.Catalogue.Ref(), contract.Call{ID: "call", Name: "read", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
	if err != nil || out.Result == nil || out.Result.Content != "ok" || executes.Load() != 1 {
		t.Fatalf("bounded gRPC invocation = %#v, %v; dispatches=%d", out, err, executes.Load())
	}
}

type boundedSessionService struct {
	countingService
	executes *atomic.Int32
}

func (s *boundedSessionService) InvokeTool(ctx context.Context, _ contract.SessionRef, _ contract.CatalogueRef, call contract.Call, _ contract.BrokerAttempt) (contract.InvocationOutcome, error) {
	s.executes.Add(1)
	select {
	case <-time.After(40 * time.Millisecond):
		result := session.NewToolResult(call.ID, "ok")
		return contract.InvocationOutcome{Kind: contract.InvocationCompleted, Result: &result}, nil
	case <-ctx.Done():
		return contract.InvocationOutcome{}, ctx.Err()
	}
}

func TestPublicHandlerRejectsBodyCompletingAfterCallbackDeadline(t *testing.T) {
	var callbacks atomic.Int32
	handler := publicHandler(http.NotFoundHandler(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbacks.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), PublicListenerConfig{CallbackTimeout: 20 * time.Millisecond, MaxCallbackBytes: 1024})

	body := &delayedCompleteBody{data: []byte("code=late"), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/callback", body)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.ContentLength = int64(len(body.data))
	recorder := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(recorder, request)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("deadline response took %v, want prompt rejection", elapsed)
	}
	if recorder.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestTimeout)
	}
	if !body.closed.Load() {
		t.Fatal("deadline did not close the in-flight callback body")
	}
	if callbacks.Load() != 0 {
		t.Fatalf("late body reached callback handler %d times", callbacks.Load())
	}
}

type delayedCompleteBody struct {
	data    []byte
	release chan struct{}
	once    sync.Once
	closed  atomic.Bool
	sent    atomic.Bool
}

func (b *delayedCompleteBody) Read(dst []byte) (int, error) {
	if b.sent.Swap(true) {
		return 0, io.EOF
	}
	<-b.release
	return copy(dst, b.data), nil
}

func (b *delayedCompleteBody) Close() error {
	b.once.Do(func() {
		b.closed.Store(true)
		close(b.release)
	})
	return nil
}

func assertSlowOrIncompleteRejected(t *testing.T, address string, roots *x509.CertPool, slow bool) {
	t.Helper()
	conn, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "POST /callback HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 10\r\nConnection: close\r\n\r\nabc")
	if slow {
		time.Sleep(150 * time.Millisecond)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = http.ReadResponse(bufio.NewReader(conn), nil)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	_, _ = io.Copy(io.Discard, conn)
}
