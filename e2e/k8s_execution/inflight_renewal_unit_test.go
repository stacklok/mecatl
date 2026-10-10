//go:build kind_execution_e2e

package k8s_execution_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
)

func TestInFlightHandshakeNegotiatesGRPCAndDrainsBeforeClose(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const serverName = "mecatl-execution.execution-qualification.svc.cluster.local"
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{serverName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	dir := t.TempDir()
	files := executionclient.TLSFiles{CA: filepath.Join(dir, "ca.crt"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key"), ServerName: serverName}
	for path, data := range map[string][]byte{files.CA: certPEM, files.Cert: certPEM, files.Key: keyPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, probe := range map[string]func(string) (string, error){
		"projected": func(endpoint string) (string, error) { return inflightHandshake(endpoint, files) },
		"issued":    func(endpoint string) (string, error) { return inflightIssuedHandshake(endpoint, certPEM, pair) },
	} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverResult := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverResult <- err
					return
				}
				defer conn.Close()
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					serverResult <- err
					return
				}
				creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})
				secure, auth, err := creds.ServerHandshake(conn)
				if err != nil {
					serverResult <- err
					return
				}
				defer secure.Close()
				if auth.(credentials.TLSInfo).State.NegotiatedProtocol != "h2" {
					serverResult <- fmt.Errorf("probe did not negotiate gRPC ALPN")
					return
				}
				// Force post-handshake records to be consumed, rather than relying
				// on the timing of a small HTTP/2 settings frame in a real tunnel.
				if _, err := secure.Write(make([]byte, 1<<20)); err != nil {
					serverResult <- err
					return
				}
				_, err = io.Copy(io.Discard, secure)
				serverResult <- err
			}()
			serial, probeErr := probe(listener.Addr().String())
			listener.Close()
			serverErr := <-serverResult
			if probeErr != nil || serverErr != nil || serial != "1" {
				t.Fatalf("serial=%q probe=%v server=%v", serial, probeErr, serverErr)
			}
		})
	}
}
