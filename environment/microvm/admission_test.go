package microvm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMicroVMEnvironments_Scenario6_ResourceAdmissionIsBounded(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	limits := AdmissionLimits{
		PerUser:            ResourceUsage{BootingVMs: 1, ActiveVMs: 1, Worktrees: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000, Execs: 1, Forks: 1, Pulls: 1},
		Deployment:         ResourceUsage{BootingVMs: 2, ActiveVMs: 2, Worktrees: 2, CPU: 4, RAMBytes: 8 << 30, DiskBytes: 40 << 30, Inodes: 2000, Execs: 2, Forks: 2, Pulls: 2},
		PerUserBootRate:    RateLimit{Count: 1, Window: time.Minute},
		DeploymentBootRate: RateLimit{Count: 2, Window: time.Minute},
	}
	admission, err := NewAdmissionController(limits, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewAdmissionController: %v", err)
	}

	vm := ResourceUsage{BootingVMs: 1, Worktrees: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000, Boots: 1}
	lease, err := admission.Acquire("alice", vm)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	_, err = admission.Acquire("alice", vm)
	var rejected *AdmissionError
	if !errors.As(err, &rejected) || rejected.Scope != AdmissionScopeUser || rejected.Resource != ResourceBootingVMs || !strings.Contains(err.Error(), "alice") || !strings.Contains(err.Error(), "limit=1") {
		t.Fatalf("same-user rejection = %v, want stable actionable booting-vm quota", err)
	}
	if err := lease.Replace(ResourceUsage{ActiveVMs: 1, Worktrees: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000}); err != nil {
		t.Fatalf("mark active: %v", err)
	}
	bobVM, err := admission.Acquire("bob", vm)
	if err != nil {
		t.Fatalf("second user admission: %v", err)
	}
	if err := bobVM.Replace(ResourceUsage{ActiveVMs: 1, Worktrees: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000}); err != nil {
		t.Fatalf("mark second user active: %v", err)
	}
	_, err = admission.Acquire("carol", ResourceUsage{CPU: 1})
	if !errors.As(err, &rejected) || rejected.Scope != AdmissionScopeDeployment || rejected.Resource != ResourceCPU {
		t.Fatalf("deployment CPU rejection = %v", err)
	}
	_, err = admission.Acquire("carol", ResourceUsage{BootingVMs: 1, Boots: 1})
	if !errors.As(err, &rejected) || rejected.Scope != AdmissionScopeDeployment || rejected.Resource != ResourceBootRate {
		t.Fatalf("deployment boot-rate rejection = %v", err)
	}
	bobVM.Release()

	for name, usage := range map[string]ResourceUsage{
		"cpu": {CPU: 2}, "ram": {RAMBytes: 4 << 30}, "disk": {DiskBytes: 20 << 30}, "inodes": {Inodes: 1000},
		"execs": {Execs: 1}, "forks": {Forks: 1}, "pulls": {Pulls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			first, acquireErr := admission.Acquire("bob", usage)
			if acquireErr != nil {
				t.Fatalf("first acquire: %v", acquireErr)
			}
			defer first.Release()
			if _, acquireErr = admission.Acquire("bob", usage); acquireErr == nil {
				t.Fatal("second acquire succeeded beyond per-user limit")
			}
		})
	}

	fx := newLifecycleFixture("success")
	fx.request.Owner = "alice"
	fx.request.Resources = ResourceUsage{CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000}
	lifecycle := NewLifecycle(LifecycleDeps{Identities: fx.identities, Worktrees: fx.worktrees, Artifacts: fx.artifacts, VMs: fx.vms, Protocol: fx.protocol, Registry: fx.registry, Sessions: fx.sessions, Admission: admission})
	if _, createErr := lifecycle.Create(context.Background(), fx.request); createErr == nil || !strings.Contains(createErr.Error(), "worktrees") {
		t.Fatalf("lifecycle admission error = %v, want worktree quota rejection", createErr)
	}
	if len(fx.registry.records) != 0 {
		t.Fatalf("rejected lifecycle create crossed provisioning boundary: %+v", fx.registry.records)
	}

	lease.Release()
	lease.Release() // idempotent accounting
	if _, err := admission.Acquire("alice", vm); err == nil || !strings.Contains(err.Error(), "boot-rate") {
		t.Fatalf("boot-rate rejection = %v, want actionable rate limit", err)
	}
	now = now.Add(time.Minute)
	if next, err := admission.Acquire("alice", vm); err != nil {
		t.Fatalf("admission after rate window: %v", err)
	} else {
		next.Release()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := admission.AcquireContext(ctx, "alice", ResourceUsage{Execs: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireContext error = %v, want context cancellation", err)
	}

	restarted, err := NewAdmissionController(limits, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewAdmissionController(restart): %v", err)
	}
	retained := ResourceUsage{ActiveVMs: 1, Worktrees: 1, CPU: 2, RAMBytes: 4 << 30, DiskBytes: 20 << 30, Inodes: 1000}
	if err := restarted.Reconstruct([]EnvironmentRecord{{State: EnvironmentReady, Owner: "alice", AdmissionUsage: retained}}); err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if _, err := restarted.Acquire("alice", ResourceUsage{CPU: 1}); err == nil {
		t.Fatal("restart reconstruction discarded the durable active reservation")
	}
	restarted.Release("alice", retained)
	if lease, err := restarted.Acquire("alice", ResourceUsage{CPU: 1}); err != nil {
		t.Fatalf("admission remained reserved after durable destruction: %v", err)
	} else {
		lease.Release()
	}
}
