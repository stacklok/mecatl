package microvm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

// Kind is the open EnvironmentRef kind owned by the microVM adapter.
const Kind = "microvm"

// EnvironmentState is the durable driver-side lifecycle state.
type EnvironmentState string

const (
	// EnvironmentProvisioning has durable planned identities but is not runnable.
	EnvironmentProvisioning EnvironmentState = "provisioning"
	// EnvironmentReady has passed readiness and protocol negotiation.
	EnvironmentReady EnvironmentState = "ready"
	// EnvironmentDeleting is a durable tombstone written before destructive work.
	EnvironmentDeleting EnvironmentState = "deleting"
	// EnvironmentCleanupPending retains resources for lifecycle reconciliation.
	EnvironmentCleanupPending EnvironmentState = "cleanup-pending"
	// EnvironmentDestroyed records successful rollback or permanent destruction.
	EnvironmentDestroyed EnvironmentState = "destroyed"
)

var (
	// ErrInvalidEnvironmentRef rejects malformed or non-microVM refs.
	ErrInvalidEnvironmentRef = errors.New("invalid microvm environment ref")
	// ErrEnvironmentUnknown rejects refs absent from the durable registry.
	ErrEnvironmentUnknown = errors.New("microvm environment is unknown")
	// ErrEnvironmentForeign rejects environments owned by another caller.
	ErrEnvironmentForeign = errors.New("microvm environment belongs to another owner")
	// ErrEnvironmentStale rejects non-ready or generation-mismatched refs.
	ErrEnvironmentStale = errors.New("microvm environment generation is stale")
	// ErrEnvironmentDestroyed rejects permanently destroyed generations.
	ErrEnvironmentDestroyed = errors.New("microvm environment is destroyed")
	// ErrEnvironmentIncompatible rejects records without the required protocol.
	ErrEnvironmentIncompatible = errors.New("microvm environment protocol is incompatible")
)

// EnvironmentRef is the nested module's transport-neutral form of the durable
// engine ref. A thin host adapter maps it to session.EnvironmentRef.
type EnvironmentRef struct {
	Kind string
	ID   string
}

// EnvironmentIdentity reserves every cleanup-relevant identity before resources
// are created. Allocators must return collision-resistant, generation-fenced values.
type EnvironmentIdentity struct {
	EnvironmentID string
	VMID          string
	Endpoint      string
	Generation    uint32
}

// IdentityAllocator allocates one complete provisional identity set.
type IdentityAllocator interface {
	Allocate(sessionID string) (EnvironmentIdentity, error)
}

// ArtifactVerifier admits the complete immutable artifact set under one policy revision.
type ArtifactVerifier interface {
	Verify(context.Context, map[ArtifactKind]ArtifactRequest) (VerifiedArtifacts, string, error)
}

type artifactLaunchValidator interface {
	LockAndValidate(context.Context, VerifiedArtifacts) (launch VerifiedArtifacts, release func(), err error)
}

// WorktreeLifecycle is task-05 preparation plus its idempotent rollback operation.
type WorktreeLifecycle interface {
	Prepare(context.Context, worktree.Request) (*worktree.Prepared, error)
	Cleanup(context.Context, *worktree.Prepared) error
}

// GuestPrebootConfigPath is the rootfs location consumed by the guest agent before
// workload services are started.
const GuestPrebootConfigPath = "/etc/mecatl/guest-agent.json"

// GuestPrebootConfig is immutable boot-time policy and guest-agent material. The
// concrete VMRuntime installs it into the rootfs before booting the workload.
type GuestPrebootConfig struct {
	DisableIPv6     bool                 `json:"disable_ipv6"`
	AgentEndpoint   string               `json:"agent_endpoint"`
	Binding         control.Binding      `json:"binding"`
	Capabilities    control.Capabilities `json:"capabilities"`
	MaxMessageBytes uint32               `json:"max_message_bytes"`
}

// GuestPrebootArtifact is the explicit rootfs hook passed to VMRuntime.
type GuestPrebootArtifact struct {
	RootfsPath string             `json:"rootfs_path"`
	Config     GuestPrebootConfig `json:"config"`
}

