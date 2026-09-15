package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

func TestMicroVMEnvironments_Scenario5_HarnessRestartReattachesExactGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	worktree := filepath.Join(dir, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	wantState := []byte("uncommitted state")
	if err := os.WriteFile(filepath.Join(worktree, "state.txt"), wantState, 0o600); err != nil {
		t.Fatal(err)
	}
	record := readyRecord("env-restart", 7)
	record.WorktreePath = worktree
	registry, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(ctx, record); err != nil {
		t.Fatal(err)
	}

	// A new registry and resolver model a restarted harness. Reattachment must not
	// invoke provisioning or any independent per-session engine factory.
	restarted, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}}
	resolved, err := NewReattachingResolver(restarted, runtime).Resolve(ctx, record.Ref, record.Owner)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	gotState, err := os.ReadFile(filepath.Join(resolved.WorktreePath, "state.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotState) != string(wantState) || resolved.Ref != record.Ref || runtime.createCalls != 0 {
		t.Fatalf("restart changed generation/state or provisioned: ref=%+v state=%q creates=%d", resolved.Ref, gotState, runtime.createCalls)
	}
}

func TestMicroVMEnvironments_Scenario5_DaemonRestartNeverRecreatesEmptyEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	record := readyRecord("env-daemon", 11)
	record.AdmissionUsage = ResourceUsage{ActiveVMs: 1, Worktrees: 1, CPU: 2}
	registry, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(ctx, record); err != nil {
		t.Fatal(err)
	}

	// A fresh daemon/backend cannot reopen this exact generation. Startup must
	// identity-check and destroy it rather than minting an empty replacement or
	// retaining quota forever.
	restarted, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &lifecycleRuntime{live: make(map[string]RuntimeStatus), inspectErr: ErrEnvironmentUnavailable}
	admission, err := NewAdmissionController(AdmissionLimits{Deployment: ResourceUsage{ActiveVMs: 1, Worktrees: 1, CPU: 2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := admission.Reconstruct([]EnvironmentRecord{record}); err != nil {
		t.Fatal(err)
	}
	if err := NewReconcilerWithAdmission(restarted, runtime, &lifecycleWorktrees{}, admission).Reconcile(ctx); err != nil {
		t.Fatalf("startup Reconcile() error = %v", err)
	}
	got, err := restarted.Lookup(ctx, record.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != EnvironmentDestroyed || !got.Tombstone || runtime.createCalls != 0 {
		t.Fatalf("restart left unmanaged or replaced generation: record=%+v creates=%d", got, runtime.createCalls)
	}
	lease, err := admission.Acquire(record.Owner, record.AdmissionUsage)
	if err != nil {
		t.Fatalf("destroyed generation retained quota: %v", err)
	}
	lease.Release()
	if _, err := NewReattachingResolver(restarted, runtime).Resolve(ctx, record.Ref, record.Owner); !errors.Is(err, ErrEnvironmentDestroyed) {
		t.Fatalf("Resolve() error = %v, want destroyed", err)
	}
}

func TestMicroVMEnvironments_Scenario5_DetachAndDeleteAreDistinct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			record := readyRecord("env-delete", 3)
			record.AdmissionUsage = ResourceUsage{ActiveVMs: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30}
			registry := newLifecycleRegistry(record)
			runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}}
			worktrees := &lifecycleWorktrees{dirty: dirty}
			observer := NewOperationsObserver(nil)
			observer.ObserveRecord(record)
			observed := observedRegistry{ReconcileRegistry: registry, observer: observer}
			manager := NewEnvironmentManagerWithAdmission(observed, runtime, worktrees, nil, observer)

			if err := manager.Detach(ctx, record.Ref, record.Owner); err != nil {
				t.Fatalf("Detach() error = %v", err)
			}
			got, _ := registry.Lookup(ctx, record.EnvironmentID)
			if got.State != EnvironmentReady || runtime.destroyCalls != 0 || runtime.detachCalls != 1 {
				t.Fatalf("detach altered durable resources: record=%+v runtime=%+v", got, runtime)
			}
			if snapshot := observer.Snapshot(); snapshot.ActiveVMs != 1 || snapshot.Cleanups[OutcomeSuccess] != 0 {
				t.Fatalf("detach altered active or cleanup metrics: %+v", snapshot)
			}

			if err := manager.Delete(ctx, record.Ref, record.Owner, DeleteExplicit); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			got, _ = registry.Lookup(ctx, record.EnvironmentID)
			if got.State != EnvironmentDestroyed || !got.Tombstone || runtime.destroyCalls != 1 {
				t.Fatalf("delete did not durably destroy generation: record=%+v destroys=%d", got, runtime.destroyCalls)
			}
			if snapshot := observer.Snapshot(); snapshot.ActiveVMs != 0 || snapshot.ResourceLimits != (ResourceLimits{}) || snapshot.Cleanups[OutcomeSuccess] != 1 {
				t.Fatalf("delete metrics were not committed exactly once: %+v", snapshot)
			}
			if worktrees.cleaned == dirty {
				t.Fatalf("dirty-state policy mismatch: dirty=%v cleaned=%v", dirty, worktrees.cleaned)
			}
			if _, err := NewReattachingResolver(registry, runtime).Resolve(ctx, record.Ref, record.Owner); !errors.Is(err, ErrEnvironmentDestroyed) {
				t.Fatalf("deleted generation Resolve() error = %v", err)
			}
		})
	}
}

