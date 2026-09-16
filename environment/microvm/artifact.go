package microvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/stacklok/go-microvm/extract"
)

// ArtifactKind identifies one executable input to a microVM.
type ArtifactKind string

const (
	// ArtifactRuntime is the go-microvm runner and libkrun bundle.
	ArtifactRuntime ArtifactKind = "runtime"
	// ArtifactFirmware is the libkrunfw bundle.
	ArtifactFirmware ArtifactKind = "firmware"
	// ArtifactExecutionImage is the admitted Brood guest root filesystem.
	ArtifactExecutionImage ArtifactKind = "execution-image"
	// ArtifactGuestAgent is the independently built guest protocol binary.
	ArtifactGuestAgent ArtifactKind = "guest-agent"
)

var (
	// ErrMutableArtifact rejects references that are not digest-pinned.
	ErrMutableArtifact = errors.New("microvm artifact is not pinned to an immutable digest")
	// ErrDigestMismatch rejects resolver output that differs from the requested digest.
	ErrDigestMismatch = errors.New("microvm artifact digest mismatch")
	// ErrUnverifiedArtifact rejects evidence that does not satisfy operator policy.
	ErrUnverifiedArtifact = errors.New("microvm artifact verification failed")
	// ErrCorruptCacheEntry rejects cache payload or metadata corruption.
	ErrCorruptCacheEntry = errors.New("verified artifact cache entry is corrupt")
	// ErrStalePolicy rejects an entry admitted under another policy revision.
	ErrStalePolicy = errors.New("verified artifact cache entry uses a stale policy")
)

// ArtifactRequest is an operator-resolved artifact reference. Digest must be a
// canonical sha256 digest; Reference is retained only for resolver lookup.
type ArtifactRequest struct {
	Kind               ArtifactKind
	Reference          string
	Digest             string
	ManifestDigest     string
	DiscoveryReference string
	ResolutionEvidence string
	Platform           string
}

// Attestation is a signed in-toto statement binding an artifact digest to a predicate.
type Attestation struct {
	PredicateType string `json:"predicate_type"`
	SubjectDigest string `json:"subject_digest"`
	Statement     []byte `json:"statement"`
}

// VerificationEvidence carries one Sigstore bundle for the attestation statement.
type VerificationEvidence struct {
	Bundle      []byte      `json:"bundle"`
	Attestation Attestation `json:"attestation"`
}

// EvidenceVerifier verifies a Sigstore bundle against exact operator-owned identity policy.
type EvidenceVerifier interface {
	Verify(context.Context, []byte, []byte, string, string) error
}

// ResolvedArtifact is immutable resolver output. Runtime and firmware Sources
// are passed through go-microvm's extract.Source model; the execution image is
// represented by the same materialization seam until VM assembly consumes it.
type ResolvedArtifact struct {
	Kind           ArtifactKind
	Digest         string
	ManifestDigest string
	Source         extract.Source
	Evidence       VerificationEvidence
}

// ArtifactResolver resolves a digest-pinned reference without granting it cache
// or execution authority.
type ArtifactResolver interface {
	Resolve(context.Context, ArtifactRequest) (ResolvedArtifact, error)
}

// TrustPolicy is operator-owned verification configuration.
type TrustPolicy struct {
	Revision             string
	CertificateIdentity  string
	OIDCIssuer           string
	PublicKeyIdentity    string
	Verifier             EvidenceVerifier
	RequiredAttestations map[ArtifactKind]string
	RevokedIdentities    map[string]struct{}
}

// VerifiedArtifact is a cache-admitted artifact. Source always points at the
// immutable admitted payload, never resolver staging content.
type VerifiedArtifact struct {
	Kind               ArtifactKind
	Digest             string
	ManifestDigest     string
	DiscoveryReference string
	ResolutionEvidence string
	Platform           string
	Path               string
	Source             extract.Source
}

