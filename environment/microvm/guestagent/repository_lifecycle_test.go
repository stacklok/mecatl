package guestagent

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestRepositoryLifecycleRetriesLostRepliesAndRejectsStaleIncarnations(t *testing.T) {
	server := newLifecycleTestServer(t)
	binding := lifecycleTestBinding()
	register := RepositoryControlRequest{Operation: RepositoryRegister, Sequence: 1, Incarnation: "incarnation-one", Binding: binding}

	exchangeLifecycleMutation(t, server, register, true)
	response := exchangeLifecycleMutation(t, server, register, false)
	if response.ErrorCode != "" {
		t.Fatalf("register retry after lost response = %q", response.ErrorCode)
	}
	services, err := connectBoundLifecycle(t.Context(), server, binding, register.Incarnation)
	if err != nil {
		t.Fatalf("connect registered data channel: %v", err)
	}
	_ = services.Close()
	if replay, replayErr := connectBoundLifecycle(t.Context(), server, binding, register.Incarnation); replayErr == nil {
		_ = replay.Close()
		t.Fatal("replayed data claim succeeded")
	}

	unregister := RepositoryControlRequest{Operation: RepositoryUnregister, Sequence: 2, Incarnation: register.Incarnation, Binding: binding}
	exchangeLifecycleMutation(t, server, unregister, true)
	if response := exchangeLifecycleMutation(t, server, unregister, false); response.ErrorCode != "" {
		t.Fatalf("unregister retry after lost response = %q", response.ErrorCode)
	}
	second := RepositoryControlRequest{Operation: RepositoryRegister, Sequence: 3, Incarnation: "incarnation-two", Binding: binding}
	if response := exchangeLifecycleMutation(t, server, second, false); response.ErrorCode != "" {
		t.Fatalf("replacement register = %q", response.ErrorCode)
	}
	stale := unregister
	stale.Sequence = 4
	if response := exchangeLifecycleMutation(t, server, stale, false); response.ErrorCode != "unauthenticated" {
		t.Fatalf("stale unregister = %q, want unauthenticated", response.ErrorCode)
	}
	staleRegister := register
	staleRegister.Sequence = 5
	if response := exchangeLifecycleMutation(t, server, staleRegister, false); response.ErrorCode != "unauthenticated" {
		t.Fatalf("stale register = %q, want unauthenticated", response.ErrorCode)
	}
	services, err = connectBoundLifecycle(t.Context(), server, binding, second.Incarnation)
	if err != nil {
		t.Fatalf("stale mutations damaged replacement data claim: %v", err)
	}
	_ = services.Close()
	if err := server.Probe(binding); err != nil {
		t.Fatalf("stale unregister removed replacement: %v", err)
	}
}

func TestRepositoryLifecycleTurnoverHasNoCapabilityNonceCeiling(t *testing.T) {
	server := newLifecycleTestServer(t)
	binding := lifecycleTestBinding()
	var sequence uint64 = 1
	for i := range 4100 {
		incarnation := "incarnation-" + strconv.Itoa(i+1)
		register := RepositoryControlRequest{Operation: RepositoryRegister, Sequence: sequence, Incarnation: incarnation, Binding: binding}
		sequence++
		if response := exchangeLifecycleMutation(t, server, register, false); response.ErrorCode != "" {
			t.Fatalf("register iteration %d = %q", i, response.ErrorCode)
		}
		services, err := connectBoundLifecycle(t.Context(), server, binding, incarnation)
		if err != nil {
			t.Fatalf("data iteration %d: %v", i, err)
		}
		_ = services.Close()
		unregister := RepositoryControlRequest{Operation: RepositoryUnregister, Sequence: sequence, Incarnation: incarnation, Binding: binding}
		sequence++
		if response := exchangeLifecycleMutation(t, server, unregister, false); response.ErrorCode != "" {
			t.Fatalf("unregister iteration %d = %q", i, response.ErrorCode)
		}
	}
}

func TestRepositoryChannelRejectsReplayedPeerProof(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	var captured repositoryChannelResponse
	firstHost, firstGuest := net.Pipe()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- AuthenticateHostRepositoryChannel(t.Context(), firstHost, key, "operator", "repository", "vm-1", 7, RepositoryChannelControl)
	}()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	var firstChallenge repositoryChannelChallenge
	if err := codec.Read(firstGuest, &firstChallenge); err != nil {
		t.Fatal(err)
	}
	captured.HostNonce[0] = 1
	copy(captured.GuestMAC[:], repositoryChannelMAC(key, "guest", firstChallenge, nil))
	if err := codec.Write(firstGuest, captured); err != nil {
		t.Fatal(err)
	}
	var confirmation repositoryChannelConfirmation
	if err := codec.Read(firstGuest, &confirmation); err != nil {
		t.Fatal(err)
	}
	_ = firstGuest.Close()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	replayHost, replayGuest := net.Pipe()
	replayDone := make(chan error, 1)
	go func() {
		replayDone <- AuthenticateHostRepositoryChannel(t.Context(), replayHost, key, "operator", "repository", "vm-1", 7, RepositoryChannelControl)
	}()
	var freshChallenge repositoryChannelChallenge
	if err := codec.Read(replayGuest, &freshChallenge); err != nil {
		t.Fatal(err)
	}
	if freshChallenge.Nonce == firstChallenge.Nonce {
		t.Fatal("fresh channel challenge repeated")
	}
	if err := codec.Write(replayGuest, captured); err != nil {
		t.Fatal(err)
	}
	_ = replayGuest.Close()
	if err := <-replayDone; !errors.Is(err, ErrUnauthenticatedRepositoryChannel) {
		t.Fatalf("replayed peer proof = %v, want unauthenticated", err)
	}
}

func newLifecycleTestServer(t *testing.T) *RepositoryServer {
	t.Helper()
	root := t.TempDir()
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // test identity
	contract := guestexec.DefaultRuntimeContract()
	contract.Identity = identity
	server, err := NewRepositoryServer(RepositoryServerConfig{
		Owner: "operator", RepositoryKey: "repository", VMID: "vm-1", Endpoint: "endpoint", Generation: 7,
		AuthorityKey: []byte("0123456789abcdef0123456789abcdef"), WorkloadIdentity: identity, RuntimeContract: contract,
		ResolveRoot: func(string) (string, error) { return root, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func lifecycleTestBinding() control.Binding {
	return control.Binding{Owner: "operator", SessionID: "logical", EnvironmentID: "logical", Ref: "logical-ref", Generation: 7, AssignedRoot: "/run/mecatl/repositories/logical/worktree"}
}

func exchangeLifecycleMutation(t *testing.T, server *RepositoryServer, request RepositoryControlRequest, dropResponse bool) RepositoryControlResponse {
	t.Helper()
	host, guest := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.ServeControl(context.Background(), guest) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(host, request); err != nil {
		t.Fatal(err)
	}
	if dropResponse {
		_ = host.Close()
		<-done
		return RepositoryControlResponse{}
	}
	var response RepositoryControlResponse
	if err := codec.Read(host, &response); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	serveErr := <-done
	if serveErr != nil && response.ErrorCode == "" && !errors.Is(serveErr, net.ErrClosed) {
		t.Fatalf("serve control: %v", serveErr)
	}
	return response
}

func connectBoundLifecycle(ctx context.Context, server *RepositoryServer, binding control.Binding, incarnation string) (*Services, error) {
	host, guest := net.Pipe()
	go func() { _ = server.ServeBound(ctx, guest) }()
	services, err := ConnectRepository(ctx, host, binding, incarnation)
	if err != nil {
		_ = host.Close()
	}
	return services, err
}