// VMCreateRequest binds launch inputs to identities recorded before launch.
type VMCreateRequest struct {
	EnvironmentID string
	VMID          string
	Endpoint      string
	Generation    uint32
	Owner         string
	SessionID     string
	Profile       string
	Prepared      *worktree.Prepared
	Artifacts     map[ArtifactKind]string
	Verified      VerifiedArtifacts
	Resources     ResourceUsage
	Preboot       GuestPrebootArtifact
}

// VMRuntime is the narrow lifecycle seam around the go-microvm/libkrun runtime.
// Create must install VMCreateRequest.Preboot at its rootfs path before starting
// guest workload services and fail if it cannot. Destroy must be idempotent because
// Create may fail after partially provisioning.
type VMRuntime interface {
	Create(context.Context, VMCreateRequest) error
	WaitReady(context.Context, EnvironmentRecord) error
	Inspect(context.Context, EnvironmentRecord) (RuntimeStatus, error)
	Destroy(context.Context, EnvironmentRecord) error
}

// ProtocolNegotiator performs the task-04 binding and capability handshake.
type ProtocolNegotiator interface {
	Negotiate(context.Context, string, control.Binding) (control.Agreement, error)
}

// EnvironmentRegistry durably stores the driver record. Save is latest-value wins.
type EnvironmentRegistry interface {
	Save(context.Context, EnvironmentRecord) error
}

// SessionPersister is the final commit boundary. A successful call must durably
// stamp the host session's worktree and mapped EnvironmentRef.
type SessionPersister interface {
	Persist(context.Context, SessionPlacement) error
}

// EnforcedProfileStatus is the daemon-authored, response-safe projection of
// policy actually applied to a generation.
type EnforcedProfileStatus struct {
	Profile     string
	GuestEgress string
	HostEgress  string
}

// EnvironmentRecord is the complete durable driver identity and reconciliation record.
type EnvironmentRecord struct {
	State           EnvironmentState
	Owner           string
	SessionID       string
	Profile         string
	EnvironmentID   string
	Ref             EnvironmentRef
	Generation      uint32
	SourceCheckout  string
	WorktreePath    string
	MetadataPath    string
	GuestRoot       string
	VMID            string
	Endpoint        string
	Artifacts       map[ArtifactKind]string
	ManifestDigests map[ArtifactKind]string
	PolicyRevision  string
	ProfileStatus   EnforcedProfileStatus
	// AdmissionUsage is the durable retained reservation reconstructed after
	// daemon restart and released only after destruction is durable.
	AdmissionUsage  ResourceUsage
	Agreement       control.Agreement
	ProcessIdentity string
	RunnerPID       int
	// ParentRef and ForkBase are both set only for child generations. They bind
	// resume, merge, and reconciliation to the exact parent generation and the
	// immutable pre-mutation tree captured by Fork.
	ParentRef        EnvironmentRef
	ForkBase         string
	Tombstone        bool
	DeleteReason     DeleteReason
	PreserveWorktree bool
	VMDeleted        bool
	WorktreeDeleted  bool
}

// SessionPlacement is the exact durable data committed to the host session store.
type SessionPlacement struct {
	SessionID       string
	Owner           string
	Profile         string
	SourceCheckout  string
	WorktreePath    string
	GuestRoot       string
	Ref             EnvironmentRef
	Generation      uint32
	VMID            string
	Endpoint        string
	Artifacts       map[ArtifactKind]string
	ManifestDigests map[ArtifactKind]string
	PolicyRevision  string
}

// CreatedEnvironment is returned only after SessionPersister succeeds.
type CreatedEnvironment struct {
	Ref          EnvironmentRef
	Generation   uint32
	HostWorktree string
	GuestRoot    string
	// Admission retains active VM, worktree, CPU, RAM, disk and inode capacity
	// until the lifecycle owner destroys the environment and releases it.
	Admission *AdmissionLease
}

// CreateRequest contains caller identity plus already-resolved operator profile inputs.
type CreateRequest struct {
	Owner            string
	SessionID        string
	Profile          string
	Worktree         worktree.Request
	ArtifactRequests map[ArtifactKind]ArtifactRequest
	// Resources are retained for the environment lifetime. VM state, worktree,
	// and boot-rate dimensions are added by Lifecycle and must be zero here.
	Resources     ResourceUsage
	ProfileStatus EnforcedProfileStatus
	// ParentRef and ForkBase bind a delegated child before its worktree or VM is
	// exposed. They are either both zero or both generation-exact/non-empty.
	ParentRef EnvironmentRef
	ForkBase  string
}