// VerifiedArtifacts is the complete set required to launch a microVM.
type VerifiedArtifacts struct {
	Runtime        VerifiedArtifact
	Firmware       VerifiedArtifact
	ExecutionImage VerifiedArtifact
	GuestAgent     VerifiedArtifact
}

// ByKind returns the verified artifact of kind, or its zero value.
func (a VerifiedArtifacts) ByKind(kind ArtifactKind) VerifiedArtifact {
	switch kind {
	case ArtifactRuntime:
		return a.Runtime
	case ArtifactFirmware:
		return a.Firmware
	case ArtifactExecutionImage:
		return a.ExecutionImage
	case ArtifactGuestAgent:
		return a.GuestAgent
	default:
		return VerifiedArtifact{}
	}
}

// All returns every independently admitted launch artifact in launch order.
func (a VerifiedArtifacts) All() []VerifiedArtifact {
	return []VerifiedArtifact{a.Runtime, a.Firmware, a.ExecutionImage, a.GuestAgent}
}

// Launcher is the narrow handoff to the later VM lifecycle implementation.
type Launcher interface {
	Launch(context.Context, VerifiedArtifacts) (vmID string, err error)
}

// MetadataStore durably records the artifact identities used by a launched VM.
type MetadataStore interface {
	Save(context.Context, EnvironmentMetadata) error
}

// EnvironmentMetadata is the durable artifact-verification portion of an
// environment record.
type EnvironmentMetadata struct {
	SessionID       string
	VMID            string
	Artifacts       map[ArtifactKind]string
	ManifestDigests map[ArtifactKind]string
	PolicyRevision  string
}

// Provisioner verifies a complete artifact set before crossing the launch seam.
type Provisioner struct {
	cache    *VerifiedCache
	resolver ArtifactResolver
	policy   TrustPolicy
	launcher Launcher
	metadata MetadataStore
	observer *OperationsObserver
}

// NewProvisioner constructs the artifact-gated launch coordinator.
func NewProvisioner(cache *VerifiedCache, resolver ArtifactResolver, policy TrustPolicy, launcher Launcher, metadata MetadataStore, observers ...*OperationsObserver) *Provisioner {
	var observer *OperationsObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &Provisioner{cache: cache, resolver: resolver, policy: clonePolicy(policy), launcher: launcher, metadata: metadata, observer: observer}
}

// Verify admits the complete artifact set without crossing the VM launch seam.
// It returns the policy revision that admitted the immutable identities.
func (p *Provisioner) Verify(ctx context.Context, requests map[ArtifactKind]ArtifactRequest) (VerifiedArtifacts, string, error) {
	if p.cache == nil || p.resolver == nil {
		return VerifiedArtifacts{}, "", errors.New("microvm artifact verification is not fully configured")
	}
	var artifacts VerifiedArtifacts
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		request, ok := requests[kind]
		if !ok || request.Kind != kind {
			p.observeVerification(kind, OutcomeFailure)
			return VerifiedArtifacts{}, "", fmt.Errorf("%w: missing %s", ErrMutableArtifact, kind)
		}
		verified, err := p.cache.resolve(ctx, request, p.resolver, p.policy)
		if err != nil {
			p.observeVerification(kind, OutcomeFailure)
			return VerifiedArtifacts{}, "", fmt.Errorf("verify %s: %w", kind, err)
		}
		p.observeVerification(kind, OutcomeSuccess)
		switch kind {
		case ArtifactRuntime:
			artifacts.Runtime = verified
		case ArtifactFirmware:
			artifacts.Firmware = verified
		case ArtifactExecutionImage:
			artifacts.ExecutionImage = verified
		case ArtifactGuestAgent:
			artifacts.GuestAgent = verified
		}
	}
	return artifacts, p.policy.Revision, nil
}

func (p *Provisioner) observeVerification(kind ArtifactKind, outcome Outcome) {
	if p.observer != nil {
		p.observer.ArtifactVerification(kind, outcome)
	}
}

