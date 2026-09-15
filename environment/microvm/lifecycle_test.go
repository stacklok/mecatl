package microvm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestMicroVMEnvironments_Scenario5_CreateTransactionIsAllOrReconciled(t *testing.T) {
	t.Parallel()

	stages := []string{"success", "prepare", "verify", "create-vm", "ready", "negotiate", "persist"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fx := newLifecycleFixture(stage)
			lifecycle := NewLifecycle(LifecycleDeps{
				Identities: fx.identities,
				Worktrees:  fx.worktrees,
				Artifacts:  fx.artifacts,
				VMs:        fx.vms,
				Protocol:   fx.protocol,
				Registry:   fx.registry,
				Sessions:   fx.sessions,
			})

			created, err := lifecycle.Create(context.Background(), fx.request)
			if stage != "success" {
				if err == nil {
					t.Fatal("Create() error = nil, want injected failure")
				}
				if created != (CreatedEnvironment{}) {
					t.Fatalf("Create() result = %+v, want zero on failure", created)
				}
				last := fx.registry.last(t)
				if last.State != EnvironmentDestroyed && last.State != EnvironmentCleanupPending {
					t.Fatalf("registry state = %q, want destroyed or cleanup-pending", last.State)
				}
				if last.EnvironmentID != "env-1" || last.VMID != "vm-1" || last.Endpoint != "/run/mecatl/env-1.sock" || last.WorktreePath != "/state/worktrees/session-1" {
					t.Fatalf("reconciliation record lost provisional identities: %+v", last)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			persisted := fx.sessions.saved
			record := fx.registry.last(t)
			if record.State != EnvironmentReady {
				t.Fatalf("registry state = %q, want ready", record.State)
			}
			if created.Ref != record.Ref || persisted.Ref != record.Ref || created.Generation != record.Generation || persisted.Generation != record.Generation {
				t.Fatalf("ref/generation disagree: created=%+v persisted=%+v registry=%+v", created, persisted, record)
			}
			if record.Owner != fx.request.Owner || record.Profile != fx.request.Profile || persisted.Owner != record.Owner || persisted.Profile != record.Profile {
				t.Fatalf("owner/profile disagree: persisted=%+v registry=%+v", persisted, record)
			}
			if persisted.WorktreePath != record.WorktreePath || created.HostWorktree != record.WorktreePath || created.GuestRoot != worktree.GuestWorkspace {
				t.Fatalf("path roles disagree: created=%+v persisted=%+v registry=%+v", created, persisted, record)
			}
			if fx.vms.created.VMID != record.VMID || fx.vms.created.Endpoint != record.Endpoint || fx.protocol.endpoint != record.Endpoint {
				t.Fatalf("VM/endpoint disagree: vm=%+v protocol=%q registry=%+v", fx.vms.created, fx.protocol.endpoint, record)
			}
			if record.ProcessIdentity == "" {
				t.Fatal("ready record omitted process-start identity needed for PID-reuse fencing")
			}
			if !reflect.DeepEqual(persisted.Artifacts, record.Artifacts) || !reflect.DeepEqual(fx.vms.created.Artifacts, record.Artifacts) {
				t.Fatalf("artifact identities disagree: persisted=%v vm=%v registry=%v", persisted.Artifacts, fx.vms.created.Artifacts, record.Artifacts)
			}
			if fx.protocol.binding.Owner != record.Owner || fx.protocol.binding.Generation != record.Generation || fx.protocol.binding.Ref != record.Ref.ID {
				t.Fatalf("guest binding = %+v, registry = %+v", fx.protocol.binding, record)
			}
		})
	}

	t.Run("cleanup failure is durably queued", func(t *testing.T) {
		fx := newLifecycleFixture("persist")
		fx.vms.destroyErr = errors.New("destroy unavailable")
		lifecycle := NewLifecycle(LifecycleDeps{Identities: fx.identities, Worktrees: fx.worktrees, Artifacts: fx.artifacts, VMs: fx.vms, Protocol: fx.protocol, Registry: fx.registry, Sessions: fx.sessions})
		if _, err := lifecycle.Create(context.Background(), fx.request); err == nil {
			t.Fatal("Create() error = nil, want persistence failure")
		}
		if got := fx.registry.last(t).State; got != EnvironmentCleanupPending {
			t.Fatalf("registry state = %q, want cleanup-pending", got)
		}
	})
}

type lifecycleFixture struct {
	request    CreateRequest
	identities *fakeIdentities
	worktrees  *fakeWorktrees
	artifacts  *fakeArtifactVerifier
	vms        *fakeVMRuntime
	protocol   *fakeProtocol
	registry   *fakeRegistry
	sessions   *fakeSessionPersister
}