// LifecycleDeps are the deterministic lifecycle transaction seams.
type LifecycleDeps struct {
	Identities IdentityAllocator
	Worktrees  WorktreeLifecycle
	Artifacts  ArtifactVerifier
	VMs        VMRuntime
	Protocol   ProtocolNegotiator
	Registry   EnvironmentRegistry
	Sessions   SessionPersister
	Admission  *AdmissionController
	Observer   *OperationsObserver
}

// Lifecycle coordinates one generation's failure-aware creation transaction.
type Lifecycle struct{ deps LifecycleDeps }

// NewLifecycle constructs a lifecycle coordinator. Configuration is checked at Create.
func NewLifecycle(deps LifecycleDeps) *Lifecycle { return &Lifecycle{deps: deps} }

// Create prepares, verifies, boots, negotiates, stamps, and persists one environment.
// Every resource identity is registered before provisioning; later failure either
// destroys the resources or leaves that durable record cleanup-pending/provisioning.
func (l *Lifecycle) Create(ctx context.Context, request CreateRequest) (CreatedEnvironment, error) {
	if err := l.validateRequest(request); err != nil {
		return CreatedEnvironment{}, err
	}
	admission, err := l.admitCreate(ctx, request)
	if err != nil {
		l.observeAdmissionError(err)
		return CreatedEnvironment{}, err
	}
	started := time.Now()
	committed := false
	defer func() {
		releaseUncommittedAdmission(admission, committed)
	}()
	identity, err := l.allocateIdentity(request.SessionID)
	if err != nil {
		return CreatedEnvironment{}, err
	}
	ref := EnvironmentRef{Kind: Kind, ID: identity.EnvironmentID + "@" + strconv.FormatUint(uint64(identity.Generation), 10)}
	record := EnvironmentRecord{
		State: EnvironmentProvisioning, Owner: request.Owner, SessionID: request.SessionID, Profile: request.Profile,
		EnvironmentID: identity.EnvironmentID, Ref: ref, Generation: identity.Generation,
		SourceCheckout: request.Worktree.Source, WorktreePath: request.Worktree.WorktreePath,
		MetadataPath: request.Worktree.MetadataPath, GuestRoot: worktree.GuestWorkspace,
		VMID: identity.VMID, Endpoint: identity.Endpoint, Artifacts: requestDigests(request.ArtifactRequests),
		ManifestDigests: requestManifestDigests(request.ArtifactRequests),
		ProfileStatus:   request.ProfileStatus, AdmissionUsage: bootingUsage(request.Resources), ParentRef: request.ParentRef, ForkBase: request.ForkBase,
	}
	if err := l.deps.Registry.Save(ctx, record); err != nil {
		return CreatedEnvironment{}, fmt.Errorf("persist provisional microvm record: %w", err)
	}

	var prepared *worktree.Prepared
	vmAttempted := false

	prepared, err = l.deps.Worktrees.Prepare(ctx, request.Worktree)
	if err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("prepare microvm worktree: %w", err))
	}
	if err := validatePreparedWorktree(prepared, record); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, err)
	}
	if err := l.deps.Registry.Save(ctx, record); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("persist prepared microvm worktree: %w", err))
	}

	verified, policyRevision, err := l.verifyArtifacts(ctx, request)
	if err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("verify microvm artifacts: %w", err))
	}
	if policyRevision == "" || !verifiedMatches(record.Artifacts, record.ManifestDigests, verified) {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, errors.New("verified microvm artifact identities do not match the provisional record"))
	}
	record.PolicyRevision = policyRevision
	if err := l.deps.Registry.Save(ctx, record); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("persist verified microvm artifacts: %w", err))
	}

	vmAttempted = true
	binding := control.Binding{Owner: record.Owner, SessionID: record.SessionID, EnvironmentID: record.EnvironmentID, Ref: record.Ref.ID, Generation: record.Generation}
	vmRequest := VMCreateRequest{
		EnvironmentID: record.EnvironmentID, VMID: record.VMID, Endpoint: record.Endpoint,
		Generation: record.Generation, Owner: record.Owner, SessionID: record.SessionID,
		Profile: record.Profile, Prepared: prepared, Artifacts: cloneArtifacts(record.Artifacts), Verified: verified,
		Resources: request.Resources,
		Preboot: GuestPrebootArtifact{
			RootfsPath: GuestPrebootConfigPath,
			Config: GuestPrebootConfig{
				DisableIPv6: true, AgentEndpoint: record.Endpoint, Binding: binding,
				Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes,
			},
		},
	}
	if err := l.createVMWithVerifiedArtifacts(ctx, verified, vmRequest); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, err)
	}
	if err := l.deps.VMs.WaitReady(ctx, record); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("wait for microvm readiness: %w", err))
	}
	record, err = l.inspectReadyRuntime(ctx, record)
	if err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, err)
	}
	if err := transitionAdmission(admission, request.Resources); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, err)
	}
	record.AdmissionUsage = activeUsage(request.Resources)

	agreement, err := l.deps.Protocol.Negotiate(ctx, record.Endpoint, binding)
	if err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("negotiate microvm guest protocol: %w", err))
	}
	if err := validateAgreement(agreement); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, err)
	}
	record.Agreement = agreement
	record.State = EnvironmentReady
	if err := l.deps.Registry.Save(ctx, record); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("persist ready microvm record: %w", err))
	}

	placement := SessionPlacement{
		SessionID: record.SessionID, Owner: record.Owner, Profile: record.Profile,
		SourceCheckout: record.SourceCheckout, WorktreePath: record.WorktreePath,
		GuestRoot: record.GuestRoot, Ref: record.Ref, Generation: record.Generation,
		VMID: record.VMID, Endpoint: record.Endpoint, Artifacts: cloneArtifacts(record.Artifacts),
		ManifestDigests: cloneArtifacts(record.ManifestDigests), PolicyRevision: record.PolicyRevision,
	}
	if err := l.deps.Sessions.Persist(ctx, placement); err != nil {
		return CreatedEnvironment{}, l.rollback(ctx, record, prepared, vmAttempted, fmt.Errorf("persist microvm-backed session: %w", err))
	}
	committed = true
	l.observeBootFinished(started)
	return CreatedEnvironment{Ref: record.Ref, Generation: record.Generation, HostWorktree: record.WorktreePath, GuestRoot: record.GuestRoot, Admission: admission}, nil
}

