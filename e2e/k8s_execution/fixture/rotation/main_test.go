//go:build kind_execution_e2e

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestForwardRestoreTrustBundleSupportsFixtureClient(t *testing.T) {
	now := time.Now().UTC()
	initial := filepath.Join(t.TempDir(), "initial")
	out := filepath.Join(t.TempDir(), "rotation")
	if err := os.MkdirAll(initial, 0o700); err != nil {
		t.Fatal(err)
	}

	oldCAKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	oldCA := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	oldCADER, err := x509.CreateCertificate(rand.Reader, oldCA, oldCA, &oldCAKey.PublicKey, oldCAKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initial, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldCADER}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, grant, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grantDER, err := x509.MarshalPKCS8PrivateKey(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initial, "grant-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: grantDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	generate(initial, out)

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fixture client"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, oldCA, &clientKey.PublicKey, oldCAKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCert := tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey}
	serverCert, err := tls.LoadX509KeyPair(filepath.Join(out, "provider-new.crt"), filepath.Join(out, "provider-new.key"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(out, "client-roots.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("generated forward trust bundle contained no certificates")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})))
	healthpb.RegisterHealthServer(server, health.NewServer())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "mecatl-execution.execution-qualification.svc.cluster.local", RootCAs: roots, Certificates: []tls.Certificate{clientCert}})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal("forward-restored TLS gRPC handshake failed")
	}
}
