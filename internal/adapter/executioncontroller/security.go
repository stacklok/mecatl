package executioncontroller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	securityManifestVersion = 1
	maxSecurityKeys         = 64
	maxSecurityClients      = 256
	securityStateDataKey    = "state.json"
	keyStateRevoked         = "revoked"
)

type securityKeyManifest struct {
	ID           string    `json:"id"`
	Version      uint64    `json:"version"`
	File         string    `json:"file"`
	PublicSHA256 string    `json:"publicKeySHA256"`
	ActivateAt   time.Time `json:"activateAt"`
	VerifyUntil  time.Time `json:"verifyUntil"`
	State        string    `json:"state"`
}

type securityClientManifest struct {
	URI            string `json:"uri"`
	MayAttestOwner bool   `json:"mayAttestOwner"`
	Administrator  bool   `json:"administrator"`
}

type securityTLSManifest struct {
	CertificateFile string `json:"certificateFile"`
	PrivateKeyFile  string `json:"privateKeyFile"`
	ClientCAFile    string `json:"clientCAFile"`
}

type securityManifest struct {
	Version       int                      `json:"version"`
	Generation    uint64                   `json:"generation"`
	Issuer        string                   `json:"issuer"`
	Audience      string                   `json:"audience"`
	ActiveKeyID   string                   `json:"activeKeyID"`
	GrantTTL      time.Duration            `json:"-"`
	GrantTTLText  string                   `json:"grantTTL"`
	ClockSkewText string                   `json:"clockSkew"`
	Keys          []securityKeyManifest    `json:"keys"`
	TLS           securityTLSManifest      `json:"tls"`
	Clients       []securityClientManifest `json:"clients"`
}

type keyValidity struct {
	activateAt, verifyUntil time.Time
	state                   string
}

type securitySnapshot struct {
	generation   uint64
	digest       string
	signer       GrantSigner
	verifier     executionenv.GrantVerifier
	keyValidity  map[string]keyValidity
	activeWindow keyValidity
	tlsConfig    *tls.Config
	clientCAs    *x509.CertPool
	clients      map[string]ClientPolicy
	validUntil   time.Time
	fingerprints map[string]string
}

type securityLedger struct {
	Generation   uint64            `json:"generation"`
	Digest       string            `json:"digest"`
	Fingerprints map[string]string `json:"fingerprints"`
}

// SecurityManager reloads one immutable, versioned security snapshot from
// projected files and binds its generation to a durable ConfigMap high-water mark.
type SecurityManager struct {
	manifestPath, keyDirectory, namespace, configMap string
	kube                                             kubernetes.Interface
	now                                              func() time.Time
	current                                          atomic.Pointer[securitySnapshot]
	ready                                            atomic.Bool
}

// NewSecurityManager constructs a fail-closed security material reloader.
func NewSecurityManager(manifestPath, keyDirectory, namespace, configMap string, kube kubernetes.Interface) *SecurityManager {
	return &SecurityManager{manifestPath: manifestPath, keyDirectory: keyDirectory, namespace: namespace, configMap: configMap, kube: kube, now: func() time.Time { return time.Now().UTC() }}
}

// Ready reports whether the current snapshot is authoritative and unexpired.
func (m *SecurityManager) Ready() bool {
	now := m.now()
	s := m.current.Load()
	return m.ready.Load() && snapshotValidAt(s, now)
}

// CheckReady verifies that the loaded snapshot still matches durable authority.
func (m *SecurityManager) CheckReady(ctx context.Context) bool {
	_, err := m.authoritative(ctx)
	return err == nil
}

// Reload atomically validates and publishes a complete security snapshot.
func (m *SecurityManager) Reload(ctx context.Context) error {
	candidate, err := m.load()
	if err != nil {
		m.ready.Store(false)
		return err
	}
	if err := m.publishGeneration(ctx, candidate); err != nil {
		m.ready.Store(false)
		return err
	}
	m.current.Store(candidate)
	m.ready.Store(true)
	return nil
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
		s := m.current.Load()
		if !m.Ready() {
			return nil, errors.New("security material is not ready")
		}
		return s.tlsConfig.Clone(), nil
	}}
}

