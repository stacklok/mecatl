package executionclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

type renewalServer struct {
	executionv1.UnimplementedExecutionProviderServiceServer
	started chan string
	release chan struct{}
	serials chan string
	count   atomic.Int32
}

func (s *renewalServer) run(ctx context.Context, kind string) error {
	p, _ := peer.FromContext(ctx)
	leaf := p.AuthInfo.(credentials.TLSInfo).State.PeerCertificates[0]
	s.serials <- leaf.SerialNumber.String()
	if s.count.Add(1) <= 2 {
		s.started <- kind
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *renewalServer) Files(ctx context.Context, _ *executionv1.FileRequest) (*executionv1.FileResponse, error) {
	if err := s.run(ctx, "file"); err != nil {
		return nil, err
	}
	return &executionv1.FileResponse{}, nil
}
func (s *renewalServer) StartCommand(ctx context.Context, _ *executionv1.CommandStartRequest) (*executionv1.CommandStartResponse, error) {
	if err := s.run(ctx, "command"); err != nil {
		return nil, err
	}
	return &executionv1.CommandStartResponse{}, nil
}
func writeRenewalPair(t *testing.T, f TLSFiles, leaf tls.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{f.Cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}), f.Key: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTLSFilesExplicitVerifiedServerName(t *testing.T) {
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	leaf, _, key := certificate(t, ca, caKey, "client", true)
	dir := t.TempDir()
	files := TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key"), ServerName: "mecatl-execution.execution-qualification.svc.cluster.local"}
	if err := os.WriteFile(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	writeRenewalPair(t, files, leaf, key)
	cfg, err := LoadTLSConfig(files)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != files.ServerName {
		t.Fatalf("verified dial name: got=%q want=%q", cfg.ServerName, files.ServerName)
	}
}

func TestReloadTLSDrainsVerifiedSameIdentityRPCs(t *testing.T) {
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	serverCert, _, _ := certificate(t, ca, caKey, "localhost", false)
	first, _, firstKey := certificate(t, ca, caKey, "client", true)
	next, _, nextKey := certificate(t, ca, caKey, "client", true)
	dir := t.TempDir()
	files := TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key")}
	if err := os.WriteFile(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	writeRenewalPair(t, files, first, firstKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	svc := &renewalServer{started: make(chan string, 2), release: make(chan struct{}), serials: make(chan string, 4)}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(executioncontroller.TLSConfig(serverCert, roots))))
	executionv1.RegisterExecutionProviderServiceServer(grpcServer, svc)
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWithTLSFiles(net.JoinHostPort("localhost", port), files)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, e := client.rpc.Files(ctx, &executionv1.FileRequest{}); results <- e }()
	go func() { _, e := client.rpc.StartCommand(ctx, &executionv1.CommandStartRequest{}); results <- e }()
	for range 2 {
		select {
		case <-svc.started:
		case err := <-results:
			t.Fatalf("RPC failed before reaching provider: %v", err)
		case <-ctx.Done():
			t.Fatal("in-flight calls did not reach provider")
		}
	}
	old := client.conn.Load()
	writeRenewalPair(t, files, next, nextKey)
	if err := client.ReloadTLS(); err != nil {
		t.Fatal(err)
	}
	if client.conn.Load() == old {
		t.Fatal("renewal did not replace transport")
	}
	// The new call must authenticate with the renewed leaf while old RPCs remain blocked.
	if _, err := client.rpc.Files(ctx, &executionv1.FileRequest{}); err != nil {
		t.Fatalf("new call: %v", err)
	}
	serial1, serial2, serial3 := <-svc.serials, <-svc.serials, <-svc.serials
	if serial1 != first.Leaf.SerialNumber.String() || serial2 != serial1 || serial3 != next.Leaf.SerialNumber.String() {
		t.Fatalf("wrong authenticated leaf sequence: %s %s %s", serial1, serial2, serial3)
	}
	close(svc.release)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("in-flight result lost: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("in-flight result not delivered")
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.retired) != 0 || len(client.active) != 0 || old.GetState() != connectivity.Shutdown {
		t.Fatal("drained RPCs retained their connection, timer, or references")
	}
}

func TestReloadTLSRetiredChannelExpiresAndReleasesReferences(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, ca, caKey := certificate(t, nil, nil, "ca", false)
		first, _, firstKey := certificate(t, ca, caKey, "client", true)
		dir := t.TempDir()
		files := TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key")}
		if err := os.WriteFile(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		writeRenewalPair(t, files, first, firstKey)
		client, err := NewWithTLSFiles("localhost:443", files)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		old, releaseFirst, err := client.acquire()
		if err != nil {
			t.Fatal(err)
		}
		_, releaseLast, err := client.acquire()
		if err != nil {
			t.Fatal(err)
		}
		// Advance only the synctest clock; issued DER remains untouched.
		time.Sleep(time.Second)
		next, _, nextKey := certificate(t, ca, caKey, "client", true)
		writeRenewalPair(t, files, next, nextKey)
		if err := client.ReloadTLS(); err != nil {
			t.Fatal(err)
		}
		current := client.conn.Load()
		client.mu.Lock()
		if client.retired[old] == nil || client.active[old] != 2 || current == old {
			t.Error("active connection was not retired with both references")
		}
		client.mu.Unlock()
		releaseFirst()
		client.mu.Lock()
		if client.retired[old] == nil || client.active[old] != 1 || old.GetState() == connectivity.Shutdown {
			t.Error("first release closed a connection still in use")
		}
		client.mu.Unlock()
		time.Sleep(time.Until(first.Leaf.NotAfter))
		synctest.Wait()
		client.mu.Lock()
		if len(client.retired) != 0 || client.active[old] != 1 || old.GetState() != connectivity.Shutdown {
			t.Error("original leaf expiry did not close and remove the retired channel")
		}
		if client.conn.Load() != current || current.GetState() == connectivity.Shutdown {
			t.Error("retired channel expiry closed the renewed channel")
		}
		client.mu.Unlock()
		releaseLast()
		client.mu.Lock()
		defer client.mu.Unlock()
		if len(client.active) != 0 || len(client.retired) != 0 {
			t.Fatal("expired channel leaked references or timers after final release")
		}
	})
}

func TestReloadTLSWithdrawsTrustDuringActiveRPC(t *testing.T) {
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	serverCert, _, _ := certificate(t, ca, caKey, "localhost", false)
	clientCert, _, clientKey := certificate(t, ca, caKey, "client", true)
	dir := t.TempDir()
	files := TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key")}
	if err := os.WriteFile(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	writeRenewalPair(t, files, clientCert, clientKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	svc := &renewalServer{started: make(chan string, 2), release: make(chan struct{}), serials: make(chan string, 4)}
	defer close(svc.release)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(executioncontroller.TLSConfig(serverCert, roots))))
	executionv1.RegisterExecutionProviderServiceServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWithTLSFiles(net.JoinHostPort("localhost", port), files)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, e := client.rpc.StartCommand(ctx, &executionv1.CommandStartRequest{}); result <- e }()
	select {
	case <-svc.started:
	case <-ctx.Done():
		t.Fatal("mutation did not start")
	}
	_, other, _ := certificate(t, nil, nil, "other CA", false)
	if err := os.WriteFile(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.ReloadTLS(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("withdrawn CA allowed in-flight mutation to complete")
		}
	case <-ctx.Done():
		t.Fatal("withdrawn transport did not terminate")
	}
}