func TestMicroVMEnvironments_Scenario5_ReconcilerConvergesAcrossCrashPoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name         string
		state        EnvironmentState
		vmDeleted    bool
		worktreeDone bool
		wantDestroy  int
	}{
		{name: "provisioning", state: EnvironmentProvisioning, wantDestroy: 1},
		{name: "cleanup-pending", state: EnvironmentCleanupPending, wantDestroy: 1},
		{name: "deleting", state: EnvironmentDeleting, wantDestroy: 1},
		{name: "after-vm-delete", state: EnvironmentDeleting, vmDeleted: true},
		{name: "after-worktree-delete", state: EnvironmentDeleting, worktreeDone: true, wantDestroy: 1},
		{name: "destroyed-tombstone", state: EnvironmentDestroyed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := readyRecord("env-"+tc.name, 5)
			record.State = tc.state
			record.VMDeleted = tc.vmDeleted
			record.WorktreeDeleted = tc.worktreeDone
			record.Tombstone = tc.state != EnvironmentProvisioning && tc.state != EnvironmentReady
			registry := newLifecycleRegistry(record)
			live := make(map[string]RuntimeStatus)
			if !tc.vmDeleted && tc.state != EnvironmentDestroyed {
				live[record.EnvironmentID] = exactRuntimeStatus(record)
			}
			runtime := &lifecycleRuntime{live: live}
			worktrees := &lifecycleWorktrees{}
			reconciler := NewReconciler(registry, runtime, worktrees)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); _ = reconciler.Reconcile(ctx) }()
			}
			wg.Wait()
			got, _ := registry.Lookup(ctx, record.EnvironmentID)
			if got.State != EnvironmentDestroyed || runtime.destroyCalls != tc.wantDestroy {
				t.Fatalf("reconcile did not converge: record=%+v destroys=%d, want %d", got, runtime.destroyCalls, tc.wantDestroy)
			}
		})
	}

	t.Run("two daemons cannot replace or resurrect a generation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		first, err := OpenFileRegistry(path)
		if err != nil {
			t.Fatal(err)
		}
		second, err := OpenFileRegistry(path)
		if err != nil {
			t.Fatal(err)
		}
		record := readyRecord("env-race", 8)
		if err := first.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		tombstone := record
		tombstone.State = EnvironmentDeleting
		tombstone.Tombstone = true
		tombstone.DeleteReason = DeleteExplicit
		if err := first.Save(ctx, tombstone); err != nil {
			t.Fatal(err)
		}
		if err := second.Save(ctx, record); !errors.Is(err, ErrEnvironmentStale) {
			t.Fatalf("stale daemon Save() error = %v, want generation/lifecycle fence", err)
		}
		other := readyRecord("env-race", 9)
		if err := second.Save(ctx, other); !errors.Is(err, ErrEnvironmentStale) {
			t.Fatalf("replacement generation Save() error = %v, want stale", err)
		}
		got, err := first.Lookup(ctx, record.EnvironmentID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Tombstone || got.Generation != record.Generation {
			t.Fatalf("race replaced/resurrected durable generation: %+v", got)
		}
	})

	t.Run("pid reuse and stale socket never delete another generation", func(t *testing.T) {
		record := readyRecord("env-live", 9)
		registry := newLifecycleRegistry(record)
		runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: {
			Live: true, Generation: record.Generation + 1, VMID: "another-vm", PID: 42,
			ProcessIdentity: "reused-pid", Endpoint: record.Endpoint,
		}}}
		if err := NewReconciler(registry, runtime, &lifecycleWorktrees{}).Reconcile(ctx); err == nil {
			t.Fatal("Reconcile() error = nil, want generation-fence failure")
		}
		got, _ := registry.Lookup(ctx, record.EnvironmentID)
		if got.State != EnvironmentCleanupPending || runtime.destroyCalls != 0 {
			t.Fatalf("foreign generation was deleted: record=%+v destroys=%d", got, runtime.destroyCalls)
		}
	})

	t.Run("disk full leaves durable cleanup work", func(t *testing.T) {
		record := readyRecord("env-disk", 4)
		record.State = EnvironmentDeleting
		registry := newLifecycleRegistry(record)
		registry.failSave = errors.New("disk full")
		runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}}
		reconciler := NewReconciler(registry, runtime, &lifecycleWorktrees{})
		if err := reconciler.Reconcile(ctx); err == nil {
			t.Fatal("Reconcile() error = nil, want durable-save failure")
		}
		registry.failSave = nil
		if err := reconciler.Reconcile(ctx); err != nil {
			t.Fatalf("retry Reconcile() error = %v", err)
		}
		got, _ := registry.Lookup(ctx, record.EnvironmentID)
		if got.State != EnvironmentDestroyed {
			t.Fatalf("retry did not converge: %+v", got)
		}
	})
}

