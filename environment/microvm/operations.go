package microvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// Outcome is the closed metric outcome dimension.
type Outcome uint8

const (
	// OutcomeSuccess records completed operational work.
	OutcomeSuccess Outcome = iota + 1
	// OutcomeFailure records failed operational work.
	OutcomeFailure
)

func (o Outcome) String() string {
	if o == OutcomeSuccess {
		return "success"
	}
	return "failure"
}

// QuotaKind is the closed quota-rejection dimension.
type QuotaKind uint8

const (
	// QuotaVMs limits concurrent active and booting VMs.
	QuotaVMs QuotaKind = iota + 1
	// QuotaVCPUs limits aggregate virtual CPUs.
	QuotaVCPUs
	// QuotaMemory limits aggregate guest memory.
	QuotaMemory
	// QuotaDisk limits aggregate guest disk.
	QuotaDisk
	// QuotaExecs limits concurrent guest execs.
	QuotaExecs
	// QuotaWorktrees limits prepared worktrees.
	QuotaWorktrees
	// QuotaInodes limits aggregate filesystem entries.
	QuotaInodes
	// QuotaForks limits concurrent child creation.
	QuotaForks
	// QuotaPulls limits concurrent artifact pulls.
	QuotaPulls
	// QuotaBootRate limits VM starts in the configured rolling window.
	QuotaBootRate
)

func (q QuotaKind) String() string {
	switch q {
	case QuotaVMs:
		return "vms"
	case QuotaVCPUs:
		return "vcpus"
	case QuotaMemory:
		return "memory"
	case QuotaDisk:
		return "disk"
	case QuotaExecs:
		return "execs"
	case QuotaWorktrees:
		return "worktrees"
	case QuotaInodes:
		return "inodes"
	case QuotaForks:
		return "forks"
	case QuotaPulls:
		return "pulls"
	case QuotaBootRate:
		return "boot-rate"
	default:
		return "unknown"
	}
}

// ResourceLimits is the aggregate bounded resource gauge for live and booting VMs.
type ResourceLimits struct {
	VCPUs       int64
	MemoryBytes int64
	DiskBytes   int64
}

// DurationMetric is a fixed-bucket duration summary. Buckets are cumulative at
// 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s, and 30s; the final count includes overflow.
type DurationMetric struct {
	Count   uint64
	Sum     time.Duration
	Buckets [8]uint64
}

// OperationsSnapshot is a point-in-time, bounded-cardinality runtime metric set.
// Its maps are populated only for closed enum dimensions.
type OperationsSnapshot struct {
	BootLatency           DurationMetric
	ActiveVMs             int64
	BootingVMs            int64
	ResourceLimits        ResourceLimits
	Execs                 uint64
	EgressDenials         uint64
	ArtifactVerifications map[ArtifactKind]map[Outcome]uint64
	Cleanups              map[Outcome]uint64
	Reconciliations       map[Outcome]uint64
	QuotaRejections       map[QuotaKind]uint64
}

// OperationsObserver records microVM operator facts. Command text and denied
// destinations are accepted at the producer seam only to make their deliberate
// exclusion explicit; they are never retained, labelled, or logged.
type OperationsObserver struct {
	mu            sync.Mutex
	diag          port.Diagnostics
	snapshot      OperationsSnapshot
	records       map[string]observedEnvironment
	egressSources map[string]observedEgressSource
}

type observedEgressSource struct {
	source egressDenialSource
	last   uint64
}

type observedEnvironment struct {
	active    bool
	booting   bool
	resources ResourceLimits
}

// NewOperationsObserver constructs an in-memory metrics source and diagnostics emitter.
func NewOperationsObserver(diag port.Diagnostics) *OperationsObserver {
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &OperationsObserver{
		diag: diag, snapshot: newOperationsSnapshot(), records: make(map[string]observedEnvironment),
		egressSources: make(map[string]observedEgressSource),
	}
}