// Provision admits the complete artifact set, launches it, and records the
// immutable identities and policy revision used by that VM.
func (p *Provisioner) Provision(ctx context.Context, sessionID string, requests map[ArtifactKind]ArtifactRequest) (EnvironmentMetadata, error) {
	if p.launcher == nil || p.metadata == nil {
		return EnvironmentMetadata{}, errors.New("microvm artifact provisioning is not fully configured")
	}
	artifacts, policyRevision, err := p.Verify(ctx, requests)
	if err != nil {
		return EnvironmentMetadata{}, err
	}

	vmID, err := p.launchVerified(ctx, artifacts)
	if err != nil {
		return EnvironmentMetadata{}, fmt.Errorf("launch verified microvm: %w", err)
	}
	metadata := EnvironmentMetadata{
		SessionID: sessionID,
		VMID:      vmID,
		Artifacts: map[ArtifactKind]string{
			ArtifactRuntime: artifacts.Runtime.Digest, ArtifactFirmware: artifacts.Firmware.Digest,
			ArtifactExecutionImage: artifacts.ExecutionImage.Digest, ArtifactGuestAgent: artifacts.GuestAgent.Digest,
		},
		ManifestDigests: map[ArtifactKind]string{
			ArtifactExecutionImage: artifacts.ExecutionImage.ManifestDigest,
		},
		PolicyRevision: policyRevision,
	}
	if err := p.metadata.Save(ctx, metadata); err != nil {
		return EnvironmentMetadata{}, fmt.Errorf("persist microvm artifact metadata: %w", err)
	}
	return metadata, nil
}

func (p *Provisioner) launchVerified(ctx context.Context, artifacts VerifiedArtifacts) (string, error) {
	launchArtifacts, release, err := p.LockAndValidate(ctx, artifacts)
	if err != nil {
		return "", err
	}
	defer release()
	return p.launcher.Launch(ctx, launchArtifacts)
}

// LockAndValidate revalidates admitted bytes while holding every corresponding
// cache lock, then copies them into one private launch snapshot. The caller must
// pass the returned identities to runtime and release them only after runtime has
// consumed their paths. Cache-path mutation cannot alter the snapshot bytes.
func (p *Provisioner) LockAndValidate(ctx context.Context, artifacts VerifiedArtifacts) (VerifiedArtifacts, func(), error) {
	locks := make([]*flock.Flock, 0, len(artifacts.All()))
	var snapshotRoot string
	release := func() {
		if snapshotRoot != "" {
			_ = os.RemoveAll(snapshotRoot)
		}
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Unlock()
		}
	}
	for _, artifact := range artifacts.All() {
		request := ArtifactRequest{
			Kind: artifact.Kind, Digest: artifact.Digest, ManifestDigest: artifact.ManifestDigest,
			DiscoveryReference: artifact.DiscoveryReference, ResolutionEvidence: artifact.ResolutionEvidence, Platform: artifact.Platform,
		}
		entryLock, err := p.cache.lockEntry(ctx, request)
		if err != nil {
			release()
			return VerifiedArtifacts{}, nil, err
		}
		locks = append(locks, entryLock)
		entry := filepath.Dir(artifact.Path)
		verified, err := loadVerifiedEntry(ctx, entry, request, p.policy)
		if err != nil || verified.Path != artifact.Path {
			release()
			if err != nil {
				return VerifiedArtifacts{}, nil, err
			}
			return VerifiedArtifacts{}, nil, ErrCorruptCacheEntry
		}
	}

	var err error
	stagingRoot := filepath.Join(p.cache.root, "staging")
	if err = os.MkdirAll(stagingRoot, 0o700); err != nil {
		release()
		return VerifiedArtifacts{}, nil, err
	}
	snapshotRoot, err = os.MkdirTemp(stagingRoot, "launch-")
	if err != nil {
		release()
		return VerifiedArtifacts{}, nil, err
	}
	launchArtifacts, err := snapshotArtifacts(artifacts, snapshotRoot)
	if err != nil {
		release()
		return VerifiedArtifacts{}, nil, err
	}
	return launchArtifacts, release, nil
}

