package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

func TestLifecycleRequestFailureDiagnosticIsClassifiedAndSecretFree(t *testing.T) {
	t.Parallel()
	diagnostics := &captureDiagnostics{}
	observer := NewOperationsObserver(diagnostics)
	const secret = "OPENROUTER_API_KEY=sk-secret /private/state\nforged=value"

	observer.LifecycleRequestFailed(newLifecycleServeError(LifecycleCreate, "failed_precondition",
		fmt.Errorf("prepare owner: operation not permitted: %s", secret)))
	observer.LifecycleRequestFailed(newLifecycleServeError(LifecycleCreate, "failed_precondition",
		repositoryLogicalFailure(repositoryLogicalStagePrepare, errors.New(secret))))
	observer.LifecycleRequestFailed(newLifecycleServeError(LifecycleOperation("create\n"+secret), "code="+secret, errors.New(secret)))

	got := diagnostics.String()
	for _, want := range []string{"microvmd lifecycle request failed", "create", "failed_precondition", "permission denied", "prepare", "unknown", "backend detail withheld"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic %q does not contain %q", got, want)
		}
	}
	for _, forbidden := range []string{secret, "OPENROUTER_API_KEY", "sk-secret", "/private/state", "forged=value"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("diagnostic leaked backend detail %q: %s", forbidden, got)
		}
	}
}

func TestMicroVMEnvironments_Scenario8_ObservabilityIsBoundedAndSecretFree(t *testing.T) {
	t.Parallel()
	diagnostics := &captureDiagnostics{}
	observer := NewOperationsObserver(diagnostics)
	secret := "OPENROUTER_API_KEY=sk-secret command=curl https://private.invalid"

	observer.VMBooting(ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30})
	observer.VMReady(1750*time.Millisecond, ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30})
	observer.ExecFinished(OutcomeSuccess, secret)
	observer.EgressDenied(secret)
	observer.ArtifactVerification(ArtifactRuntime, OutcomeSuccess)
	observer.ArtifactVerification(ArtifactFirmware, OutcomeFailure)
	observer.CleanupFinished(OutcomeSuccess)
	observer.ReconciliationFinished(OutcomeFailure)
	observer.QuotaRejected(QuotaVMs)
	observer.ArtifactVerification(ArtifactKind("session-secret"), OutcomeSuccess)
	observer.QuotaRejected(QuotaKind(255))

	snapshot := observer.Snapshot()
	if snapshot.ActiveVMs != 1 || snapshot.BootingVMs != 0 || snapshot.Execs != 1 || snapshot.EgressDenials != 1 || snapshot.QuotaRejections[QuotaVMs] != 1 {
		t.Fatalf("missing lifecycle metrics: %+v", snapshot)
	}
	if snapshot.ResourceLimits != (ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30}) {
		t.Fatalf("resource gauges = %+v", snapshot.ResourceLimits)
	}
	if snapshot.BootLatency.Count != 1 || snapshot.BootLatency.Sum != 1750*time.Millisecond {
		t.Fatalf("boot latency = %+v", snapshot.BootLatency)
	}
	if len(snapshot.ArtifactVerifications) != 4 || len(snapshot.QuotaRejections) != 10 || len(snapshot.BootLatency.Buckets) != 8 {
		t.Fatalf("metric dimensions are not fixed: %+v", snapshot)
	}
	if snapshot.ArtifactVerifications[ArtifactRuntime][OutcomeSuccess] != 1 || snapshot.ArtifactVerifications[ArtifactFirmware][OutcomeFailure] != 1 || snapshot.Cleanups[OutcomeSuccess] != 1 || snapshot.Reconciliations[OutcomeFailure] != 1 {
		t.Fatalf("missing bounded outcomes: %+v", snapshot)
	}

	exposed := fmt.Sprintf("%+v %s", snapshot, diagnostics.String())
	if strings.Contains(exposed, secret) || strings.Contains(exposed, "sk-secret") || strings.Contains(exposed, "private.invalid") {
		t.Fatalf("observability leaked command or credential: %s", exposed)
	}
	for _, record := range diagnostics.records {
		for i := 0; i < len(record.args); i += 2 {
			key, _ := record.args[i].(string)
			if key != "outcome" && key != "artifact_kind" && key != "quota" {
				t.Fatalf("unbounded diagnostic key %q in %+v", key, record)
			}
		}
	}

	response := (&Daemon{observer: observer}).handleAuthenticated(context.Background(), LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleMetrics,
	})
	if response.Err != nil {
		t.Fatalf("metrics operation: %v", response.Err)
	}
	var exported OperationsSnapshot
	if err := json.Unmarshal(response.Payload, &exported); err != nil {
		t.Fatalf("decode exported metrics: %v", err)
	}
	if exported.Execs != snapshot.Execs || exported.EgressDenials != snapshot.EgressDenials || len(exported.QuotaRejections) != len(snapshot.QuotaRejections) {
		t.Fatalf("daemon metrics export drifted: exported=%+v snapshot=%+v", exported, snapshot)
	}
}