func (l *Lifecycle) createVMWithVerifiedArtifacts(ctx context.Context, verified VerifiedArtifacts, request VMCreateRequest) error {
	release := func() {}
	if validator, ok := l.deps.Artifacts.(artifactLaunchValidator); ok {
		var err error
		verified, release, err = validator.LockAndValidate(ctx, verified)
		if err != nil {
			return fmt.Errorf("revalidate microvm artifacts for launch: %w", err)
		}
		request.Verified = verified
	}
	defer release()
	if err := l.deps.VMs.Create(ctx, request); err != nil {
		return fmt.Errorf("create microvm: %w", err)
	}
	return nil
}

func (l *Lifecycle) observeBootFinished(started time.Time) {
	if l.deps.Observer != nil {
		l.deps.Observer.BootFinished(time.Since(started))
	}
}

func (l *Lifecycle) verifyArtifacts(ctx context.Context, request CreateRequest) (VerifiedArtifacts, string, error) {
	var lease *AdmissionLease
	var err error
	if l.deps.Admission != nil {
		lease, err = l.deps.Admission.AcquireContext(ctx, request.Owner, ResourceUsage{Pulls: 1})
		if err != nil {
			l.observeAdmissionError(err)
			return VerifiedArtifacts{}, "", err
		}
		defer lease.Release()
	}
	verified, policyRevision, err := l.deps.Artifacts.Verify(ctx, cloneArtifactRequests(request.ArtifactRequests))
	return verified, policyRevision, err
}

func (l *Lifecycle) allocateIdentity(sessionID string) (EnvironmentIdentity, error) {
	identity, err := l.deps.Identities.Allocate(sessionID)
	if err != nil {
		return EnvironmentIdentity{}, fmt.Errorf("allocate microvm identity: %w", err)
	}
	if err := validateIdentity(identity); err != nil {
		return EnvironmentIdentity{}, err
	}
	return identity, nil
}

func (l *Lifecycle) observeAdmissionError(err error) {
	if l.deps.Observer == nil {
		return
	}
	var admissionErr *AdmissionError
	if !errors.As(err, &admissionErr) {
		return
	}
	kind, ok := quotaKindForResource(admissionErr.Resource)
	if ok {
		l.deps.Observer.QuotaRejected(kind)
	}
}