func newOperationsSnapshot() OperationsSnapshot {
	artifacts := make(map[ArtifactKind]map[Outcome]uint64, 4)
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		artifacts[kind] = map[Outcome]uint64{OutcomeSuccess: 0, OutcomeFailure: 0}
	}
	return OperationsSnapshot{
		ArtifactVerifications: artifacts,
		Cleanups:              map[Outcome]uint64{OutcomeSuccess: 0, OutcomeFailure: 0},
		Reconciliations:       map[Outcome]uint64{OutcomeSuccess: 0, OutcomeFailure: 0},
		QuotaRejections: map[QuotaKind]uint64{
			QuotaVMs: 0, QuotaVCPUs: 0, QuotaMemory: 0, QuotaDisk: 0, QuotaExecs: 0,
			QuotaWorktrees: 0, QuotaInodes: 0, QuotaForks: 0, QuotaPulls: 0, QuotaBootRate: 0,
		},
	}
}

// ObserveRecord projects one durable lifecycle generation into process gauges.
// Repeated saves of the same state are idempotent.
func (o *OperationsObserver) ObserveRecord(record EnvironmentRecord) {
	if o == nil || record.Ref.ID == "" {
		return
	}
	next := observedFromRecord(record)
	o.mu.Lock()
	if previous, ok := o.records[record.Ref.ID]; ok {
		o.removeObserved(previous)
	}
	if next.active || next.booting {
		o.records[record.Ref.ID] = next
		o.addObserved(next)
	} else {
		delete(o.records, record.Ref.ID)
	}
	o.mu.Unlock()
}

// Reconstruct replaces process gauges from the authoritative durable registry.
// Operational counters remain process-local and are not reconstructed.
func (o *OperationsObserver) Reconstruct(records []EnvironmentRecord) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.snapshot.ActiveVMs = 0
	o.snapshot.BootingVMs = 0
	o.snapshot.ResourceLimits = ResourceLimits{}
	o.records = make(map[string]observedEnvironment, len(records))
	for _, record := range records {
		next := observedFromRecord(record)
		if record.Ref.ID == "" || (!next.active && !next.booting) {
			continue
		}
		o.records[record.Ref.ID] = next
		o.addObserved(next)
	}
	o.mu.Unlock()
}

func observedFromRecord(record EnvironmentRecord) observedEnvironment {
	observed := observedEnvironment{resources: ResourceLimits{
		VCPUs: record.AdmissionUsage.CPU, MemoryBytes: record.AdmissionUsage.RAMBytes, DiskBytes: record.AdmissionUsage.DiskBytes,
	}}
	switch record.State {
	case EnvironmentReady, EnvironmentDeleting:
		observed.active = true
	case EnvironmentProvisioning:
		observed.booting = true
	case EnvironmentCleanupPending:
		observed.active = record.AdmissionUsage.ActiveVMs > 0
		observed.booting = !observed.active && record.AdmissionUsage.BootingVMs > 0
	}
	return observed
}

func (o *OperationsObserver) addObserved(observed observedEnvironment) {
	if observed.active {
		o.snapshot.ActiveVMs++
	}
	if observed.booting {
		o.snapshot.BootingVMs++
	}
	o.addResources(observed.resources)
}

func (o *OperationsObserver) removeObserved(observed observedEnvironment) {
	if observed.active {
		o.snapshot.ActiveVMs = max(o.snapshot.ActiveVMs-1, 0)
	}
	if observed.booting {
		o.snapshot.BootingVMs = max(o.snapshot.BootingVMs-1, 0)
	}
	o.subtractResources(observed.resources)
}

// LifecycleRequestFailed retains only closed request metadata and a classified
// cause. Lifecycle errors may contain paths, bindings, or credentials.
func (o *OperationsObserver) LifecycleRequestFailed(err error) {
	if o == nil || err == nil {
		return
	}
	operation, code := LifecycleOperation("connection"), "failed_precondition"
	var serveErr *lifecycleServeError
	if errors.As(err, &serveErr) {
		operation, code = serveErr.operation, serveErr.code
	}
	args := []any{"operation", safeLifecycleOperation(operation), "code", safeLifecycleErrorCode(code), "detail", safeLifecycleErrorDetail(err)}
	var logicalErr *repositoryLogicalStageError
	if errors.As(err, &logicalErr) {
		args = append(args, "stage", safeRepositoryLogicalStage(logicalErr.stage))
	}
	o.diag.Log(context.Background(), port.LevelWarn, "microvmd lifecycle request failed", args...)
}