func TestInvariant_microvm_observer_transition_table_is_idempotent(t *testing.T) {
	t.Parallel()
	observer := NewOperationsObserver(nil)
	usage := ResourceUsage{CPU: 2, RAMBytes: 2 << 30, DiskBytes: 20 << 30, BootingVMs: 1, Worktrees: 1}
	record := readyRecord("observed", 1)
	record.State = EnvironmentProvisioning
	record.AdmissionUsage = usage

	observer.ObserveRecord(record)
	observer.ObserveRecord(record)
	assertOperationsGauges(t, observer.Snapshot(), 0, 1, ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30})

	record.State = EnvironmentReady
	record.AdmissionUsage.BootingVMs = 0
	record.AdmissionUsage.ActiveVMs = 1
	observer.ObserveRecord(record)
	observer.ObserveRecord(record)
	assertOperationsGauges(t, observer.Snapshot(), 1, 0, ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30})

	record.State = EnvironmentDeleting
	observer.ObserveRecord(record)
	assertOperationsGauges(t, observer.Snapshot(), 1, 0, ResourceLimits{VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 20 << 30})

	record.State = EnvironmentDestroyed
	observer.ObserveRecord(record)
	observer.ObserveRecord(record)
	assertOperationsGauges(t, observer.Snapshot(), 0, 0, ResourceLimits{})
}

func TestInvariant_microvm_observer_reconstructs_durable_gauges(t *testing.T) {
	t.Parallel()
	observer := NewOperationsObserver(nil)
	ready := readyRecord("ready", 1)
	ready.AdmissionUsage = ResourceUsage{CPU: 4, RAMBytes: 8 << 30, DiskBytes: 40 << 30, ActiveVMs: 1}
	booting := readyRecord("booting", 1)
	booting.State = EnvironmentProvisioning
	booting.AdmissionUsage = ResourceUsage{CPU: 2, RAMBytes: 3 << 30, DiskBytes: 10 << 30, BootingVMs: 1}
	destroyed := readyRecord("destroyed", 1)
	destroyed.State = EnvironmentDestroyed
	destroyed.AdmissionUsage = ResourceUsage{CPU: 99, RAMBytes: 99, DiskBytes: 99, ActiveVMs: 1}

	observer.Reconstruct([]EnvironmentRecord{ready, booting, destroyed})
	observer.Reconstruct([]EnvironmentRecord{ready, booting, destroyed})
	assertOperationsGauges(t, observer.Snapshot(), 1, 1, ResourceLimits{VCPUs: 6, MemoryBytes: 11 << 30, DiskBytes: 50 << 30})
}

func TestInvariant_microvm_production_egress_denials_are_counted(t *testing.T) {
	t.Parallel()
	observer := NewOperationsObserver(nil)
	provider := &fakeNetworkProvider{denials: 3}
	observer.trackEgressDenials("env@1", provider)
	if got := observer.Snapshot().EgressDenials; got != 0 {
		t.Fatalf("initial cumulative provider total counted as new denials: %d", got)
	}
	if got := observer.Snapshot().EgressDenials; got != 0 {
		t.Fatalf("repeated baseline sample changed denials: %d", got)
	}

	provider.denials++
	if got := observer.Snapshot().EgressDenials; got != 1 {
		t.Fatalf("one additional provider denial = %d, want 1", got)
	}
	if got := observer.Snapshot().EgressDenials; got != 1 {
		t.Fatalf("repeated live sample double-counted denial: %d", got)
	}

	observer.untrackEgressDenials("env@1")
	if got := observer.Snapshot().EgressDenials; got != 1 {
		t.Fatalf("final sampling re-added cumulative provider total: %d", got)
	}
}