func quotaKindForResource(resource Resource) (QuotaKind, bool) {
	switch resource {
	case ResourceBootingVMs, ResourceActiveVMs:
		return QuotaVMs, true
	case ResourceCPU:
		return QuotaVCPUs, true
	case ResourceRAMBytes:
		return QuotaMemory, true
	case ResourceDiskBytes:
		return QuotaDisk, true
	case ResourceExecs:
		return QuotaExecs, true
	case ResourceWorktrees:
		return QuotaWorktrees, true
	case ResourceInodes:
		return QuotaInodes, true
	case ResourceForks:
		return QuotaForks, true
	case ResourcePulls:
		return QuotaPulls, true
	case ResourceBootRate:
		return QuotaBootRate, true
	default:
		return 0, false
	}
}

func (l *Lifecycle) admitCreate(ctx context.Context, request CreateRequest) (*AdmissionLease, error) {
	if l.deps.Admission == nil {
		return nil, nil
	}
	return l.deps.Admission.AcquireContext(ctx, request.Owner, bootingUsage(request.Resources))
}

func releaseUncommittedAdmission(admission *AdmissionLease, committed bool) {
	if admission != nil && !committed {
		admission.Release()
	}
}

func transitionAdmission(admission *AdmissionLease, resources ResourceUsage) error {
	if admission == nil {
		return nil
	}
	if err := admission.Replace(activeUsage(resources)); err != nil {
		return fmt.Errorf("admit active microvm resources: %w", err)
	}
	return nil
}

func (l *Lifecycle) inspectReadyRuntime(ctx context.Context, record EnvironmentRecord) (EnvironmentRecord, error) {
	status, err := l.deps.VMs.Inspect(ctx, record)
	if err != nil {
		return record, fmt.Errorf("inspect ready microvm identity: %w", err)
	}
	if !status.Live || status.Generation != record.Generation || status.VMID != record.VMID || status.Endpoint != record.Endpoint || status.PID <= 0 || status.ProcessIdentity == "" {
		return record, ErrRuntimeIdentityMismatch
	}
	record.RunnerPID = status.PID
	record.ProcessIdentity = status.ProcessIdentity
	return record, nil
}

func (l *Lifecycle) rollback(ctx context.Context, record EnvironmentRecord, prepared *worktree.Prepared, vmAttempted bool, cause error) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var cleanupErr error
	if vmAttempted {
		cleanupErr = errors.Join(cleanupErr, l.deps.VMs.Destroy(cleanupCtx, record))
	}
	if prepared != nil {
		cleanupErr = errors.Join(cleanupErr, l.deps.Worktrees.Cleanup(cleanupCtx, prepared))
	}
	if cleanupErr == nil {
		record.State = EnvironmentDestroyed
	} else {
		record.State = EnvironmentCleanupPending
	}
	registryErr := l.deps.Registry.Save(cleanupCtx, record)
	return errors.Join(cause, cleanupErr, registryErr)
}

func (l *Lifecycle) validateRequest(request CreateRequest) error {
	if !l.configured() {
		return errors.New("microvm lifecycle is not fully configured")
	}
	if !completeCreateRequest(request) {
		return errors.New("microvm lifecycle request is incomplete")
	}
	parentSet := request.ParentRef != (EnvironmentRef{})
	if parentSet != (request.ForkBase != "") {
		return errors.New("microvm child lifecycle requires parent ref and immutable fork base together")
	}
	if parentSet {
		if request.ParentRef.Kind != Kind {
			return errors.New("microvm child parent kind is invalid")
		}
		if _, _, err := parseEnvironmentRef(request.ParentRef); err != nil {
			return fmt.Errorf("microvm child parent ref: %w", err)
		}
	}
	if err := validateLifecycleResources(request.Resources); err != nil {
		return err
	}
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		artifact, ok := request.ArtifactRequests[kind]
		if !ok || artifact.Kind != kind || artifact.Digest == "" {
			return fmt.Errorf("microvm lifecycle request is missing %s", kind)
		}
	}
	return nil
}

func (l *Lifecycle) configured() bool {
	return l != nil && l.deps.Identities != nil && l.deps.Worktrees != nil && l.deps.Artifacts != nil && l.deps.VMs != nil && l.deps.Protocol != nil && l.deps.Registry != nil && l.deps.Sessions != nil
}