func newLifecycleFixture(fail string) *lifecycleFixture {
	failure := func(stage string) error {
		if fail == stage {
			return errors.New("injected " + stage + " failure")
		}
		return nil
	}
	requests := map[ArtifactKind]ArtifactRequest{
		ArtifactRuntime:        {Kind: ArtifactRuntime, Reference: "runtime@sha256:runtime", Digest: "sha256:runtime"},
		ArtifactFirmware:       {Kind: ArtifactFirmware, Reference: "firmware@sha256:firmware", Digest: "sha256:firmware"},
		ArtifactExecutionImage: {Kind: ArtifactExecutionImage, Reference: "image@sha256:image", Digest: "sha256:image"},
		ArtifactGuestAgent:     {Kind: ArtifactGuestAgent, Reference: "guest-agent@sha256:guest-agent", Digest: "sha256:guest-agent"},
	}
	verified := VerifiedArtifacts{
		Runtime:        VerifiedArtifact{Kind: ArtifactRuntime, Digest: "sha256:runtime"},
		Firmware:       VerifiedArtifact{Kind: ArtifactFirmware, Digest: "sha256:firmware"},
		ExecutionImage: VerifiedArtifact{Kind: ArtifactExecutionImage, Digest: "sha256:image"},
		GuestAgent:     VerifiedArtifact{Kind: ArtifactGuestAgent, Digest: "sha256:guest-agent"},
	}
	return &lifecycleFixture{
		request: CreateRequest{
			Owner: "caller:alice", SessionID: "session-1", Profile: "secure",
			Worktree:         worktree.Request{Source: "/source", WorktreePath: "/state/worktrees/session-1", MetadataPath: "/state/metadata/session-1", Branch: "mecatl/session-1"},
			ArtifactRequests: requests,
		},
		identities: &fakeIdentities{identity: EnvironmentIdentity{EnvironmentID: "env-1", VMID: "vm-1", Endpoint: "/run/mecatl/env-1.sock", Generation: 7}},
		worktrees:  &fakeWorktrees{err: failure("prepare")},
		artifacts:  &fakeArtifactVerifier{verified: verified, policyRevision: "policy-7", err: failure("verify")},
		vms:        &fakeVMRuntime{createErr: failure("create-vm"), readyErr: failure("ready")},
		protocol:   &fakeProtocol{err: failure("negotiate")},
		registry:   &fakeRegistry{},
		sessions:   &fakeSessionPersister{err: failure("persist")},
	}
}

type fakeIdentities struct{ identity EnvironmentIdentity }

func (f *fakeIdentities) Allocate(string) (EnvironmentIdentity, error) { return f.identity, nil }

type fakeWorktrees struct {
	err     error
	cleaned bool
}

func (f *fakeWorktrees) Prepare(_ context.Context, request worktree.Request) (*worktree.Prepared, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &worktree.Prepared{SourceRoot: request.Source, WorktreePath: request.WorktreePath, MetadataPath: request.MetadataPath, Branch: request.Branch, CommonObjectStore: "/source/.git/objects"}, nil
}
func (f *fakeWorktrees) Cleanup(context.Context, *worktree.Prepared) error {
	f.cleaned = true
	return nil
}

type fakeArtifactVerifier struct {
	verified       VerifiedArtifacts
	policyRevision string
	err            error
}

func (f *fakeArtifactVerifier) Verify(context.Context, map[ArtifactKind]ArtifactRequest) (VerifiedArtifacts, string, error) {
	return f.verified, f.policyRevision, f.err
}

type fakeVMRuntime struct {
	created    VMCreateRequest
	createErr  error
	readyErr   error
	destroyErr error
}

func (f *fakeVMRuntime) Create(_ context.Context, request VMCreateRequest) error {
	f.created = request
	return f.createErr
}
func (f *fakeVMRuntime) WaitReady(context.Context, EnvironmentRecord) error { return f.readyErr }
func (*fakeVMRuntime) Inspect(_ context.Context, record EnvironmentRecord) (RuntimeStatus, error) {
	return RuntimeStatus{Live: true, Generation: record.Generation, VMID: record.VMID, PID: 42, ProcessIdentity: "boot-1:42", Endpoint: record.Endpoint}, nil
}
func (f *fakeVMRuntime) Destroy(context.Context, EnvironmentRecord) error { return f.destroyErr }

type fakeProtocol struct {
	endpoint string
	binding  control.Binding
	err      error
}

func (f *fakeProtocol) Negotiate(_ context.Context, endpoint string, binding control.Binding) (control.Agreement, error) {
	f.endpoint, f.binding = endpoint, binding
	if f.err != nil {
		return control.Agreement{}, f.err
	}
	return control.Agreement{Version: control.ProtocolVersion, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes}, nil
}

type fakeRegistry struct{ records []EnvironmentRecord }

func (f *fakeRegistry) Save(_ context.Context, record EnvironmentRecord) error {
	f.records = append(f.records, cloneEnvironmentRecord(record))
	return nil
}
func (f *fakeRegistry) last(t *testing.T) EnvironmentRecord {
	t.Helper()
	if len(f.records) == 0 {
		t.Fatal("registry has no durable record")
	}
	return f.records[len(f.records)-1]
}

type fakeSessionPersister struct {
	saved SessionPlacement
	err   error
}

func (f *fakeSessionPersister) Persist(_ context.Context, placement SessionPlacement) error {
	f.saved = placement
	return f.err
}