func readyRecord(id string, generation uint32) EnvironmentRecord {
	ref := EnvironmentRef{Kind: Kind, ID: id + "@" + strconv.FormatUint(uint64(generation), 10)}
	return EnvironmentRecord{
		State: EnvironmentReady, Owner: "caller:alice", SessionID: "session-1", EnvironmentID: id,
		Ref: ref, Generation: generation, WorktreePath: "/state/" + id, MetadataPath: "/state/meta-" + id,
		VMID: "vm-" + id, Endpoint: "/run/mecatl/" + id + ".sock", RunnerPID: 42, ProcessIdentity: "boot-1:42",
		Agreement: control.Agreement{Version: control.ProtocolVersion, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes},
	}
}

type lifecycleRegistry struct {
	mu       sync.Mutex
	records  map[string]EnvironmentRecord
	failSave error
}

func newLifecycleRegistry(records ...EnvironmentRecord) *lifecycleRegistry {
	r := &lifecycleRegistry{records: make(map[string]EnvironmentRecord)}
	for _, record := range records {
		r.records[record.EnvironmentID] = cloneEnvironmentRecord(record)
	}
	return r
}
func (r *lifecycleRegistry) Save(_ context.Context, record EnvironmentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSave != nil {
		return r.failSave
	}
	r.records[record.EnvironmentID] = cloneEnvironmentRecord(record)
	return nil
}
func (r *lifecycleRegistry) Lookup(_ context.Context, id string) (EnvironmentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return EnvironmentRecord{}, ErrEnvironmentUnknown
	}
	return cloneEnvironmentRecord(record), nil
}
func (r *lifecycleRegistry) List(context.Context) ([]EnvironmentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EnvironmentRecord, 0, len(r.records))
	for _, record := range r.records {
		out = append(out, cloneEnvironmentRecord(record))
	}
	return out, nil
}

type lifecycleRuntime struct {
	mu           sync.Mutex
	live         map[string]RuntimeStatus
	createCalls  int
	detachCalls  int
	destroyCalls int
	inspectErr   error
}

func (r *lifecycleRuntime) Reattach(_ context.Context, record EnvironmentRecord) error {
	status, err := r.Inspect(context.Background(), record)
	if err != nil {
		return err
	}
	return validateRuntimeIdentity(record, status)
}
func (r *lifecycleRuntime) Inspect(_ context.Context, record EnvironmentRecord) (RuntimeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inspectErr != nil {
		return RuntimeStatus{}, r.inspectErr
	}
	status, ok := r.live[record.EnvironmentID]
	if !ok {
		return RuntimeStatus{}, nil
	}
	return status, nil
}
func (r *lifecycleRuntime) Detach(context.Context, EnvironmentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detachCalls++
	return nil
}
func (r *lifecycleRuntime) Destroy(_ context.Context, record EnvironmentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	status, ok := r.live[record.EnvironmentID]
	if !ok {
		return nil
	}
	if err := validateRuntimeIdentity(record, status); err != nil {
		return err
	}
	delete(r.live, record.EnvironmentID)
	r.destroyCalls++
	return nil
}
func exactRuntimeStatus(record EnvironmentRecord) RuntimeStatus {
	return RuntimeStatus{Live: true, Generation: record.Generation, VMID: record.VMID, PID: 42, ProcessIdentity: record.ProcessIdentity, Endpoint: record.Endpoint}
}

type lifecycleWorktrees struct {
	mu             sync.Mutex
	dirty, cleaned bool
}

func (w *lifecycleWorktrees) Dirty(context.Context, EnvironmentRecord) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dirty, nil
}
func (w *lifecycleWorktrees) Cleanup(context.Context, EnvironmentRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleaned = true
	return nil
}