// VerifiedCache admits artifacts by atomic rename after materialization and
// verification. A file lock serializes cold admission across processes.
type VerifiedCache struct {
	root string
}

// NewVerifiedCache creates an atomic verified cache rooted at root.
func NewVerifiedCache(root string) *VerifiedCache { return &VerifiedCache{root: root} }

type cacheMetadata struct {
	Kind               ArtifactKind         `json:"kind"`
	Digest             string               `json:"digest"`
	ManifestDigest     string               `json:"manifest_digest,omitempty"`
	DiscoveryReference string               `json:"discovery_reference,omitempty"`
	ResolutionEvidence string               `json:"resolution_evidence,omitempty"`
	Platform           string               `json:"platform,omitempty"`
	ContentDigest      string               `json:"content_digest"`
	PolicyRevision     string               `json:"policy_revision"`
	Evidence           VerificationEvidence `json:"evidence"`
}

func (c *VerifiedCache) resolve(ctx context.Context, request ArtifactRequest, resolver ArtifactResolver, policy TrustPolicy) (VerifiedArtifact, error) {
	if err := validateRequest(request, policy); err != nil {
		return VerifiedArtifact{}, err
	}
	entry := filepath.Join(c.root, "entries", string(request.Kind), artifactCacheKey(request))
	entryLock, err := c.lockEntry(ctx, request)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	defer func() { _ = entryLock.Unlock() }()

	if _, err := os.Lstat(entry); err == nil {
		return loadVerifiedEntry(ctx, entry, request, policy)
	} else if !errors.Is(err, os.ErrNotExist) {
		return VerifiedArtifact{}, err
	}
	return c.admit(ctx, entry, request, resolver, policy)
}

func validateRequest(request ArtifactRequest, policy TrustPolicy) error {
	pinnedDigest := request.Digest
	if request.ManifestDigest != "" {
		pinnedDigest = request.ManifestDigest
	}
	if !validDigest(request.Digest) || !validDigest(pinnedDigest) || request.Reference == "" || !strings.HasSuffix(request.Reference, "@"+pinnedDigest) {
		return ErrMutableArtifact
	}
	if policy.Revision == "" {
		return fmt.Errorf("%w: empty policy revision", ErrUnverifiedArtifact)
	}
	broodFieldsSet := request.DiscoveryReference != "" || request.ResolutionEvidence != "" || request.Platform != ""
	if request.Kind == ArtifactExecutionImage && request.ManifestDigest != "" {
		if request.DiscoveryReference != "ghcr.io/stacklok/brood-box/base:latest" ||
			!validDigest(request.ManifestDigest) || !validDigest(request.ResolutionEvidence) ||
			(request.Platform != "linux/amd64" && request.Platform != "linux/arm64") {
			return fmt.Errorf("%w: invalid Brood discovery resolution", ErrUnverifiedArtifact)
		}
	} else if broodFieldsSet {
		return fmt.Errorf("%w: Brood discovery evidence is execution-image only", ErrUnverifiedArtifact)
	}
	return nil
}

func artifactCacheKey(request ArtifactRequest) string {
	tree := strings.TrimPrefix(request.Digest, "sha256:")
	if request.ManifestDigest == "" {
		return tree
	}
	return strings.TrimPrefix(request.ManifestDigest, "sha256:") + "-" + tree
}

func (c *VerifiedCache) lockEntry(ctx context.Context, request ArtifactRequest) (*flock.Flock, error) {
	entryDir := filepath.Join(c.root, "entries", string(request.Kind))
	if err := os.MkdirAll(entryDir, 0o700); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(c.root, "locks", string(request.Kind))
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	entryLock := flock.New(filepath.Join(lockDir, artifactCacheKey(request)+".lock"))
	locked, err := entryLock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ctx.Err()
	}
	return entryLock, nil
}

