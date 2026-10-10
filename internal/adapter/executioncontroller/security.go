package executioncontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	securityManifestVersion = 1
	maxSecurityClients      = 256
	securityStateDataKey    = "state.json"
)

type securityClientManifest struct {
	URI                string   `json:"uri"`
	MayAttestOwner     bool     `json:"mayAttestOwner"`
	Administrator      bool     `json:"administrator"`
	AdministratorFor   []string `json:"administratorFor,omitempty"`
	ExecutionTemplates []string `json:"executionTemplates,omitempty"`
}

type securityTLSManifest struct {
	CertificateFile string `json:"certificateFile"`
	PrivateKeyFile  string `json:"privateKeyFile"`
	ClientCAFile    string `json:"clientCAFile"`
}

type securityManifest struct {
	Version    int                      `json:"version"`
	Generation uint64                   `json:"generation"`
	TLS        securityTLSManifest      `json:"tls"`
	Clients    []securityClientManifest `json:"clients"`
}

type securitySnapshot struct {
	generation uint64
	digest     string
	tlsConfig  *tls.Config
	clientCAs  *x509.CertPool
	clients    map[string]ClientPolicy
	validUntil time.Time
}

type securityLedger struct {
	Generation uint64 `json:"generation"`
	Digest     string `json:"digest"`
}

type securityState struct {
	snapshot *securitySnapshot
}

// SecurityManager reloads one immutable, versioned security snapshot from
// projected files and binds its generation to a durable ConfigMap high-water mark.
type SecurityManager struct {
	manifestPath, keyDirectory, namespace, configMap string
	kube                                             kubernetes.Interface
	now                                              func() time.Time
	readFile                                         func(*os.Root, string) ([]byte, error)
	state                                            atomic.Pointer[securityState]
}

// NewSecurityManager constructs a fail-closed security material reloader.
func NewSecurityManager(manifestPath, keyDirectory, namespace, configMap string, kube kubernetes.Interface) *SecurityManager {
	return &SecurityManager{manifestPath: manifestPath, keyDirectory: keyDirectory, namespace: namespace, configMap: configMap, kube: kube, now: func() time.Time { return time.Now().UTC() }, readFile: readRootFile}
}

// Ready reports whether the current snapshot is authoritative and unexpired.
func (m *SecurityManager) Ready() bool {
	state := m.state.Load()
	return state != nil && snapshotValidAt(state.snapshot, m.now())
}

// CheckReady verifies that the loaded snapshot still matches durable authority.
func (m *SecurityManager) CheckReady(ctx context.Context) bool {
	_, err := m.authoritative(ctx)
	return err == nil
}

// Reload atomically validates and publishes a complete security snapshot.
func (m *SecurityManager) Reload(ctx context.Context) error {
	observed := m.state.Load()
	candidate, err := m.load()
	if err != nil {
		m.invalidateObservedUnlessReplaced(ctx, observed)
		return err
	}
	if err := m.publishGeneration(ctx, candidate); err != nil {
		m.invalidateObservedUnlessReplaced(ctx, observed)
		return err
	}
	if err := m.verifyAuthority(ctx, candidate); err != nil {
		m.invalidateObservedUnlessReplaced(ctx, observed)
		return err
	}
	for {
		current := m.state.Load()
		if current != nil && current.snapshot.generation > candidate.generation {
			return errors.New("security manifest generation rollback rejected")
		}
		if m.state.CompareAndSwap(current, &securityState{snapshot: candidate}) {
			return nil
		}
	}
}

// Run periodically reloads security material until ctx is cancelled.
func (m *SecurityManager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.Reload(ctx)
		}
	}
}

// TLSConfig returns a dynamic TLS configuration backed by the current snapshot.
func (m *SecurityManager) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		state := m.state.Load()
		if state == nil || !snapshotValidAt(state.snapshot, m.now()) {
			return nil, errors.New("security material is not ready")
		}
		return state.snapshot.tlsConfig.Clone(), nil
	}}
}

