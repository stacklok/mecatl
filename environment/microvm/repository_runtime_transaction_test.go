package microvm

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
)

func TestRepositoryMutationAcknowledgementCannotChangeNewIncarnation(t *testing.T) {
	binding := control.Binding{Ref: "logical-ref", Owner: "operator", Generation: 1}
	generation := &repositoryRuntimeGeneration{registrations: map[string]*repositoryHostRegistration{
		binding.Ref: {binding: binding, incarnation: "new-incarnation", registered: true},
	}}
	applyRepositoryMutationAcknowledgement(generation, guestagent.RepositoryControlRequest{
		Operation: guestagent.RepositoryUnregister, Binding: binding, Incarnation: "old-incarnation",
	}, true)
	registration := generation.registrations[binding.Ref]
	if registration == nil || registration.incarnation != "new-incarnation" || !registration.registered {
		t.Fatalf("stale acknowledgement changed new incarnation: %+v", registration)
	}
}

func TestRepositoryRuntimeLostAcknowledgementAppliedBeforeInterleavedMutation(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	anchor := fixture.attach(t)
	defer anchor.Close()
	record := anchor.Logical.Repository
	binding := func(id string) (control.Binding, RepositoryGuestMount) {
		root := filepath.Join(filepath.Dir(record.RootFSPath), "logical", id, "worktree")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		guestRoot := path.Join(RepositoryGuestMountRoot, id, "worktree")
		return control.Binding{Owner: record.Owner, SessionID: id, EnvironmentID: id, Ref: "logical-" + id + "@" + fmt.Sprint(record.Generation), Generation: record.Generation, AssignedRoot: guestRoot}, RepositoryGuestMount{HostPath: root, GuestPath: guestRoot}
	}
	a, mountA := binding("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b, mountB := binding("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	fixture.backend.mu.Lock()
	fixture.backend.dropNextControlResponse = true
	fixture.backend.mu.Unlock()
	if services, err := fixture.composition.Runtime.Register(t.Context(), record, a, mountA); err == nil {
		_ = services.Close()
		t.Fatal("lost register acknowledgement unexpectedly succeeded")
	}
	servicesB, err := fixture.composition.Runtime.Register(t.Context(), record, b, mountB)
	if err != nil {
		t.Fatalf("interleaved register B: %v", err)
	}
	defer servicesB.Close()
	servicesA, err := fixture.composition.Runtime.Register(t.Context(), record, a, mountA)
	if err != nil {
		t.Fatalf("retry register A after B: %v", err)
	}
	_ = servicesA.Close()

	fixture.backend.mu.Lock()
	fixture.backend.dropNextControlResponse = true
	fixture.backend.mu.Unlock()
	if err := fixture.composition.Runtime.Unregister(t.Context(), record, a); err == nil {
		t.Fatal("lost unregister acknowledgement unexpectedly succeeded")
	}
	if err := fixture.composition.Runtime.Unregister(t.Context(), record, b); err != nil {
		t.Fatalf("interleaved unregister B: %v", err)
	}
	if err := fixture.composition.Runtime.Unregister(t.Context(), record, a); err != nil {
		t.Fatalf("retry unregister A after B: %v", err)
	}
}

func TestRepositoryRuntimeSemanticFailureAppliedBeforeLaterMutation(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	anchor := fixture.attach(t)
	defer anchor.Close()
	record := anchor.Logical.Repository
	makeBinding := func(id string, create bool) (control.Binding, RepositoryGuestMount) {
		root := filepath.Join(filepath.Dir(record.RootFSPath), "logical", id, "worktree")
		if create {
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		guestRoot := path.Join(RepositoryGuestMountRoot, id, "worktree")
		return control.Binding{Owner: record.Owner, SessionID: id, EnvironmentID: id, Ref: "logical-" + id + "@" + fmt.Sprint(record.Generation), Generation: record.Generation, AssignedRoot: guestRoot}, RepositoryGuestMount{HostPath: root, GuestPath: guestRoot}
	}
	a, mountA := makeBinding("cccccccccccccccccccccccccccccccc", false)
	b, mountB := makeBinding("dddddddddddddddddddddddddddddddd", true)
	if _, err := fixture.composition.Runtime.Register(t.Context(), record, a, mountA); !errors.Is(err, ErrRepositoryLogicalRootUnavailable) {
		t.Fatalf("semantic register A failure = %v", err)
	}
	servicesB, err := fixture.composition.Runtime.Register(t.Context(), record, b, mountB)
	if err != nil {
		t.Fatalf("register B after semantic A failure: %v", err)
	}
	defer servicesB.Close()
	if err := os.MkdirAll(mountA.HostPath, 0o700); err != nil {
		t.Fatal(err)
	}
	servicesA, err := fixture.composition.Runtime.Register(t.Context(), record, a, mountA)
	if err != nil {
		t.Fatalf("repaired retry A: %v", err)
	}
	_ = servicesA.Close()
}

func TestRepositoryRuntimeDecodedMutationFailureAdvancesSequence(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	anchor := fixture.attach(t)
	defer anchor.Close()
	record := anchor.Logical.Repository
	const logicalID = "22222222222222222222222222222222"
	logicalRoot := filepath.Join(filepath.Dir(record.RootFSPath), "logical", logicalID)
	worktree := filepath.Join(logicalRoot, "worktree")
	guestRoot := path.Join(RepositoryGuestMountRoot, logicalID, "worktree")
	binding := control.Binding{
		Owner: record.Owner, SessionID: logicalID, EnvironmentID: logicalID,
		Ref: "logical-" + logicalID + "@" + fmt.Sprint(record.Generation), Generation: record.Generation, AssignedRoot: guestRoot,
	}

	generation, err := fixture.composition.Runtime.generation(record)
	if err != nil {
		t.Fatal(err)
	}
	generation.controlMu.Lock()
	initialSequence := generation.nextSequence
	generation.controlMu.Unlock()
	if _, err := fixture.composition.Runtime.Register(t.Context(), record, binding, RepositoryGuestMount{HostPath: worktree, GuestPath: guestRoot}); !errors.Is(err, ErrRepositoryLogicalRootUnavailable) {
		t.Fatalf("register missing logical root = %v", err)
	}
	generation, err = fixture.composition.Runtime.generation(record)
	if err != nil {
		t.Fatal(err)
	}
	generation.controlMu.Lock()
	pending, sequence := generation.pending, generation.nextSequence
	generation.controlMu.Unlock()
	if pending != nil || sequence != initialSequence+1 {
		t.Fatalf("decoded failed mutation retained pending request: pending=%+v sequence=%d", pending, sequence)
	}

	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	services, err := fixture.composition.Runtime.Register(t.Context(), record, binding, RepositoryGuestMount{HostPath: worktree, GuestPath: guestRoot})
	if err != nil {
		t.Fatalf("register after repairing logical root: %v", err)
	}
	_ = services.Close()
}

func TestRepositoryRuntimeDataConnectFailureRollsBackRegistration(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	anchor := fixture.attach(t)
	defer anchor.Close()
	record := anchor.Logical.Repository
	const logicalID = "11111111111111111111111111111111"
	logicalRoot := filepath.Join(filepath.Dir(record.RootFSPath), "logical", logicalID)
	worktree := filepath.Join(logicalRoot, "worktree")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(logicalRoot, "metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	guestRoot := path.Join(RepositoryGuestMountRoot, logicalID, "worktree")
	binding := control.Binding{
		Owner: record.Owner, SessionID: logicalID, EnvironmentID: logicalID,
		Ref: "logical-" + logicalID + "@" + fmt.Sprint(record.Generation), Generation: record.Generation, AssignedRoot: guestRoot,
	}
	fixture.backend.mu.Lock()
	fixture.backend.failNextData = true
	fixture.backend.mu.Unlock()
	if services, err := fixture.composition.Runtime.Register(t.Context(), record, binding, RepositoryGuestMount{HostPath: worktree, GuestPath: guestRoot}); err == nil {
		_ = services.Close()
		t.Fatal("data connection failure did not fail registration")
	}
	if err := fixture.backend.server.Probe(binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("failed data setup left guest registration: %v", err)
	}
	generation, err := fixture.composition.Runtime.generation(record)
	if err != nil {
		t.Fatal(err)
	}
	generation.controlMu.Lock()
	_, retained := generation.registrations[binding.Ref]
	generation.controlMu.Unlock()
	if retained {
		t.Fatal("confirmed rollback retained host registration")
	}
	services, err := fixture.composition.Runtime.Register(t.Context(), record, binding, RepositoryGuestMount{HostPath: worktree, GuestPath: guestRoot})
	if err != nil {
		t.Fatalf("register after rollback: %v", err)
	}
	_ = services.Close()
	generation.controlMu.Lock()
	sequenceBefore := generation.nextSequence
	generation.controlMu.Unlock()
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- fixture.composition.Runtime.Unregister(t.Context(), record, binding)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent unregister: %v", err)
		}
	}
	generation.controlMu.Lock()
	sequenceAfter := generation.nextSequence
	generation.controlMu.Unlock()
	if sequenceAfter != sequenceBefore+1 {
		t.Fatalf("concurrent unregister mutations = %d, want 1", sequenceAfter-sequenceBefore)
	}
}