func (c *VerifiedCache) admit(ctx context.Context, entry string, request ArtifactRequest, resolver ArtifactResolver, policy TrustPolicy) (VerifiedArtifact, error) {
	resolved, err := resolver.Resolve(ctx, request)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	if resolved.Kind != request.Kind || resolved.Digest != request.Digest || resolved.ManifestDigest != request.ManifestDigest {
		return VerifiedArtifact{}, ErrDigestMismatch
	}
	if resolved.Source == nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: artifact has no source", ErrUnverifiedArtifact)
	}

	stagingRoot := filepath.Join(c.root, "staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return VerifiedArtifact{}, err
	}
	work, err := os.MkdirTemp(stagingRoot, "admit-")
	if err != nil {
		return VerifiedArtifact{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	materialized, err := resolved.Source.Ensure(ctx, work)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	stage := filepath.Join(work, "entry")
	payload := filepath.Join(stage, "payload")
	if err := copyTree(materialized, payload); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("stage artifact: %w", err)
	}
	contentDigest, err := digestTree(payload)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	if contentDigest != resolved.Digest {
		return VerifiedArtifact{}, ErrDigestMismatch
	}
	if err := verifyArtifact(ctx, request, resolved.Evidence, policy); err != nil {
		return VerifiedArtifact{}, err
	}
	meta := cacheMetadata{
		Kind: resolved.Kind, Digest: resolved.Digest, ManifestDigest: resolved.ManifestDigest,
		DiscoveryReference: request.DiscoveryReference, ResolutionEvidence: request.ResolutionEvidence, Platform: request.Platform,
		ContentDigest: contentDigest, PolicyRevision: policy.Revision, Evidence: resolved.Evidence,
	}
	if err := writeMetadata(filepath.Join(stage, "verification.json"), meta); err != nil {
		return VerifiedArtifact{}, err
	}
	if err := os.Rename(stage, entry); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("atomically admit artifact: %w", err)
	}
	if err := syncDir(filepath.Dir(entry)); err != nil {
		return VerifiedArtifact{}, err
	}
	return verifiedFromEntry(entry, meta), nil
}

func loadVerifiedEntry(ctx context.Context, entry string, request ArtifactRequest, policy TrustPolicy) (VerifiedArtifact, error) {
	data, err := os.ReadFile(filepath.Join(entry, "verification.json"))
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: metadata: %v", ErrCorruptCacheEntry, err)
	}
	var meta cacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: metadata: %v", ErrCorruptCacheEntry, err)
	}
	if meta.Kind != request.Kind || meta.Digest != request.Digest || meta.ManifestDigest != request.ManifestDigest ||
		meta.DiscoveryReference != request.DiscoveryReference || meta.ResolutionEvidence != request.ResolutionEvidence || meta.Platform != request.Platform {
		return VerifiedArtifact{}, ErrCorruptCacheEntry
	}
	if meta.PolicyRevision != policy.Revision {
		return VerifiedArtifact{}, ErrStalePolicy
	}
	got, err := digestTree(filepath.Join(entry, "payload"))
	if err != nil || got != meta.ContentDigest || got != meta.Digest {
		return VerifiedArtifact{}, ErrCorruptCacheEntry
	}
	if err := verifyArtifact(ctx, request, meta.Evidence, policy); err != nil {
		return VerifiedArtifact{}, err
	}
	return verifiedFromEntry(entry, meta), nil
}

func verifiedFromEntry(entry string, meta cacheMetadata) VerifiedArtifact {
	payload := filepath.Join(entry, "payload")
	return VerifiedArtifact{
		Kind: meta.Kind, Digest: meta.Digest, ManifestDigest: meta.ManifestDigest,
		DiscoveryReference: meta.DiscoveryReference, ResolutionEvidence: meta.ResolutionEvidence, Platform: meta.Platform,
		Path: payload, Source: extract.Dir(payload),
	}
}

