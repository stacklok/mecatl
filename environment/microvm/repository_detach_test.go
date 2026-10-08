package microvm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
)

type retryingRepositoryGuest struct {
	calls    int
	delegate RepositoryGuestRegistrar
}

func (*retryingRepositoryGuest) Register(context.Context, RepositoryVMRecord, control.Binding, RepositoryGuestMount) (*guestagent.Services, error) {
	return nil, errors.New("not used")
}

func (g *retryingRepositoryGuest) Unregister(ctx context.Context, record RepositoryVMRecord, binding control.Binding) error {
	g.calls++
	if g.calls == 1 {
		return errors.New("teardown uncertain")
	}
	if g.delegate != nil {
		return g.delegate.Unregister(ctx, record, binding)
	}
	return nil
}

func TestRepositoryDaemonDetachRetainsBindingAndWorktreeUntilRetry(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	defer attachment.Close()
	originalGuest := attachment.Logical.guest
	guest := &retryingRepositoryGuest{delegate: originalGuest}
	attachment.Logical.guest = guest
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
	daemon := &Daemon{
		repositoryAttachments: fixture.composition.Attachments,
		repositoryBindings:    map[string]repositoryDaemonBinding{binding.Ref: {binding: binding}},
	}
	request := LifecycleRequest{Operation: LifecycleDetach, Binding: binding}
	if _, err := daemon.repositoryOperation(t.Context(), request); err == nil {
		t.Fatal("first daemon detach succeeded despite uncertain teardown")
	}
	if daemon.repositoryAttachment(binding) == nil {
		t.Fatal("daemon dropped binding after failed detach")
	}
	if _, err := os.Stat(attachment.Logical.WorktreePath); err != nil {
		t.Fatalf("failed detach removed worktree: %v", err)
	}
	if _, err := daemon.repositoryOperation(t.Context(), request); err != nil {
		t.Fatalf("retry daemon detach: %v", err)
	}
	if daemon.repositoryAttachment(binding) != nil || guest.calls != 2 {
		t.Fatalf("successful retry state: bound=%v calls=%d", daemon.repositoryAttachment(binding) != nil, guest.calls)
	}
	if _, err := os.Stat(attachment.Logical.WorktreePath); err != nil {
		t.Fatalf("detach retry deleted preserved worktree: %v", err)
	}
}

func TestRepositoryAttachmentDetachRetainsActiveBindingUntilRetrySucceeds(t *testing.T) {
	guest := &retryingRepositoryGuest{}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind(Kind), ID: "logical-test@1"}
	logical := &LogicalEnvironment{guest: guest}
	attachment := &RepositoryAttachment{Logical: logical}
	manager := &RepositoryAttachmentManager{active: map[session.EnvironmentRef]*repositoryChildAttachment{ref: {attachment: attachment}}}

	if err := manager.Detach(ref); err == nil {
		t.Fatal("first detach succeeded despite uncertain teardown")
	}
	if manager.lookup(ref) == nil {
		t.Fatal("failed detach dropped the sole active retry handle")
	}
	if err := manager.Detach(ref); err != nil {
		t.Fatalf("retry detach: %v", err)
	}
	if manager.lookup(ref) != nil || guest.calls != 2 {
		t.Fatalf("successful retry state: active=%v calls=%d", manager.lookup(ref) != nil, guest.calls)
	}
}
