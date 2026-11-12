package executioncontroller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const testProviderClientURI = "spiffe://mecatl.test/provider/executor-client"

type platformPKI struct {
	pool      *x509.CertPool
	clientDir string
}

func newPlatformPKI(t *testing.T, clientURI string) platformPKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "platform"},
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
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	uri, _ := url.Parse(clientURI)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		URIs:         []*url.URL{uri},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	dir := t.TempDir()
	for name, block := range map[string]*pem.Block{
		"tls.crt": {Type: "CERTIFICATE", Bytes: leafDER},
		"tls.key": {Type: "PRIVATE KEY", Bytes: keyDER},
	} {
		err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return platformPKI{pool: pool, clientDir: dir}
}

type executorServer struct {
	port        int
	fingerprint string
}

// startExecutorServer stands in for #2699: it serves certPEM and admits only the provider URI.
// Any chain certificates are sent after the executor certificate.
func startExecutorServer(
	t *testing.T, env string, pki platformPKI, chain ...[]byte,
) executorServer {
	t.Helper()
	certPEM, keyPEM, fingerprint, err := generateExecutorCertificate(env, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cert.Certificate = append(cert.Certificate, chain...)
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pki.pool,
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			id, err := canonicalClientIdentity(chains[0][0])
			if err != nil || id != testProviderClientURI {
				return errors.New("not the provider")
			}
			return nil
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if conn.(*tls.Conn).Handshake() == nil {
					_, _ = conn.Write([]byte("ok"))
				}
			}()
		}
	}()
	return executorServer{port: ln.Addr().(*net.TCPAddr).Port, fingerprint: fingerprint}
}

func executorPodAt(name, uid, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(uid)},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func dialAndRead(t *testing.T, d *ExecutorDialer, pin ExecutorPin, port int) error {
	t.Helper()
	conn, err := d.Dial(t.Context(), pin, port)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	reply, err := io.ReadAll(conn)
	if err == nil && string(reply) != "ok" {
		err = errors.New("executor refused the provider")
	}
	return err
}

func TestExecutorDialAcceptsOnlyThePinnedPodAndCertificate(t *testing.T) {
	pki := newPlatformPKI(t, testProviderClientURI)
	envA := startExecutorServer(t, "exec-a", pki)
	envB := startExecutorServer(t, "exec-b", pki)
	rekeyedA := startExecutorServer(t, "exec-a", pki)
	kube := kubefake.NewSimpleClientset(executorPodAt("executor-a", "pod-a2", "127.0.0.1"))
	dialer := NewExecutorDialer(kube, "ns", pki.clientDir, testProviderClientURI)
	if err := dialer.CheckClientCertificate(); err != nil {
		t.Fatal(err)
	}
	current := ExecutorPin{PodName: "executor-a", PodUID: "pod-a2", Fingerprint: envA.fingerprint}
	if err := dialAndRead(t, dialer, current, envA.port); err != nil {
		t.Fatalf("pinned executor rejected: %v", err)
	}
	err := dialAndRead(t, dialer, current, rekeyedA.port)
	if !errors.Is(err, errExecutorCertificateMismatch) {
		t.Fatalf("server with a different key accepted: %v", err)
	}
	err = dialAndRead(t, dialer, current, envB.port)
	if !errors.Is(err, errExecutorCertificateMismatch) {
		t.Fatalf("reused Pod IP serving another Pod accepted: %v", err)
	}
	stale := ExecutorPin{PodName: "executor-a", PodUID: "pod-a1", Fingerprint: rekeyedA.fingerprint}
	if err = dialAndRead(t, dialer, stale, rekeyedA.port); !errors.Is(err, errExecutorPodReplaced) {
		t.Fatalf("old Pod UID with a valid certificate accepted: %v", err)
	}
}