func snapshotArtifacts(artifacts VerifiedArtifacts, root string) (VerifiedArtifacts, error) {
	var snapshot VerifiedArtifacts
	for _, artifact := range artifacts.All() {
		path := filepath.Join(root, string(artifact.Kind))
		if err := copyTree(artifact.Path, path); err != nil {
			return VerifiedArtifacts{}, fmt.Errorf("snapshot verified %s artifact: %w", artifact.Kind, err)
		}
		digest, err := digestTree(path)
		if err != nil || digest != artifact.Digest {
			return VerifiedArtifacts{}, ErrCorruptCacheEntry
		}
		verified := VerifiedArtifact{
			Kind: artifact.Kind, Digest: artifact.Digest, ManifestDigest: artifact.ManifestDigest,
			DiscoveryReference: artifact.DiscoveryReference, ResolutionEvidence: artifact.ResolutionEvidence, Platform: artifact.Platform,
			Path: path, Source: extract.Dir(path),
		}
		switch artifact.Kind {
		case ArtifactRuntime:
			snapshot.Runtime = verified
		case ArtifactFirmware:
			snapshot.Firmware = verified
		case ArtifactExecutionImage:
			snapshot.ExecutionImage = verified
		case ArtifactGuestAgent:
			snapshot.GuestAgent = verified
		default:
			return VerifiedArtifacts{}, ErrCorruptCacheEntry
		}
	}
	return snapshot, nil
}

func trustIdentity(policy TrustPolicy) (string, error) {
	keyless := policy.CertificateIdentity != "" && policy.OIDCIssuer != "" && policy.PublicKeyIdentity == ""
	keyed := policy.CertificateIdentity == "" && policy.OIDCIssuer == "" && policy.PublicKeyIdentity != ""
	switch {
	case keyless:
		return policy.CertificateIdentity, nil
	case keyed:
		return policy.PublicKeyIdentity, nil
	default:
		return "", fmt.Errorf("%w: sigstore trust policy is incomplete or ambiguous", ErrUnverifiedArtifact)
	}
}

type provenanceDependency struct {
	URI      string            `json:"uri"`
	Digest   map[string]string `json:"digest"`
	Platform string            `json:"platform"`
}