func safeRepositoryLogicalStage(stage repositoryLogicalStage) string {
	switch stage {
	case repositoryLogicalStageEnsure, repositoryLogicalStageIdentity, repositoryLogicalStageAllocate,
		repositoryLogicalStageReserve, repositoryLogicalStagePrepare, repositoryLogicalStageRegister:
		return string(stage)
	default:
		return "invalid"
	}
}

func safeLifecycleOperation(operation LifecycleOperation) string {
	switch operation {
	case "connection", LifecycleInfo, LifecycleCreate, LifecycleResolve, LifecycleInspect, LifecycleDetach,
		LifecycleDelete, LifecycleWorkspace, LifecycleExec, LifecycleFork, LifecycleMerge, LifecycleMetrics,
		LifecycleInventory, LifecycleReconcile, LifecycleChildDelete:
		return string(operation)
	default:
		return "unknown"
	}
}

func safeLifecycleErrorCode(code string) string {
	switch code {
	case "unauthenticated", "binding_mismatch", "not_found", "destroyed", "unavailable", "repository_logical_root_unavailable", "failed_precondition", "transport":
		return code
	default:
		return "unknown"
	}
}

func safeLifecycleErrorDetail(err error) string {
	switch {
	case errors.Is(err, syscall.EDQUOT):
		return "disk quota exceeded"
	case errors.Is(err, syscall.ENOSPC):
		return "insufficient disk space"
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EPERM):
		return "permission denied"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, context.Canceled):
		return "operation cancelled"
	}
	text := strings.ToLower(err.Error())
	for phrase, detail := range map[string]string{
		"disk quota exceeded":     "disk quota exceeded",
		"no space left on device": "insufficient disk space",
		"operation not permitted": "permission denied",
		"permission denied":       "permission denied",
	} {
		if strings.Contains(text, phrase) {
			return detail
		}
	}
	return "backend detail withheld"
}

// VMBooting records one generation entering the bounded boot set.
func (o *OperationsObserver) VMBooting(resources ResourceLimits) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshot.BootingVMs++
	o.addResources(resources)
}

// BootFinished observes boot latency without changing durable-state gauges.
func (o *OperationsObserver) BootFinished(latency time.Duration) {
	o.mu.Lock()
	o.snapshot.BootLatency.observe(latency)
	o.mu.Unlock()
	o.diag.Log(context.Background(), port.LevelInfo, "microvm became ready")
}

// VMReady moves one generation from booting to active and observes boot latency.
func (o *OperationsObserver) VMReady(latency time.Duration, _ ResourceLimits) {
	o.mu.Lock()
	if o.snapshot.BootingVMs > 0 {
		o.snapshot.BootingVMs--
	}
	o.snapshot.ActiveVMs++
	o.snapshot.BootLatency.observe(latency)
	o.mu.Unlock()
	o.diag.Log(context.Background(), port.LevelInfo, "microvm became ready")
}

// VMStopped removes a generation and its resources from the active gauges.
func (o *OperationsObserver) VMStopped(resources ResourceLimits) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.snapshot.ActiveVMs > 0 {
		o.snapshot.ActiveVMs--
	} else if o.snapshot.BootingVMs > 0 {
		o.snapshot.BootingVMs--
	}
	o.subtractResources(resources)
}

// ExecFinished counts a guest exec without retaining command content.
func (o *OperationsObserver) ExecFinished(outcome Outcome, _ string) {
	if !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.Execs++
	o.mu.Unlock()
	if outcome == OutcomeFailure {
		o.diag.Log(context.Background(), port.LevelWarn, "microvm exec failed", "outcome", outcome.String())
	}
}

