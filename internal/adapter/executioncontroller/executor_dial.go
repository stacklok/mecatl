package executioncontroller

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

var (
	errExecutorUnpinned = errors.New(
		"executor has no pinned server certificate for its current Pod")
	errExecutorPodReplaced = errors.New(
		"executor Pod does not match the pinned Pod UID")
	errExecutorCertificateMismatch = errors.New(
		"executor server certificate does not match the pinned fingerprint")
)

// ExecutorPin is the server identity the provider accepts from one executor Pod.
type ExecutorPin struct {
	PodName     string
	PodUID      string
	Fingerprint string
}

// ExecutorPinFromEnvironment returns the pin recorded for env's current executor Pod.
func ExecutorPinFromEnvironment(env *unstructured.Unstructured) (ExecutorPin, error) {
	pin := readServerCertificatePin(env)
	podName := textNested(env.Object, statusField, "pod", "name")
	podUID := textNested(env.Object, statusField, "pod", "uid")
	if podName == "" || podUID == "" || pin.PodUID != podUID || !validFingerprint(pin.Fingerprint) {
		return ExecutorPin{}, errExecutorUnpinned
	}
	return ExecutorPin{PodName: podName, PodUID: podUID, Fingerprint: pin.Fingerprint}, nil
}

func validFingerprint(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ExecutorDialer connects to executor servers by pinned Pod UID and certificate
// fingerprint, presenting the provider's projected platform client certificate.
type ExecutorDialer struct {
	kube            kubernetes.Interface
	namespace       string
	clientDirectory string
	clientURI       string
	now             func() time.Time
}

// NewExecutorDialer constructs a dialer reading tls.crt and tls.key from clientDirectory.
func NewExecutorDialer(
	kube kubernetes.Interface, namespace, clientDirectory, clientURI string,
) *ExecutorDialer {
	return &ExecutorDialer{
		kube:            kube,
		namespace:       namespace,
		clientDirectory: clientDirectory,
		clientURI:       clientURI,
		now:             func() time.Time { return time.Now().UTC() },
	}
}

// CheckClientCertificate reports whether the projected client certificate is
// currently valid for client authentication and names the configured URI.
func (d *ExecutorDialer) CheckClientCertificate() error {
	_, err := d.clientCertificate()
	return err
}

// Resolve returns the IP of the executor Pod only while it is the pinned incarnation.
func (d *ExecutorDialer) Resolve(ctx context.Context, pin ExecutorPin) (string, error) {
	pod, err := d.kube.CoreV1().Pods(d.namespace).Get(ctx, pin.PodName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("resolve executor Pod: %w", err)
	}
	if string(pod.UID) != pin.PodUID || pod.DeletionTimestamp != nil || pod.Status.PodIP == "" {
		return "", errExecutorPodReplaced
	}
	return pod.Status.PodIP, nil
}

// TLSConfig accepts only the pinned server certificate; CA validation is replaced by the pin.
func (d *ExecutorDialer) TLSConfig(pin ExecutorPin) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection enforces the pin.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 || !validFingerprint(pin.Fingerprint) {
				return errExecutorCertificateMismatch
			}
			got := certificateFingerprint(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare([]byte(got), []byte(pin.Fingerprint)) != 1 {
				return errExecutorCertificateMismatch
			}
			return nil
		},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return d.clientCertificate()
		},
	}
}

// Dial resolves the pinned Pod and completes a mutual TLS handshake with it.
func (d *ExecutorDialer) Dial(ctx context.Context, pin ExecutorPin, port int) (*tls.Conn, error) {
	ip, err := d.Resolve(ctx, pin)
	if err != nil {
		return nil, err
	}
	dialer := &tls.Dialer{Config: d.TLSConfig(pin)}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("executor connection is not TLS")
	}
	return tlsConn, nil
}

func (d *ExecutorDialer) clientCertificate() (*tls.Certificate, error) {
	root, err := os.OpenRoot(d.clientDirectory)
	if err != nil {
		return nil, fmt.Errorf("open executor client certificate directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	for range 3 {
		before, _ := root.Readlink("..data")
		cert, loadErr := d.loadClientCertificate(root)
		after, _ := root.Readlink("..data")
		if before == after {
			return cert, loadErr
		}
	}
	return nil, errors.New("executor client certificate projection changed during load")
}

func (d *ExecutorDialer) loadClientCertificate(root *os.Root) (*tls.Certificate, error) {
	certPEM, err := readRootFile(root, "tls.crt")
	if err != nil {
		return nil, err
	}
	keyPEM, err := readRootFile(root, "tls.key")
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load executor client certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	now := d.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) ||
		!hasUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return nil, errors.New(
			"executor client certificate is not currently valid for client authentication")
	}
	if id, err := canonicalClientIdentity(leaf); err != nil || id != d.clientURI {
		return nil, errors.New(
			"executor client certificate does not name the configured provider URI")
	}
	cert.Leaf = leaf
	return &cert, nil
}