func verifyArtifact(ctx context.Context, request ArtifactRequest, evidence VerificationEvidence, policy TrustPolicy) error {
	identity, err := trustIdentity(policy)
	if err != nil || policy.Verifier == nil {
		return fmt.Errorf("%w: sigstore trust policy is incomplete or ambiguous", ErrUnverifiedArtifact)
	}
	if _, revoked := policy.RevokedIdentities[identity]; revoked {
		return fmt.Errorf("%w: signer identity is revoked", ErrUnverifiedArtifact)
	}
	required := policy.RequiredAttestations[request.Kind]
	if required == "" || evidence.Attestation.PredicateType != required || evidence.Attestation.SubjectDigest != request.Digest || len(evidence.Attestation.Statement) == 0 || len(evidence.Bundle) == 0 {
		return fmt.Errorf("%w: required attestation is missing or does not match", ErrUnverifiedArtifact)
	}
	var statement struct {
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			BuildDefinition struct {
				ResolvedDependencies []provenanceDependency `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(evidence.Attestation.Statement, &statement); err != nil || statement.PredicateType != required {
		return fmt.Errorf("%w: attestation statement is malformed or has the wrong predicate", ErrUnverifiedArtifact)
	}
	matched := false
	for _, subject := range statement.Subject {
		if "sha256:"+subject.Digest["sha256"] == request.Digest {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%w: attestation subject digest does not match", ErrUnverifiedArtifact)
	}
	if err := verifyBroodResolution(statement.Predicate.BuildDefinition.ResolvedDependencies, request); err != nil {
		return err
	}
	if err := policy.Verifier.Verify(ctx, evidence.Attestation.Statement, evidence.Bundle, identity, policy.OIDCIssuer); err != nil {
		return fmt.Errorf("%w: Sigstore bundle: %v", ErrUnverifiedArtifact, err)
	}
	return nil
}

func verifyBroodResolution(dependencies []provenanceDependency, request ArtifactRequest) error {
	if request.Kind != ArtifactExecutionImage || request.ManifestDigest == "" {
		return nil
	}
	for _, dependency := range dependencies {
		if dependency.URI == request.DiscoveryReference &&
			"sha256:"+dependency.Digest["sha256"] == request.ManifestDigest &&
			"sha256:"+dependency.Digest["resolutionEvidence"] == request.ResolutionEvidence &&
			dependency.Platform == request.Platform {
			return nil
		}
	}
	return fmt.Errorf("%w: Brood discovery resolution does not match", ErrUnverifiedArtifact)
}

func validDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func clonePolicy(policy TrustPolicy) TrustPolicy {
	cloned := TrustPolicy{
		Revision:             policy.Revision,
		CertificateIdentity:  policy.CertificateIdentity,
		OIDCIssuer:           policy.OIDCIssuer,
		PublicKeyIdentity:    policy.PublicKeyIdentity,
		Verifier:             policy.Verifier,
		RequiredAttestations: make(map[ArtifactKind]string, len(policy.RequiredAttestations)),
		RevokedIdentities:    make(map[string]struct{}, len(policy.RevokedIdentities)),
	}
	for kind, predicate := range policy.RequiredAttestations {
		cloned.RequiredAttestations[kind] = predicate
	}
	for identity := range policy.RevokedIdentities {
		cloned.RevokedIdentities[identity] = struct{}{}
	}
	return cloned
}

func copyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("artifact source must be a directory")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(destination, info.Mode()); err != nil {
		return err
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = destinationRoot.Close() }()
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			if rel == "." {
				return nil
			}
			if err := destinationRoot.MkdirAll(rel, info.Mode().Perm()); err != nil {
				return err
			}
			return destinationRoot.Chmod(rel, info.Mode())
		case info.Mode()&os.ModeSymlink != 0:
			targetName, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if !containedSymlink(source, path, targetName) {
				return fmt.Errorf("artifact symlink %q escapes its tree", rel)
			}
			return destinationRoot.Symlink(targetName, rel)
		case info.Mode().IsRegular():
			return copyFileToRoot(destinationRoot, path, rel, info.Mode())
		default:
			return fmt.Errorf("unsupported artifact entry %q", rel)
		}
	})
}

func containedSymlink(root, path, target string) bool {
	if target == "" {
		return false
	}
	var resolved string
	if filepath.IsAbs(target) {
		resolved = filepath.Join(root, strings.TrimLeftFunc(target, func(r rune) bool { return r == '/' || r == '\\' }))
	} else {
		resolved = filepath.Join(filepath.Dir(path), target)
	}
	rel, err := filepath.Rel(root, filepath.Clean(resolved))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyFileToRoot(root *os.Root, source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := root.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return root.Chmod(destination, mode)
}

// ArtifactTreeDigest returns the canonical materialized-tree identity used by
// strict artifact admission and release provenance subjects.
func ArtifactTreeDigest(root string) (string, error) { return digestTree(root) }

func digestTree(root string) (string, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return "", errors.New("cache payload contains unsupported entry")
		}
		writeHashField(h, filepath.ToSlash(rel))
		writeHashField(h, info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			if !containedSymlink(root, path, target) {
				return "", errors.New("cache payload symlink escapes its tree")
			}
			writeHashField(h, target)
		} else if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(h, file); err != nil {
				_ = file.Close()
				return "", err
			}
			if err := file.Close(); err != nil {
				return "", err
			}
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func writeHashField(h hash.Hash, value string) {
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func writeMetadata(path string, metadata cacheMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