// trackEgressDenials adds a live cumulative denial source. Its current total is
// the baseline, so a daemon restart does not fabricate historical process metrics.
func (o *OperationsObserver) trackEgressDenials(generation string, source egressDenialSource) {
	if o == nil || generation == "" || source == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sampleEgressSource(generation)
	o.egressSources[generation] = observedEgressSource{source: source, last: source.EgressDenials()}
}

// untrackEgressDenials records the final delta and removes a generation's source.
func (o *OperationsObserver) untrackEgressDenials(generation string) {
	if o == nil || generation == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sampleEgressSource(generation)
	delete(o.egressSources, generation)
}

func (o *OperationsObserver) sampleEgressSource(generation string) {
	tracked, ok := o.egressSources[generation]
	if !ok {
		return
	}
	current := tracked.source.EgressDenials()
	if current >= tracked.last {
		o.snapshot.EgressDenials += current - tracked.last
	}
	tracked.last = current
	o.egressSources[generation] = tracked
}

func (o *OperationsObserver) sampleEgressSources() {
	for generation := range o.egressSources {
		o.sampleEgressSource(generation)
	}
}

// EgressDenials counts denied guest packets without retaining destinations.
func (o *OperationsObserver) EgressDenials(count uint64) {
	if o == nil || count == 0 {
		return
	}
	o.mu.Lock()
	o.snapshot.EgressDenials += count
	o.mu.Unlock()
	o.diag.Log(context.Background(), port.LevelWarn, "microvm guest egress denied")
}

// EgressDenied counts a guest-network denial without retaining its destination.
func (o *OperationsObserver) EgressDenied(_ string) {
	o.EgressDenials(1)
}

// ArtifactVerification records verification by the closed artifact-kind dimension.
func (o *OperationsObserver) ArtifactVerification(kind ArtifactKind, outcome Outcome) {
	if !validArtifactKind(kind) || !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.ArtifactVerifications[kind][outcome]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), levelFor(outcome), "microvm artifact verification finished", "artifact_kind", string(kind), "outcome", outcome.String())
}

// DetachFinished reports process-local data-plane release; durable VM gauges remain unchanged.
func (o *OperationsObserver) DetachFinished(outcome Outcome) {
	if o == nil || !validOutcome(outcome) {
		return
	}
	o.diag.Log(context.Background(), levelFor(outcome), "microvm detach finished", "outcome", outcome.String())
}

// CleanupFinished records lifecycle cleanup.
func (o *OperationsObserver) CleanupFinished(outcome Outcome) {
	if !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.Cleanups[outcome]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), levelFor(outcome), "microvm cleanup finished", "outcome", outcome.String())
}

// ReconciliationFinished records one reconciliation pass.
func (o *OperationsObserver) ReconciliationFinished(outcome Outcome) {
	if !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.Reconciliations[outcome]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), levelFor(outcome), "microvm reconciliation finished", "outcome", outcome.String())
}

// QuotaRejected records a rejection by the closed resource quota dimension.
func (o *OperationsObserver) QuotaRejected(quota QuotaKind) {
	if quota.String() == "unknown" {
		return
	}
	o.mu.Lock()
	o.snapshot.QuotaRejections[quota]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), port.LevelWarn, "microvm quota rejected request", "quota", quota.String())
}

// Snapshot returns a deep copy suitable for a metrics exporter.
func (o *OperationsObserver) Snapshot() OperationsSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sampleEgressSources()
	result := o.snapshot
	result.ArtifactVerifications = make(map[ArtifactKind]map[Outcome]uint64, len(o.snapshot.ArtifactVerifications))
	for kind, outcomes := range o.snapshot.ArtifactVerifications {
		result.ArtifactVerifications[kind] = map[Outcome]uint64{OutcomeSuccess: outcomes[OutcomeSuccess], OutcomeFailure: outcomes[OutcomeFailure]}
	}
	result.Cleanups = map[Outcome]uint64{OutcomeSuccess: o.snapshot.Cleanups[OutcomeSuccess], OutcomeFailure: o.snapshot.Cleanups[OutcomeFailure]}
	result.Reconciliations = map[Outcome]uint64{OutcomeSuccess: o.snapshot.Reconciliations[OutcomeSuccess], OutcomeFailure: o.snapshot.Reconciliations[OutcomeFailure]}
	result.QuotaRejections = make(map[QuotaKind]uint64, len(o.snapshot.QuotaRejections))
	for quota, count := range o.snapshot.QuotaRejections {
		result.QuotaRejections[quota] = count
	}
	return result
}