func TestExecutorDialRefusesChainWhoseLeafMatchesThePin(t *testing.T) {
	pki := newPlatformPKI(t, testProviderClientURI)
	otherPEM, _, _, err := generateExecutorCertificate("exec-other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pem.Decode(otherPEM)
	server := startExecutorServer(t, "exec-a", pki, other.Bytes)
	kube := kubefake.NewSimpleClientset(executorPodAt("executor-a", "pod-a", "127.0.0.1"))
	dialer := NewExecutorDialer(kube, "ns", pki.clientDir, testProviderClientURI)
	pin := ExecutorPin{PodName: "executor-a", PodUID: "pod-a", Fingerprint: server.fingerprint}
	err = dialAndRead(t, dialer, pin, server.port)
	if !errors.Is(err, errExecutorCertificateMismatch) {
		t.Fatalf("two-certificate chain with the pinned leaf accepted: %v", err)
	}
}

func TestExecutorDialRefusesEmptyPin(t *testing.T) {
	pki := newPlatformPKI(t, testProviderClientURI)
	server := startExecutorServer(t, "exec-a", pki)
	kube := kubefake.NewSimpleClientset(executorPodAt("executor-a", "pod-a", "127.0.0.1"))
	dialer := NewExecutorDialer(kube, "ns", pki.clientDir, testProviderClientURI)
	pin := ExecutorPin{PodName: "executor-a", PodUID: "pod-a"}
	err := dialAndRead(t, dialer, pin, server.port)
	if !errors.Is(err, errExecutorCertificateMismatch) {
		t.Fatalf("empty pin accepted: %v", err)
	}
}

func TestExecutorDialRefusesUnusableProviderIdentity(t *testing.T) {
	pki := newPlatformPKI(t, "spiffe://mecatl.test/client/mecak8s")
	server := startExecutorServer(t, "exec-a", newPlatformPKI(t, testProviderClientURI))
	kube := kubefake.NewSimpleClientset(executorPodAt("executor-a", "pod-a", "127.0.0.1"))
	dialer := NewExecutorDialer(kube, "ns", pki.clientDir, testProviderClientURI)
	if err := dialer.CheckClientCertificate(); err == nil {
		t.Fatal("client certificate naming another URI accepted")
	}
	pin := ExecutorPin{PodName: "executor-a", PodUID: "pod-a", Fingerprint: server.fingerprint}
	if err := dialAndRead(t, dialer, pin, server.port); err == nil {
		t.Fatal("executor admitted a provider without its client certificate")
	}
}

func TestExecutorDialRequiresLiveAddressablePod(t *testing.T) {
	terminating := executorPodAt("executor-t", "pod-t", "127.0.0.1")
	terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	kube := kubefake.NewSimpleClientset(terminating, executorPodAt("executor-p", "pod-p", ""))
	dialer := NewExecutorDialer(kube, "ns", t.TempDir(), testProviderClientURI)
	for _, pin := range []ExecutorPin{
		{PodName: "executor-t", PodUID: "pod-t"},
		{PodName: "executor-p", PodUID: "pod-p"},
	} {
		if _, err := dialer.Resolve(t.Context(), pin); !errors.Is(err, errExecutorPodReplaced) {
			t.Fatalf("%s resolved: %v", pin.PodName, err)
		}
	}
	missing := ExecutorPin{PodName: "missing", PodUID: "x"}
	if _, err := dialer.Resolve(t.Context(), missing); err == nil {
		t.Fatal("missing Pod resolved")
	}
}

func TestExecutorPinFromEnvironmentRequiresCurrentPodPin(t *testing.T) {
	fingerprint := certificateFingerprint([]byte("certificate"))
	build := func(podUID, pinnedUID, fp string) *unstructured.Unstructured {
		env := testEnvironment()
		pod := map[string]any{"name": "executor-test", "uid": podUID}
		_ = unstructured.SetNestedMap(env.Object, pod, "status", "pod")
		pin := serverCertificatePin{SecretName: "s", Fingerprint: fp, PodUID: pinnedUID}
		writeServerCertificatePin(env, pin)
		return env
	}
	pin, err := ExecutorPinFromEnvironment(build("pod-2", "pod-2", fingerprint))
	want := ExecutorPin{PodName: "executor-test", PodUID: "pod-2", Fingerprint: fingerprint}
	if err != nil || pin != want {
		t.Fatalf("pin=%+v err=%v", pin, err)
	}
	for name, env := range map[string]*unstructured.Unstructured{
		"previous Pod":         build("pod-2", "pod-1", fingerprint),
		"not yet bound to Pod": build("pod-2", "", fingerprint),
		"malformed":            build("pod-2", "pod-2", "ABC"),
		"no Pod":               build("", "", fingerprint),
	} {
		if _, err := ExecutorPinFromEnvironment(env); !errors.Is(err, errExecutorUnpinned) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
