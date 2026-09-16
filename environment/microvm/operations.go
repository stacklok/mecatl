package microvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"syscall"

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

// OperationsSnapshot is the bounded-cardinality repository runtime metric set.
type OperationsSnapshot struct {
	Execs                 uint64
	EgressDenials         uint64
	ArtifactVerifications map[ArtifactKind]map[Outcome]uint64
	Cleanups              map[Outcome]uint64
}

// OperationsObserver records repository microVM operator facts without retaining
// commands, destinations, paths, bindings, or credentials.
type OperationsObserver struct {
	mu            sync.Mutex
	diag          port.Diagnostics
	snapshot      OperationsSnapshot
	egressSources map[string]observedEgressSource
}

type observedEgressSource struct {
	source egressDenialSource
	last   uint64
}

// NewOperationsObserver constructs repository runtime metrics and diagnostics.
func NewOperationsObserver(diag port.Diagnostics) *OperationsObserver {
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	artifacts := make(map[ArtifactKind]map[Outcome]uint64, 4)
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		artifacts[kind] = map[Outcome]uint64{OutcomeSuccess: 0, OutcomeFailure: 0}
	}
	return &OperationsObserver{
		diag: diag,
		snapshot: OperationsSnapshot{
			ArtifactVerifications: artifacts,
			Cleanups:              map[Outcome]uint64{OutcomeSuccess: 0, OutcomeFailure: 0},
		},
		egressSources: make(map[string]observedEgressSource),
	}
}

// LifecycleRequestFailed retains only closed request metadata and a classified cause.
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
		LifecycleInventory, LifecycleChildDelete:
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
		"disk quota exceeded": "disk quota exceeded", "no space left on device": "insufficient disk space",
		"operation not permitted": "permission denied", "permission denied": "permission denied",
	} {
		if strings.Contains(text, phrase) {
			return detail
		}
	}
	return "backend detail withheld"
}

// ExecFinished records one repository guest execution.
func (o *OperationsObserver) ExecFinished(outcome Outcome, _ string) {
	if o == nil || !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.Execs++
	o.mu.Unlock()
	if outcome == OutcomeFailure {
		o.diag.Log(context.Background(), port.LevelWarn, "microvm exec failed", "outcome", outcome.String())
	}
}

func (o *OperationsObserver) trackEgressDenials(generation string, source egressDenialSource) {
	if o == nil || generation == "" || source == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sampleEgressSource(generation)
	o.egressSources[generation] = observedEgressSource{source: source, last: source.EgressDenials()}
}

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

// ArtifactVerification records a closed-dimension verification outcome.
func (o *OperationsObserver) ArtifactVerification(kind ArtifactKind, outcome Outcome) {
	if o == nil || !validArtifactKind(kind) || !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.ArtifactVerifications[kind][outcome]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), levelFor(outcome), "microvm artifact verification finished", "artifact_kind", string(kind), "outcome", outcome.String())
}

// CleanupFinished records logical attachment cleanup.
func (o *OperationsObserver) CleanupFinished(outcome Outcome) {
	if o == nil || !validOutcome(outcome) {
		return
	}
	o.mu.Lock()
	o.snapshot.Cleanups[outcome]++
	o.mu.Unlock()
	o.diag.Log(context.Background(), levelFor(outcome), "microvm cleanup finished", "outcome", outcome.String())
}

// Snapshot returns a deep-copy operator metric view.
func (o *OperationsObserver) Snapshot() OperationsSnapshot {
	if o == nil {
		return OperationsSnapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for generation := range o.egressSources {
		o.sampleEgressSource(generation)
	}
	result := o.snapshot
	result.ArtifactVerifications = make(map[ArtifactKind]map[Outcome]uint64, len(o.snapshot.ArtifactVerifications))
	for kind, outcomes := range o.snapshot.ArtifactVerifications {
		result.ArtifactVerifications[kind] = map[Outcome]uint64{OutcomeSuccess: outcomes[OutcomeSuccess], OutcomeFailure: outcomes[OutcomeFailure]}
	}
	result.Cleanups = map[Outcome]uint64{OutcomeSuccess: o.snapshot.Cleanups[OutcomeSuccess], OutcomeFailure: o.snapshot.Cleanups[OutcomeFailure]}
	return result
}

func validArtifactKind(kind ArtifactKind) bool {
	switch kind {
	case ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent:
		return true
	default:
		return false
	}
}

func validOutcome(outcome Outcome) bool {
	return outcome == OutcomeSuccess || outcome == OutcomeFailure
}

func levelFor(outcome Outcome) port.Level {
	if outcome == OutcomeSuccess {
		return port.LevelInfo
	}
	return port.LevelWarn
}

// ReadinessCheck names one repository runtime prerequisite.
type ReadinessCheck string

const (
	// CheckHypervisor verifies the host virtualization facility.
	CheckHypervisor ReadinessCheck = "hypervisor"
	// CheckRuntime verifies the runtime artifact.
	CheckRuntime ReadinessCheck = "runtime-artifact"
	// CheckFirmware verifies the firmware artifact.
	CheckFirmware ReadinessCheck = "firmware-artifact"
	// CheckControlSocket verifies the authenticated daemon socket.
	CheckControlSocket ReadinessCheck = "control-socket"
	// CheckNetwork verifies hosted guest networking.
	CheckNetwork ReadinessCheck = "network-provider"
	// CheckProfiles verifies daemon-owned placement aliases.
	CheckProfiles ReadinessCheck = "profiles"
)

// ReadinessStatus is the closed doctor result status.
type ReadinessStatus string

const (
	// ReadinessPass means the prerequisite is ready.
	ReadinessPass ReadinessStatus = "PASS"
	// ReadinessFail means the prerequisite blocks startup.
	ReadinessFail ReadinessStatus = "FAIL"
)

// ReadinessChecker supplies platform and configured-runtime probes.
type ReadinessChecker interface {
	Check(context.Context, ReadinessCheck) error
	Profiles(context.Context) ([]string, error)
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

// Doctor checks whether the repository runtime can accept work.
type Doctor struct{ checker ReadinessChecker }

// NewDoctor constructs an operator readiness path.
func NewDoctor(checker ReadinessChecker) *Doctor { return &Doctor{checker: checker} }

// Run executes every readiness probe without hiding later failures.
func (d *Doctor) Run(ctx context.Context) ReadinessReport {
	if d == nil || d.checker == nil {
		return ReadinessReport{Results: []ReadinessResult{{Check: CheckHypervisor, Status: ReadinessFail, Detail: "doctor is not configured", Remediation: "configure the microVM runtime and rerun doctor"}}}
	}
	results := make([]ReadinessResult, 0, 6)
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
	return ReadinessReport{Results: append(results, profileResult)}
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

// Ready reports whether every prerequisite passed.
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
		return "define at least one operator-owned environment profile with immutable artifacts and guest egress policy"
	default:
		return "inspect the microVM daemon configuration and rerun doctor"
	}
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }
