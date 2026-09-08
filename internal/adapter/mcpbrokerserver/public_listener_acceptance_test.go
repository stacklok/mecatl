package mcpbrokerserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSingletonBrokerRemediation_Scenario3_PublicListenerBoundsRejectBeforeCallbackSideEffects(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	catalogue, err := mcpbroker.Compile(mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "fixture", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}, []mcpbroker.ToolDefinition{{Backend: "fixture", Name: "read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var executes, callbacks atomic.Int32
	runtime, err := mcpbroker.New(catalogue, func(ctx context.Context, _ mcpbroker.SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		executes.Add(1)
		select {
		case <-time.After(40 * time.Millisecond):
			return session.NewToolResult(call.ID, "ok"), nil
		case <-ctx.Done():
			return session.ToolResult{}, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := mcpbrokergrpc.DefaultConfig()
	transport.ExecuteDeadline = 250 * time.Millisecond
	server, err := New(t.Context(), Config{
		OIDC: productionOIDC(issuer, time.Minute), Transport: transport,
		Factory: func(context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
			return runtime, mcpbroker.HandlerBundle{Callback: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				callbacks.Add(1)
				w.WriteHeader(http.StatusNoContent)
			})}, "/callback", runtime.Close, nil
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
	bounds.ReadHeaderTimeout = 100 * time.Millisecond
	bounds.ReadTimeout = 6 * time.Second
	bounds.WriteTimeout = 6 * time.Second
	bounds.IdleTimeout = 100 * time.Millisecond
	bounds.CallbackTimeout = 80 * time.Millisecond
	bounds.MaxHeaderBytes = 1024
	bounds.MaxCallbackBytes = 32
	public, err := NewPublicListener(listener, server, &tls.Config{Certificates: []tls.Certificate{fCertificate(t, productionOIDC(issuer, time.Minute))}, MinVersion: tls.VersionTLS13}, bounds)
	if err != nil {
		t.Fatal(err)
	}
	public.Serve()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = server.Close(ctx)
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
	factory := mcpbrokergrpc.NewRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{Target: listener.Addr().String(), CAFile: caPath, ServerName: "example.com", TokenFile: tokenPath, Transport: transport})
	remote, closeRemote, err := factory(t.Context())
	if err != nil {
		t.Fatalf("production remote factory: %v", err)
	}
	defer func() { _ = closeRemote() }()
	attachment, _, err := remote.AttachSession(t.Context(), "bounded-execute")
	if err != nil {
		t.Fatal(err)
	}
	result, err := attachment.Tools()[0].Execute(t.Context(), session.NewToolCall("call", "read", json.RawMessage(`{}`)), tool.Environment{})
	if err != nil || result.Content != "ok" || executes.Load() != 1 {
		t.Fatalf("bounded gRPC Execute = %#v, %v; dispatches=%d", result, err, executes.Load())
	}
}

func TestPublicHandlerRejectsBodyCompletingAfterCallbackDeadline(t *testing.T) {
	var callbacks atomic.Int32
	handler := PublicHandler(http.NotFoundHandler(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