func completeCreateRequest(request CreateRequest) bool {
	return request.Owner != "" && request.SessionID != "" && request.Profile != "" && request.Worktree.Source != "" && request.Worktree.WorktreePath != "" && request.Worktree.MetadataPath != "" && request.Worktree.Branch != ""
}

func validateLifecycleResources(resources ResourceUsage) error {
	controlled := resources
	controlled.CPU = 0
	controlled.RAMBytes = 0
	controlled.DiskBytes = 0
	controlled.Inodes = 0
	if controlled != (ResourceUsage{}) {
		return errors.New("microvm lifecycle resources may specify only CPU, RAM, disk, and inodes")
	}
	if err := validateUsage(resources); err != nil {
		return fmt.Errorf("microvm lifecycle resources: %w", err)
	}
	return nil
}

func validatePreparedWorktree(prepared *worktree.Prepared, record EnvironmentRecord) error {
	if prepared == nil || prepared.SourceRoot != record.SourceCheckout || prepared.WorktreePath != record.WorktreePath || prepared.MetadataPath != record.MetadataPath {
		return errors.New("prepared microvm worktree identities do not match the provisional record")
	}
	return nil
}

func validateIdentity(identity EnvironmentIdentity) error {
	if identity.EnvironmentID == "" || strings.TrimSpace(identity.EnvironmentID) != identity.EnvironmentID || identity.VMID == "" || strings.TrimSpace(identity.VMID) != identity.VMID || identity.Generation == 0 || !filepath.IsAbs(identity.Endpoint) {
		return errors.New("microvm identity allocation returned invalid values")
	}
	return nil
}

func validateAgreement(agreement control.Agreement) error {
	if agreement.Version != control.ProtocolVersion || agreement.MaxMessageBytes == 0 || agreement.MaxMessageBytes > control.DefaultMaxMessageBytes {
		return ErrEnvironmentIncompatible
	}
	seen := make(map[control.Capability]bool, len(agreement.Capabilities))
	for _, capability := range agreement.Capabilities {
		seen[capability] = true
	}
	for _, required := range control.RequiredCapabilities() {
		if !seen[required] {
			return ErrEnvironmentIncompatible
		}
	}
	return nil
}

func bootingUsage(resources ResourceUsage) ResourceUsage {
	resources.BootingVMs = 1
	resources.Worktrees = 1
	resources.Boots = 1
	return resources
}

func activeUsage(resources ResourceUsage) ResourceUsage {
	resources.ActiveVMs = 1
	resources.Worktrees = 1
	return resources
}

func requestDigests(requests map[ArtifactKind]ArtifactRequest) map[ArtifactKind]string {
	result := make(map[ArtifactKind]string, len(requests))
	for kind, request := range requests {
		result[kind] = request.Digest
	}
	return result
}

func requestManifestDigests(requests map[ArtifactKind]ArtifactRequest) map[ArtifactKind]string {
	result := make(map[ArtifactKind]string)
	for kind, request := range requests {
		if request.ManifestDigest != "" {
			result[kind] = request.ManifestDigest
		}
	}
	return result
}

func verifiedMatches(want, manifests map[ArtifactKind]string, got VerifiedArtifacts) bool {
	for _, artifact := range got.All() {
		if want[artifact.Kind] != artifact.Digest || manifests[artifact.Kind] != artifact.ManifestDigest {
			return false
		}
	}
	return len(want) == 4
}

func cloneArtifacts(in map[ArtifactKind]string) map[ArtifactKind]string {
	out := make(map[ArtifactKind]string, len(in))
	for kind, digest := range in {
		out[kind] = digest
	}
	return out
}

func cloneArtifactRequests(in map[ArtifactKind]ArtifactRequest) map[ArtifactKind]ArtifactRequest {
	out := make(map[ArtifactKind]ArtifactRequest, len(in))
	for kind, request := range in {
		out[kind] = request
	}
	return out
}

func cloneEnvironmentRecord(record EnvironmentRecord) EnvironmentRecord {
	record.Artifacts = cloneArtifacts(record.Artifacts)
	record.ManifestDigests = cloneArtifacts(record.ManifestDigests)
	record.Agreement.Capabilities = append(control.Capabilities(nil), record.Agreement.Capabilities...)
	return record
}