func (m *SecurityManager) authorize(ctx context.Context, chain []*x509.Certificate) (string, ClientPolicy, error) {
	if len(chain) == 0 || chain[0] == nil {
		return "", ClientPolicy{}, errors.New("client certificate is unavailable")
	}
	now := m.now()
	s, err := m.authoritativeAt(ctx, now)
	if err != nil {
		return "", ClientPolicy{}, err
	}
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		if cert != nil {
			intermediates.AddCert(cert)
		}
	}
	opts := x509.VerifyOptions{Roots: s.clientCAs, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leaf := chain[0]
	if _, err := leaf.Verify(opts); err != nil {
		return "", ClientPolicy{}, err
	}
	id, err := canonicalClientIdentity(leaf)
	if err != nil {
		return "", ClientPolicy{}, err
	}
	policy, ok := s.clients[id]
	if !ok {
		return "", ClientPolicy{}, errors.New("client is not authorized")
	}
	return id, policy, nil
}

func (m *SecurityManager) authoritative(ctx context.Context) (*securitySnapshot, error) {
	return m.authoritativeAt(ctx, m.now())
}

var errAuthorityUnavailable = errors.New("security authority is unavailable")

func authorityUnavailable(err error) error {
	if err == nil {
		return errAuthorityUnavailable
	}
	return fmt.Errorf("%w: %v", errAuthorityUnavailable, err)
}

func (m *SecurityManager) authoritativeAt(ctx context.Context, now time.Time) (*securitySnapshot, error) {
	state := m.state.Load()
	if state == nil || !snapshotValidAt(state.snapshot, now) || m.kube == nil {
		return nil, authorityUnavailable(nil)
	}
	if err := m.verifyAuthority(ctx, state.snapshot); err != nil {
		m.state.CompareAndSwap(state, nil)
		return nil, authorityUnavailable(err)
	}
	return state.snapshot, nil
}