func (m *SecurityManager) authorize(ctx context.Context, leaf *x509.Certificate) (string, ClientPolicy, error) {
	now := m.now()
	s, err := m.authoritativeAt(ctx, now)
	if err != nil {
		return "", ClientPolicy{}, err
	}
	opts := x509.VerifyOptions{Roots: s.clientCAs, Intermediates: x509.NewCertPool(), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
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

func (m *SecurityManager) material(ctx context.Context) (*securitySnapshot, error) {
	return m.authoritative(ctx)
}

func (m *SecurityManager) authoritative(ctx context.Context) (*securitySnapshot, error) {
	return m.authoritativeAt(ctx, m.now())
}

func (m *SecurityManager) authoritativeAt(ctx context.Context, now time.Time) (*securitySnapshot, error) {
	s := m.current.Load()
	if !m.ready.Load() || !snapshotValidAt(s, now) || m.kube == nil {
		return nil, errors.New("security material is not ready")
	}
	cm, err := m.kube.CoreV1().ConfigMaps(m.namespace).Get(ctx, m.configMap, metav1.GetOptions{})
	if err != nil {
		m.ready.Store(false)
		return nil, err
	}
	var ledger securityLedger
	if err := executionenv.DecodeStrict([]byte(cm.Data[securityStateDataKey]), &ledger); err != nil || ledger.Generation != s.generation || ledger.Digest != s.digest || !maps.Equal(ledger.Fingerprints, s.fingerprints) {
		m.ready.Store(false)
		return nil, errors.New("loaded security generation is not authoritative")
	}
	return s, nil
}

func snapshotValidAt(s *securitySnapshot, now time.Time) bool {
	return s != nil && !now.Before(s.activeWindow.activateAt) && now.Before(s.activeWindow.verifyUntil) && now.Before(s.validUntil)
}

func (s *securitySnapshot) verifierAt(now time.Time) executionenv.GrantVerifier {
	v := s.verifier
	v.Keys = make(map[string]ed25519.PublicKey, len(s.verifier.Keys))
	for id, key := range s.verifier.Keys {
		window := s.keyValidity[id]
		if window.state != keyStateRevoked && !now.Before(window.activateAt) && now.Before(window.verifyUntil) {
			v.Keys[id] = key
		}
	}
	return v
}

func (m *SecurityManager) load() (*securitySnapshot, error) {
	mf, ttl, skew, err := m.loadManifest()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(m.keyDirectory)
	if err != nil {
		return nil, fmt.Errorf("open security directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	keys, active, fingerprints, windows, activeWindow, err := m.loadGrantKeys(root, mf)
	if err != nil {
		return nil, err
	}
	tlsConfig, clientCAs, tlsValidUntil, caFingerprints, serverFingerprint, err := m.loadTLS(root, mf.TLS)
	if err != nil {
		return nil, err
	}
	clients, err := clientPolicies(mf.Clients)
	if err != nil {
		return nil, err
	}
	digest, err := authorityDigest(mf, ttl, skew, fingerprints, caFingerprints, serverFingerprint)
	if err != nil {
		return nil, err
	}
	validUntil := tlsValidUntil
	now := m.now()
	for _, window := range windows {
		if window.state != keyStateRevoked && window.verifyUntil.Before(validUntil) {
			validUntil = window.verifyUntil
		}
		if window.state != keyStateRevoked && now.Before(window.activateAt) && window.activateAt.Before(validUntil) {
			validUntil = window.activateAt
		}
	}
	return &securitySnapshot{
		generation: mf.Generation, digest: digest,
		signer: GrantSigner{
			KeyID: mf.ActiveKeyID, PrivateKey: active, Issuer: mf.Issuer,
			Audience: mf.Audience, Lifetime: ttl,
		},
		verifier: executionenv.GrantVerifier{
			Keys: keys, Issuer: mf.Issuer, Audience: mf.Audience, MaxLifetime: ttl + skew,
		},
		keyValidity: windows, activeWindow: activeWindow,
		tlsConfig: tlsConfig, clientCAs: clientCAs, clients: clients,
		validUntil: validUntil, fingerprints: fingerprints,
	}, nil
}

func (m *SecurityManager) loadManifest() (securityManifest, time.Duration, time.Duration, error) {
	manifestBytes, err := os.ReadFile(m.manifestPath)
	if err != nil {
		return securityManifest{}, 0, 0, fmt.Errorf("read security manifest: %w", err)
	}
	var mf securityManifest
	if err := executionenv.DecodeStrict(manifestBytes, &mf); err != nil {
		return securityManifest{}, 0, 0, fmt.Errorf("decode security manifest: %w", err)
	}
	if mf.Version != securityManifestVersion || mf.Generation == 0 || mf.Generation > math.MaxInt64 || mf.Issuer == "" || mf.Audience == "" || mf.ActiveKeyID == "" || len(mf.Keys) == 0 || len(mf.Keys) > maxSecurityKeys || len(mf.Clients) == 0 || len(mf.Clients) > maxSecurityClients {
		return securityManifest{}, 0, 0, errors.New("security manifest identity or bounds are invalid")
	}
	ttl, err := time.ParseDuration(mf.GrantTTLText)
	if err != nil || ttl <= 0 || ttl > 5*time.Minute {
		return securityManifest{}, 0, 0, errors.New("grantTTL must be positive and at most 5m")
	}
	skew, err := time.ParseDuration(mf.ClockSkewText)
	if err != nil || skew < 0 || skew >= ttl {
		return securityManifest{}, 0, 0, errors.New("clockSkew must be non-negative and less than grantTTL")
	}
	return mf, ttl, skew, nil
}

func (m *SecurityManager) loadGrantKeys(root *os.Root, mf securityManifest) (map[string]ed25519.PublicKey, ed25519.PrivateKey, map[string]string, map[string]keyValidity, keyValidity, error) {
	keys := make(map[string]ed25519.PublicKey, len(mf.Keys))
	fingerprints := make(map[string]string, len(mf.Keys))
	windows := make(map[string]keyValidity, len(mf.Keys))
	seenVersion := map[uint64]bool{}
	seenID := map[string]bool{}
	var active ed25519.PrivateKey
	var activeWindow keyValidity
	now := m.now()
	for _, km := range mf.Keys {
		if !validKeyManifest(km, seenID, seenVersion) {
			return nil, nil, nil, nil, keyValidity{}, errors.New("security key entry is invalid or duplicated")
		}
		seenID[km.ID] = true
		seenVersion[km.Version] = true
		key, err := readEd25519(root, km.File)
		if err != nil {
			return nil, nil, nil, nil, keyValidity{}, fmt.Errorf("load security key %q: %w", km.ID, err)
		}
		pub := key.Public().(ed25519.PublicKey)
		sum := sha256.Sum256(pub)
		fp := hex.EncodeToString(sum[:])
		if !strings.EqualFold(fp, km.PublicSHA256) {
			return nil, nil, nil, nil, keyValidity{}, fmt.Errorf("security key %q fingerprint mismatch", km.ID)
		}
		fingerprints[km.ID+":"+strconv.FormatUint(km.Version, 10)] = fp
		window := keyValidity{activateAt: km.ActivateAt, verifyUntil: km.VerifyUntil, state: km.State}
		windows[km.ID] = window
		if km.State != keyStateRevoked {
			keys[km.ID] = pub
		}
		if km.ID == mf.ActiveKeyID {
			if km.State != "active" || now.Before(km.ActivateAt) || !now.Before(km.VerifyUntil) {
				return nil, nil, nil, nil, keyValidity{}, errors.New("active grant key is outside its activation window")
			}
			active = key
			activeWindow = window
		}
	}
	if active == nil {
		return nil, nil, nil, nil, keyValidity{}, errors.New("active grant key is missing")
	}
	return keys, active, fingerprints, windows, activeWindow, nil
}

func validKeyManifest(km securityKeyManifest, seenID map[string]bool, seenVersion map[uint64]bool) bool {
	return validKeyID(km.ID) && !seenID[km.ID] && km.Version != 0 && !seenVersion[km.Version] && validBaseName(km.File) &&
		(km.State == "active" || km.State == "verify-only" || km.State == keyStateRevoked) &&
		!km.VerifyUntil.IsZero() && km.VerifyUntil.After(km.ActivateAt)
}

func (m *SecurityManager) loadTLS(root *os.Root, manifest securityTLSManifest) (*tls.Config, *x509.CertPool, time.Time, []string, string, error) {
	certPEM, err := readRootFile(root, manifest.CertificateFile)
	if err != nil {
		return nil, nil, time.Time{}, nil, "", err
	}
	keyPEM, err := readRootFile(root, manifest.PrivateKeyFile)
	if err != nil {
		return nil, nil, time.Time{}, nil, "", err
	}
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, time.Time{}, nil, "", fmt.Errorf("load TLS identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		return nil, nil, time.Time{}, nil, "", err
	}
	now := m.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || !hasUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return nil, nil, time.Time{}, nil, "", errors.New("server certificate is not currently valid for server authentication")
	}
	serverCert.Leaf = leaf
	caPEM, err := readRootFile(root, manifest.ClientCAFile)
	if err != nil {
		return nil, nil, time.Time{}, nil, "", err
	}
	pool := x509.NewCertPool()
	caFingerprints, err := appendCAs(pool, caPEM)
	if err != nil {
		return nil, nil, time.Time{}, nil, "", err
	}
	serverSum := sha256.Sum256(leaf.Raw)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAnyClientCert}
	return cfg, pool, leaf.NotAfter, caFingerprints, hex.EncodeToString(serverSum[:]), nil
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
		clients[entry.URI] = ClientPolicy{MayAttestOwner: entry.MayAttestOwner, Administrator: entry.Administrator}
	}
	return clients, nil
}

func authorityDigest(mf securityManifest, ttl, skew time.Duration, keyFingerprints map[string]string, caFingerprints []string, serverFingerprint string) (string, error) {
	keys := slices.Clone(mf.Keys)
	slices.SortFunc(keys, func(a, b securityKeyManifest) int { return strings.Compare(a.ID, b.ID) })
	clients := slices.Clone(mf.Clients)
	slices.SortFunc(clients, func(a, b securityClientManifest) int { return strings.Compare(a.URI, b.URI) })
	canonical := struct {
		Version                       int
		Generation                    uint64
		Issuer, Audience, ActiveKeyID string
		GrantTTL, ClockSkew           int64
		Keys                          []securityKeyManifest
		TLS                           securityTLSManifest
		Clients                       []securityClientManifest
		KeyFingerprints               map[string]string
		CAFingerprints                []string
		ServerFingerprint             string
	}{
		Version: mf.Version, Generation: mf.Generation, Issuer: mf.Issuer, Audience: mf.Audience, ActiveKeyID: mf.ActiveKeyID,
		GrantTTL: int64(ttl), ClockSkew: int64(skew), Keys: keys, TLS: mf.TLS, Clients: clients,
		KeyFingerprints: keyFingerprints, CAFingerprints: slices.Clone(caFingerprints), ServerFingerprint: serverFingerprint,
	}
	raw, err := json.Marshal(canonical)
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
		return cm, false, securityLedger{Fingerprints: map[string]string{}}, nil
	}
	if err != nil {
		return nil, false, securityLedger{}, err
	}
	ledger := securityLedger{Fingerprints: map[string]string{}}
	if raw := cm.Data[securityStateDataKey]; raw != "" {
		if err := executionenv.DecodeStrict([]byte(raw), &ledger); err != nil || ledger.Generation == 0 || ledger.Digest == "" || ledger.Fingerprints == nil || len(ledger.Fingerprints) > maxSecurityKeys {
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
		if ledger.Digest != s.digest || !containsFingerprints(ledger.Fingerprints, s.fingerprints) {
			return false, errors.New("security generation authority drift rejected")
		}
		s.fingerprints = maps.Clone(ledger.Fingerprints)
		return false, nil
	}
	for identity, fp := range s.fingerprints {
		old, exists := ledger.Fingerprints[identity]
		if exists && old != fp {
			return false, errors.New("security key identity reuse rejected")
		}
		if !exists && !keyVersionAdvances(ledger.Fingerprints, identity) {
			return false, errors.New("security key version rollback rejected")
		}
		ledger.Fingerprints[identity] = fp
	}
	if len(ledger.Fingerprints) > maxSecurityKeys {
		return false, errors.New("security key tombstone limit reached; rotate issuer")
	}
	ledger.Generation = s.generation
	ledger.Digest = s.digest
	s.fingerprints = maps.Clone(ledger.Fingerprints)
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
func readEd25519(root *os.Root, name string) (ed25519.PrivateKey, error) {
	b, err := readRootFile(root, name)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("key must be one PKCS8 PEM block")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid PKCS8 key")
	}
	key, ok := raw.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("key is not Ed25519")
	}
	return key, nil
}
func validBaseName(v string) bool { return v != "" && filepath.Base(v) == v && v != "." && v != ".." }
func validKeyID(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func hasUsage(usages []x509.ExtKeyUsage, wanted x509.ExtKeyUsage) bool {
	for _, u := range usages {
		if u == wanted || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}
func containsFingerprints(authority, candidate map[string]string) bool {
	for identity, fingerprint := range candidate {
		if authority[identity] != fingerprint {
			return false
		}
	}
	return true
}

func keyVersionAdvances(authority map[string]string, identity string) bool {
	separator := strings.LastIndexByte(identity, ':')
	if separator <= 0 {
		return false
	}
	id := identity[:separator]
	version, err := strconv.ParseUint(identity[separator+1:], 10, 64)
	if err != nil {
		return false
	}
	for previous := range authority {
		previousSeparator := strings.LastIndexByte(previous, ':')
		if previousSeparator <= 0 || previous[:previousSeparator] != id {
			continue
		}
		previousVersion, err := strconv.ParseUint(previous[previousSeparator+1:], 10, 64)
		if err != nil || version <= previousVersion {
			return false
		}
	}
	return true
}
