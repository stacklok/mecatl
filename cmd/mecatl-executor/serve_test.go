package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/internal/executionexecutor"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
)

func TestFileServiceOverOSFSConformance(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		root := t.TempDir()
		srv, err := newFileServer(root, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return fileconformance.Target{
			Conn: fileconformance.NewClient(t, srv),
			Root: root,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(root, p))
				},
				Write: func(p string, data []byte) error {
					return os.WriteFile(filepath.Join(root, p), data, 0o600)
				},
			},
		}
	})
}

func TestFileServiceOverOSFSBoundsFileSize(t *testing.T) {
	root := t.TempDir()
	srv, err := newFileServer(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	ws := fileconformance.NewWorkspace(fileconformance.NewClient(t, srv), root)
	oversized := bytes.Repeat([]byte("x"), 5)
	if err := os.WriteFile(filepath.Join(root, "big"), oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(context.Background(), "big"); err == nil {
		t.Fatal("read of an oversized file succeeded")
	}
	if _, err := ws.CreateFile(context.Background(), "new", []byte("12345")); err == nil {
		t.Fatal("oversized create succeeded")
	}
}

func TestServeRejectsInvalidMaxFileBytes(t *testing.T) {
	for _, value := range []string{"0", "5242881"} {
		args := []string{serveMode, "-listen", "127.0.0.1:0", "-files", "-max-file-bytes", value}
		err := runServeMode(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "max-file-bytes") {
			t.Fatalf("-max-file-bytes %s: err = %v", value, err)
		}
	}
}

func TestServeRejectsNoEnabledService(t *testing.T) {
	err := runServeMode(context.Background(), []string{serveMode, "-listen", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "enable at least one") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeWithoutCommandsLeavesCommandServiceUnimplemented(t *testing.T) {
	ctx := context.Background()
	cfg := serveConfig{files: true, maxFileBytes: 1 << 20}
	srv, err := newExecutorServer(ctx, t.TempDir(), cfg, insecure.NewCredentials())
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_, err = executorv1.NewFileServiceClient(conn).StatFile(
		ctx,
		&executorv1.StatFileRequest{Path: "missing"},
	)
	if status.Code(err) == codes.Unimplemented {
		t.Fatalf("StatFile: %v", err)
	}

	stream, err := executorv1.NewCommandServiceClient(conn).RunCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: "true"},
	}
	_ = stream.Send(&executorv1.RunCommandRequest{Action: start})
	if _, err := stream.Recv(); status.Code(err) != codes.Unimplemented {
		t.Fatalf("RunCommand: err = %v, want Unimplemented", err)
	}
}

type servePKI struct {
	certDir, trustDir string
	provider          tls.Certificate
	shell             tls.Certificate
}

func newServePKI(t *testing.T) servePKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	issue := func(uri string, usage x509.ExtKeyUsage) tls.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		u, _ := url.Parse(uri)
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(2),
			URIs:         []*url.URL{u},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	server := issue("urn:mecatl:execution-environment:env", x509.ExtKeyUsageServerAuth)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(server.PrivateKey)
	pki := servePKI{
		certDir:  t.TempDir(),
		trustDir: t.TempDir(),
		provider: issue(testProviderURI, x509.ExtKeyUsageClientAuth),
		shell:    issue("spiffe://mecatl.test/shell", x509.ExtKeyUsageClientAuth),
	}
	files := map[string][]byte{
		filepath.Join(pki.certDir, "tls.crt"):     pemBlock("CERTIFICATE", server.Certificate[0]),
		filepath.Join(pki.certDir, "tls.key"):     pemBlock("PRIVATE KEY", keyDER),
		filepath.Join(pki.trustDir, "ca.crt"):     pemBlock("CERTIFICATE", caDER),
		filepath.Join(pki.trustDir, "client-uri"): []byte(testProviderURI),
	}
	for name, data := range files {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return pki
}

const testProviderURI = "spiffe://mecatl.test/provider/executor-client"

func pemBlock(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func TestServeAdmitsOnlyTheProviderOverLocalhost(t *testing.T) {
	pki := newServePKI(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := executionexecutor.ServerTLSConfig(pki.certDir, pki.trustDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newExecutorServer(
		t.Context(),
		root,
		serveConfig{files: true, maxFileBytes: 1 << 20},
		credentials.NewTLS(tlsConfig),
	)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	stat := func(certs ...tls.Certificate) error {
		creds := credentials.NewTLS(&tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, //nolint:gosec // The server pin is the dialer's concern.
			Certificates:       certs,
		})
		conn, err := grpc.NewClient(
			"passthrough:///"+lis.Addr().String(),
			grpc.WithTransportCredentials(creds),
		)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		client := executorv1.NewFileServiceClient(conn)
		_, err = client.StatFile(t.Context(), &executorv1.StatFileRequest{Path: "f"})
		return err
	}
	if err := stat(pki.provider); err != nil {
		t.Fatalf("provider rejected: %v", err)
	}
	if err := stat(); status.Code(err) != codes.Unavailable {
		t.Fatalf("caller without a certificate: %v", err)
	}
	if err := stat(pki.shell); status.Code(err) != codes.Unavailable {
		t.Fatalf("caller with the wrong URI: %v", err)
	}
}

func TestServeRequiresTLSMounts(t *testing.T) {
	pki := newServePKI(t)
	missing := filepath.Join(t.TempDir(), "absent")
	for _, flags := range [][]string{
		{"-tls-dir", missing, "-provider-trust-dir", pki.trustDir},
		{"-tls-dir", pki.certDir, "-provider-trust-dir", missing},
	} {
		args := append([]string{serveMode, "-listen", "127.0.0.1:0", "-files"}, flags...)
		err := runServeMode(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("%v: err = %v", flags, err)
		}
	}
}