func (m *SecurityManager) verifyAuthority(ctx context.Context, snapshot *securitySnapshot) error {
	if snapshot == nil || m.kube == nil {
		return errors.New("security material is not ready")
	}
	cm, err := m.kube.CoreV1().ConfigMaps(m.namespace).Get(ctx, m.configMap, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var ledger securityLedger
	if err := executionenv.DecodeStrict([]byte(cm.Data[securityStateDataKey]), &ledger); err != nil {
		return err
	}
	if ledger.Generation != snapshot.generation || ledger.Digest != snapshot.digest {
		return errors.New("loaded security generation is not authoritative")
	}
	return nil
}

func (m *SecurityManager) invalidateObservedUnlessReplaced(ctx context.Context, observed *securityState) {
	if m.state.CompareAndSwap(observed, nil) {
		return
	}
	current := m.state.Load()
	if current != nil && m.verifyAuthority(ctx, current.snapshot) != nil {
		m.state.CompareAndSwap(current, nil)
	}
}

func snapshotValidAt(s *securitySnapshot, now time.Time) bool {
	return s != nil && now.Before(s.validUntil)
}

func (m *SecurityManager) load() (*securitySnapshot, error) {
	root, err := os.OpenRoot(m.keyDirectory)
	if err != nil {
		return nil, fmt.Errorf("open security directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	// A projected volume switches all files via ..data, not the opened mount
	// root. Check both policy and TLS projections around the entire candidate,
	// including failed reads when Kubernetes removes the previous generation.
	for range 3 {
		before, err := m.projectedGenerations(root)
		if err != nil {
			return nil, err
		}
		candidate, loadErr := m.loadCandidate(root)
		after, err := m.projectedGenerations(root)
		if err != nil {
			return nil, err
		}
		if before == after {
			return candidate, loadErr
		}
	}
	return nil, errors.New("security projection changed during load")
}

func (m *SecurityManager) projectedGenerations(root *os.Root) ([2]string, error) {
	var generations [2]string
	var err error
	generations[0], err = root.Readlink("..data")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return generations, err
	}
	generations[1], err = os.Readlink(filepath.Join(filepath.Dir(m.manifestPath), "..data"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return generations, err
	}
	return generations, nil
}

func (m *SecurityManager) loadCandidate(root *os.Root) (*securitySnapshot, error) {
	mf, err := m.loadManifest()
	if err != nil {
		return nil, err
	}
	tlsConfig, clientCAs, tlsValidUntil, err := m.loadTLS(root, mf.TLS)
	if err != nil {
		return nil, err
	}
	clients, err := clientPolicies(mf.Clients)
	if err != nil {
		return nil, err
	}
	digest, err := authorityDigest(mf)
	if err != nil {
		return nil, err
	}
	return &securitySnapshot{generation: mf.Generation, digest: digest, tlsConfig: tlsConfig, clientCAs: clientCAs, clients: clients, validUntil: tlsValidUntil}, nil
}

func (m *SecurityManager) loadManifest() (securityManifest, error) {
	manifestBytes, err := os.ReadFile(m.manifestPath)
	if err != nil {
		return securityManifest{}, fmt.Errorf("read security manifest: %w", err)
	}
	var mf securityManifest
	if err := executionenv.DecodeStrict(manifestBytes, &mf); err != nil {
		return securityManifest{}, fmt.Errorf("decode security manifest: %w", err)
	}
	if mf.Version != securityManifestVersion || mf.Generation == 0 || mf.Generation > math.MaxInt64 || len(mf.Clients) == 0 || len(mf.Clients) > maxSecurityClients {
		return securityManifest{}, errors.New("security manifest identity or bounds are invalid")
	}
	return mf, nil
}

func (m *SecurityManager) loadTLS(root *os.Root, manifest securityTLSManifest) (*tls.Config, *x509.CertPool, time.Time, error) {
	certPEM, err := m.readFile(root, manifest.CertificateFile)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	keyPEM, err := m.readFile(root, manifest.PrivateKeyFile)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("load TLS identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	now := m.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || !hasUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return nil, nil, time.Time{}, errors.New("server certificate is not currently valid for server authentication")
	}
	serverCert.Leaf = leaf
	caPEM, err := m.readFile(root, manifest.ClientCAFile)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	pool := x509.NewCertPool()
	if _, err := appendCAs(pool, caPEM); err != nil {
		return nil, nil, time.Time{}, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAnyClientCert}
	return cfg, pool, leaf.NotAfter, nil
}

func appendCAs(pool *x509.CertPool, pemBytes []byte) ([]string, error) {
	var fingerprints []string
	for len(bytes.TrimSpace(pemBytes)) != 0 {
		block, rest := pem.Decode(pemBytes)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("client CA bundle is invalid")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return nil, errors.New("client CA bundle contains an invalid CA certificate")
		}
		pool.AddCert(cert)
		sum := sha256.Sum256(cert.Raw)
		fingerprints = append(fingerprints, hex.EncodeToString(sum[:]))
		pemBytes = rest
	}
	if len(fingerprints) == 0 {
		return nil, errors.New("client CA bundle has no certificates")
	}
	slices.Sort(fingerprints)
	return fingerprints, nil
}

//nolint:gocyclo // One manifest entry must be fully validated before authorization is published.
func clientPolicies(entries []securityClientManifest) (map[string]ClientPolicy, error) {
	clients := make(map[string]ClientPolicy, len(entries))
	for _, entry := range entries {
		uri, err := url.Parse(entry.URI)
		if err != nil {
			return nil, errors.New("client URI is invalid")
		}
		canonical, err := canonicalClientIdentity(&x509.Certificate{URIs: []*url.URL{uri}})
		if err != nil || canonical != entry.URI {
			return nil, errors.New("client URI is not canonical")
		}
		if _, exists := clients[entry.URI]; exists {
			return nil, errors.New("client URI is duplicated")
		}
		if len(entry.AdministratorFor) > maxSecurityClients || len(entry.AdministratorFor) > 0 && !entry.Administrator {
			return nil, errors.New("administrator scope requires administrator authority and at most 256 creators")
		}
		scope := slices.Clone(entry.AdministratorFor)
		slices.Sort(scope)
		for i, creator := range scope {
			uri, err := url.Parse(creator)
			if err != nil || strings.ContainsAny(creator, "*?") || strings.ContainsAny(uri.Path, "*?") {
				return nil, errors.New("administrator creator URI is invalid")
			}
			canonical, err := canonicalClientIdentity(&x509.Certificate{URIs: []*url.URL{uri}})
			if err != nil || canonical != creator || i > 0 && scope[i-1] == creator {
				return nil, errors.New("administrator creator URI is not canonical or is duplicated")
			}
		}
		allowed := slices.Clone(entry.ExecutionTemplates)
		if len(allowed) > 64 || len(allowed) > 0 && !entry.MayAttestOwner {
			return nil, errors.New("execution templates require owner attestation and at most 64 IDs")
		}
		slices.Sort(allowed)
		for i, id := range allowed {
			if len(validation.IsDNS1123Label(id)) != 0 || i > 0 && id == allowed[i-1] {
				return nil, errors.New("invalid or duplicate execution template ID")
			}
		}
		clients[entry.URI] = ClientPolicy{MayAttestOwner: entry.MayAttestOwner, Administrator: entry.Administrator, AdministratorFor: scope, ExecutionTemplates: allowed}
	}
	return clients, nil
}

func authorityDigest(mf securityManifest) (string, error) {
	clients := slices.Clone(mf.Clients)
	for i := range clients {
		clients[i].AdministratorFor = slices.Clone(clients[i].AdministratorFor)
		slices.Sort(clients[i].AdministratorFor)
		clients[i].ExecutionTemplates = slices.Clone(clients[i].ExecutionTemplates)
		slices.Sort(clients[i].ExecutionTemplates)
	}
	slices.SortFunc(clients, func(a, b securityClientManifest) int { return strings.Compare(a.URI, b.URI) })
	raw, err := json.Marshal(struct {
		Version    int
		Generation uint64
		Clients    []securityClientManifest
	}{mf.Version, mf.Generation, clients})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (m *SecurityManager) publishGeneration(ctx context.Context, s *securitySnapshot) error {
	if m.kube == nil || m.configMap == "" {
		return errors.New("security authority ConfigMap is required")
	}
	cms := m.kube.CoreV1().ConfigMaps(m.namespace)
	for range 5 {
		cm, exists, ledger, err := m.readSecurityLedger(ctx, cms)
		if err != nil {
			return err
		}
		changed, err := advanceSecurityLedger(&ledger, s)
		if err != nil || !changed {
			return err
		}
		raw, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[securityStateDataKey] = string(raw)
		if !exists {
			_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		} else {
			_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		}
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) && !apierrors.IsConflict(err) {
			return err
		}
	}
	return errors.New("security authority state changed concurrently")
}

func (m *SecurityManager) readSecurityLedger(ctx context.Context, cms corev1client.ConfigMapInterface) (*corev1.ConfigMap, bool, securityLedger, error) {
	cm, err := cms.Get(ctx, m.configMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: m.configMap, Namespace: m.namespace}, Data: map[string]string{}}
		return cm, false, securityLedger{}, nil
	}
	if err != nil {
		return nil, false, securityLedger{}, err
	}
	ledger := securityLedger{}
	if raw := cm.Data[securityStateDataKey]; raw != "" {
		if err := executionenv.DecodeStrict([]byte(raw), &ledger); err != nil || ledger.Generation == 0 || ledger.Digest == "" {
			return nil, false, securityLedger{}, errors.New("security authority state is invalid")
		}
	}
	return cm, true, ledger, nil
}

func advanceSecurityLedger(ledger *securityLedger, s *securitySnapshot) (bool, error) {
	if s.generation < ledger.Generation {
		return false, errors.New("security manifest generation rollback rejected")
	}
	if s.generation == ledger.Generation && ledger.Generation != 0 {
		if ledger.Digest != s.digest {
			return false, errors.New("security generation authority drift rejected")
		}
		return false, nil
	}
	ledger.Generation = s.generation
	ledger.Digest = s.digest
	return true, nil
}

func readRootFile(root *os.Root, name string) ([]byte, error) {
	if !validBaseName(name) {
		return nil, errors.New("security filename must be a basename")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	b, readErr := io.ReadAll(io.LimitReader(f, 1<<20))
	return b, errors.Join(readErr, f.Close())
}
func validBaseName(v string) bool { return v != "" && filepath.Base(v) == v && v != "." && v != ".." }
func hasUsage(usages []x509.ExtKeyUsage, wanted x509.ExtKeyUsage) bool {
	for _, u := range usages {
		if u == wanted || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}