func (o *OperationsObserver) addResources(resources ResourceLimits) {
	o.snapshot.ResourceLimits.VCPUs += max(resources.VCPUs, 0)
	o.snapshot.ResourceLimits.MemoryBytes += max(resources.MemoryBytes, 0)
	o.snapshot.ResourceLimits.DiskBytes += max(resources.DiskBytes, 0)
}

func (o *OperationsObserver) subtractResources(resources ResourceLimits) {
	o.snapshot.ResourceLimits.VCPUs = max(o.snapshot.ResourceLimits.VCPUs-max(resources.VCPUs, 0), 0)
	o.snapshot.ResourceLimits.MemoryBytes = max(o.snapshot.ResourceLimits.MemoryBytes-max(resources.MemoryBytes, 0), 0)
	o.snapshot.ResourceLimits.DiskBytes = max(o.snapshot.ResourceLimits.DiskBytes-max(resources.DiskBytes, 0), 0)
}

func (d *DurationMetric) observe(value time.Duration) {
	if value < 0 {
		value = 0
	}
	d.Count++
	d.Sum += value
	for i, bound := range [...]time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2500 * time.Millisecond, 5 * time.Second, 10 * time.Second, 30 * time.Second} {
		if value <= bound {
			d.Buckets[i]++
		}
	}
}

func validOutcome(outcome Outcome) bool {
	return outcome == OutcomeSuccess || outcome == OutcomeFailure
}

func validArtifactKind(kind ArtifactKind) bool {
	return kind == ArtifactRuntime || kind == ArtifactFirmware || kind == ArtifactExecutionImage || kind == ArtifactGuestAgent
}

func levelFor(outcome Outcome) port.Level {
	if outcome == OutcomeSuccess {
		return port.LevelInfo
	}
	return port.LevelWarn
}

// ReadinessCheck is the closed doctor check set.
type ReadinessCheck string

// Doctor readiness checks cover every prerequisite and stale-resource condition.
const (
	CheckHypervisor     ReadinessCheck = "hypervisor"
	CheckRuntime        ReadinessCheck = "runtime-artifact"
	CheckFirmware       ReadinessCheck = "firmware-artifact"
	CheckControlSocket  ReadinessCheck = "control-socket"
	CheckNetwork        ReadinessCheck = "network-provider"
	CheckProfiles       ReadinessCheck = "profiles"
	CheckStaleResources ReadinessCheck = "stale-resources"
)

// ReadinessStatus is the closed doctor result status.
type ReadinessStatus string

// Readiness result statuses are a closed operator-facing vocabulary.
const (
	ReadinessPass ReadinessStatus = "PASS"
	ReadinessWarn ReadinessStatus = "WARN"
	ReadinessFail ReadinessStatus = "FAIL"
)

// ReadinessChecker supplies platform and configured-runtime probes to Doctor.
type ReadinessChecker interface {
	Check(context.Context, ReadinessCheck) error
	Profiles(context.Context) ([]string, error)
	StaleResources(context.Context) (int, error)
}

// ReadinessResult is one actionable doctor finding.
type ReadinessResult struct {
	Check       ReadinessCheck
	Status      ReadinessStatus
	Detail      string
	Remediation string
}

// ReadinessReport is the stable ordered doctor output.
type ReadinessReport struct{ Results []ReadinessResult }

// Doctor checks whether the configured microVM runtime can safely accept work.
type Doctor struct{ checker ReadinessChecker }

// NewDoctor constructs an operator readiness path over platform-specific probes.
func NewDoctor(checker ReadinessChecker) *Doctor { return &Doctor{checker: checker} }