func TestInvariant_microvm_egress_denial_restart_is_truthful(t *testing.T) {
	t.Parallel()
	provider := &fakeNetworkProvider{denials: 7}
	restarted := NewOperationsObserver(nil)
	restarted.trackEgressDenials("env@1", provider)
	if got := restarted.Snapshot().EgressDenials; got != 0 {
		t.Fatalf("restart fabricated historical process counter = %d", got)
	}
	provider.denials = 9
	if got := restarted.Snapshot().EgressDenials; got != 2 {
		t.Fatalf("post-restart denial delta = %d, want 2", got)
	}
}

func assertOperationsGauges(t *testing.T, snapshot OperationsSnapshot, active, booting int64, resources ResourceLimits) {
	t.Helper()
	if snapshot.ActiveVMs != active || snapshot.BootingVMs != booting || snapshot.ResourceLimits != resources {
		t.Fatalf("gauges = active:%d booting:%d resources:%+v, want active:%d booting:%d resources:%+v", snapshot.ActiveVMs, snapshot.BootingVMs, snapshot.ResourceLimits, active, booting, resources)
	}
}

func TestMicroVMEnvironments_Scenario8_DoctorReportsActionableReadiness(t *testing.T) {
	t.Parallel()
	checker := readinessFixture{
		errors: map[ReadinessCheck]error{
			CheckFirmware: errors.New("firmware signature rejected"),
			CheckNetwork:  errors.New("hosted network unavailable"),
		},
		profiles: []string{"locked-down", "build"},
		stale:    2,
	}
	report := NewDoctor(checker).Run(context.Background())

	for _, name := range []ReadinessCheck{CheckHypervisor, CheckRuntime, CheckFirmware, CheckControlSocket, CheckNetwork, CheckProfiles, CheckStaleResources} {
		result, ok := report.Result(name)
		if !ok {
			t.Fatalf("doctor omitted %s: %+v", name, report)
		}
		if result.Remediation == "" {
			t.Fatalf("doctor result %s has no actionable remediation", name)
		}
	}
	if report.Ready() {
		t.Fatal("doctor reported ready despite failed firmware and network checks")
	}
	if result, _ := report.Result(CheckProfiles); result.Status != ReadinessPass || !strings.Contains(result.Detail, "2 profiles available") {
		t.Fatalf("profile readiness = %+v", result)
	}
	if result, _ := report.Result(CheckStaleResources); result.Status != ReadinessWarn || !strings.Contains(result.Detail, "2 stale resources") {
		t.Fatalf("stale-resource readiness = %+v", result)
	}
	if text := report.String(); !strings.Contains(text, "verify the pinned firmware digest") || !strings.Contains(text, "run reconciliation") {
		t.Fatalf("doctor output is not actionable:\n%s", text)
	}
}

type readinessFixture struct {
	errors   map[ReadinessCheck]error
	profiles []string
	stale    int
}

func (f readinessFixture) Check(_ context.Context, check ReadinessCheck) error {
	return f.errors[check]
}
func (f readinessFixture) Profiles(context.Context) ([]string, error)  { return f.profiles, nil }
func (f readinessFixture) StaleResources(context.Context) (int, error) { return f.stale, nil }

type diagnosticRecord struct {
	level port.Level
	msg   string
	args  []any
}

type captureDiagnostics struct {
	mu      sync.Mutex
	records []diagnosticRecord
}

func (d *captureDiagnostics) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, diagnosticRecord{level: level, msg: msg, args: append([]any(nil), args...)})
}

func (d *captureDiagnostics) With(...any) port.Diagnostics { return d }

func (d *captureDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprintf("%+v", d.records)
}
