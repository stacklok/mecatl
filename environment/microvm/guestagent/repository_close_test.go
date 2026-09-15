package guestagent

import (
	"context"
	"os"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestRepositoryUnregisterClearsPerRefReplayState(t *testing.T) {
	root := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // test process identity
	contract := guestexec.DefaultRuntimeContract()
	contract.Identity = identity
	server, err := NewRepositoryServer(RepositoryServerConfig{
		Owner: "operator", Generation: 7, AuthorityKey: key, WorkloadIdentity: identity, RuntimeContract: contract,
		ResolveRoot: func(string) (string, error) { return root, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Binding{Owner: "operator", SessionID: "logical", EnvironmentID: "logical", Ref: "logical-ref", Generation: 7, AssignedRoot: "/run/mecatl/repositories/logical/worktree"}

	for i := range 5000 {
		registration, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		if err := server.Register(context.Background(), registration, binding); err != nil {
			t.Fatalf("register iteration %d: %v", i, err)
		}
		unregistration, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		if err := server.Unregister(unregistration, binding); err != nil {
			t.Fatalf("unregister iteration %d: %v", i, err)
		}
		if err := server.Probe(binding); err == nil {
			t.Fatalf("iteration %d remained registered", i)
		}
	}
}