// Run executes every readiness probe; one failure never hides later findings.
func (d *Doctor) Run(ctx context.Context) ReadinessReport {
	if d == nil || d.checker == nil {
		return ReadinessReport{Results: []ReadinessResult{{Check: CheckHypervisor, Status: ReadinessFail, Detail: "doctor is not configured", Remediation: "configure the microVM runtime and rerun doctor"}}}
	}
	results := make([]ReadinessResult, 0, 7)
	for _, check := range []ReadinessCheck{CheckHypervisor, CheckRuntime, CheckFirmware, CheckControlSocket, CheckNetwork} {
		result := ReadinessResult{Check: check, Status: ReadinessPass, Detail: "ready", Remediation: remediation(check)}
		if err := d.checker.Check(ctx, check); err != nil {
			result.Status, result.Detail = ReadinessFail, oneLine(err.Error())
		}
		results = append(results, result)
	}
	profiles, err := d.checker.Profiles(ctx)
	profileResult := ReadinessResult{Check: CheckProfiles, Status: ReadinessPass, Remediation: remediation(CheckProfiles)}
	if err != nil {
		profileResult.Status, profileResult.Detail = ReadinessFail, oneLine(err.Error())
	} else if len(profiles) == 0 {
		profileResult.Status, profileResult.Detail = ReadinessFail, "no environment profiles available"
	} else {
		profileResult.Detail = fmt.Sprintf("%d profiles available", len(profiles))
	}
	results = append(results, profileResult)
	stale, err := d.checker.StaleResources(ctx)
	staleResult := ReadinessResult{Check: CheckStaleResources, Status: ReadinessPass, Detail: "no stale resources", Remediation: remediation(CheckStaleResources)}
	if err != nil {
		staleResult.Status, staleResult.Detail = ReadinessFail, oneLine(err.Error())
	} else if stale > 0 {
		staleResult.Status, staleResult.Detail = ReadinessWarn, fmt.Sprintf("%d stale resources require reconciliation", stale)
	}
	results = append(results, staleResult)
	return ReadinessReport{Results: results}
}

// Result returns the named doctor result.
func (r ReadinessReport) Result(check ReadinessCheck) (ReadinessResult, bool) {
	for _, result := range r.Results {
		if result.Check == check {
			return result, true
		}
	}
	return ReadinessResult{}, false
}

// Ready reports whether every mandatory check passed; stale-resource warnings do not block readiness.
func (r ReadinessReport) Ready() bool {
	if len(r.Results) == 0 {
		return false
	}
	for _, result := range r.Results {
		if result.Status == ReadinessFail {
			return false
		}
	}
	return true
}

// String renders stable line-oriented operator output.
func (r ReadinessReport) String() string {
	var output strings.Builder
	for _, result := range r.Results {
		_, _ = fmt.Fprintf(&output, "%s %-18s %s; remediation: %s\n", result.Status, result.Check, result.Detail, result.Remediation)
	}
	return output.String()
}

func remediation(check ReadinessCheck) string {
	switch check {
	case CheckHypervisor:
		return "grant this account access to /dev/kvm on Linux or enable Hypervisor.framework on macOS, then rerun doctor"
	case CheckRuntime:
		return "install the pinned runtime artifact and verify its configured digest, signature, and attestation"
	case CheckFirmware:
		return "verify the pinned firmware digest, signature, attestation, and policy revision; replace stale cache entries"
	case CheckControlSocket:
		return "start mecatl-microvmd as the same local account and correct the private socket owner and mode"
	case CheckNetwork:
		return "install and configure the selected hosted network provider and confirm guest IPv6 can be disabled"
	case CheckProfiles:
		return "define at least one operator-owned environment profile with immutable artifacts, quotas, and guest egress policy"
	case CheckStaleResources:
		return "run reconciliation, inspect retained dirty worktrees, and retry failed deletion checkpoints"
	default:
		return "inspect the microVM daemon configuration and rerun doctor"
	}
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }
