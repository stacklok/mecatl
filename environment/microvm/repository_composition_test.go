package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gomicrovmnet "github.com/stacklok/go-microvm/net"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestRepositoryGuestAuthenticationRejectsCompetingConnectorBeforeDisclosure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	if err := os.Chmod(filepath.Join(repository, "README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "add", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "commit", "-m", "tracked"); err != nil {
		t.Fatal(err)
	}
	backend := &repositoryCompositionBackend{
		attackFirst:     true,
		attackerPayload: make(chan []byte, 1),
		attackerClosed:  make(chan struct{}, 1),
	}
	provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
	composition, err := NewRepositoryComposition(filepath.Join(root, "state"), RepositoryRuntimeConfig{
		Backend: backend, Network: NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{}),
		GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, DialGuest: backend.dialData, DialControl: backend.dialControl,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	logical, err := composition.Logical.Create(ctx, LogicalEnvironmentRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(repositoryVerifiedArtifacts(t, root))})
	if err != nil {
		t.Fatalf("real repository guest did not establish after stalled connector: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("parent operation expired before the legitimate guest authenticated: %v", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("legitimate guest authentication took %v after stalled connector", elapsed)
	}
	defer logical.Close()
	select {
	case <-backend.attackerClosed:
	case <-time.After(time.Second):
		t.Fatal("stalled unauthenticated connector was not closed")
	}
	payload := <-backend.attackerPayload
	for _, forbidden := range []string{"capability", "binding", "operation", "assigned_root", "session_id"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("unauthenticated connector received %q in pre-auth frame: %s", forbidden, payload)
		}
	}
	if !strings.Contains(string(payload), `"purpose":"control"`) || !strings.Contains(string(payload), `"generation":`) || strings.Contains(string(payload), `"generation":0`) {
		t.Fatalf("authentication challenge was not generation/purpose bound: %s", payload)
	}
}

func TestRepositoryProductionCompositionBootsOnceAndRoutesGuestMounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	if err := os.Chmod(filepath.Join(repository, "README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "add", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "commit", "-m", "tracked"); err != nil {
		t.Fatal(err)
	}

	backend := &repositoryCompositionBackend{}
	provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
	network := NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{})

	composition, err := NewRepositoryComposition(filepath.Join(root, "state"), RepositoryRuntimeConfig{
		Backend: backend, Network: network, GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll},
		DialGuest: backend.dialData, DialControl: backend.dialControl,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified := repositoryVerifiedArtifacts(t, root)
	createCtx, cancelCreate := context.WithCancel(t.Context())
	first, err := composition.Logical.Create(createCtx, LogicalEnvironmentRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := composition.Logical.Create(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()

	if backend.starts != 1 || first.Repository.RootFSPath != second.Repository.RootFSPath {
		t.Fatalf("repository composition starts=%d roots=%q/%q", backend.starts, first.Repository.RootFSPath, second.Repository.RootFSPath)
	}
	if backend.launch.VsockPort != control.GuestControlPort {
		t.Fatalf("repository launch vsock port = %d, want %d", backend.launch.VsockPort, control.GuestControlPort)
	}
	if first.GuestRoot == first.WorktreePath || second.GuestRoot == second.WorktreePath || !strings.HasPrefix(first.GuestRoot, RepositoryGuestMountRoot+"/") {
		t.Fatalf("host paths leaked as guest roots: first=%+v second=%+v", first, second)
	}
	if got, err := first.Workspace.Read(t.Context(), "tracked.txt"); err != nil || string(got) != "source\n" {
		t.Fatalf("first guest mount read = %q, %v", got, err)
	}
	if got, err := second.Workspace.Read(t.Context(), "tracked.txt"); err != nil || string(got) != "source\n" {
		t.Fatalf("second guest mount read = %q, %v", got, err)
	}
	if first.Binding.Ref == second.Binding.Ref || first.GuestRoot == second.GuestRoot {
		t.Fatal("logical guest refs or roots were shared")
	}
	cancelCreate()
	if err := provider.startCtx.Err(); err != nil {
		t.Fatalf("repository generation network inherited create cancellation: %v", err)
	}
}

func TestRepositoryExecStreamForwardsManagedTemporaryScopeIntoGuest(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
	if err := fixture.composition.Attachments.register(binding, attachment); err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: fixture.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	daemon.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	payload, err := json.Marshal(map[string]string{
		"command":         `printf '%s\n' "$TMPDIR"; printf managed-ok > "$TMPDIR/proof"; cat "$TMPDIR/proof"`,
		"temporary_scope": "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	response, err := daemon.repositoryExecStream(t.Context(), LifecycleRequest{Binding: binding, Payload: payload}, func(frame LifecycleExecStream) error {
		_, writeErr := output.Write(frame.Data)
		return writeErr
	})
	if err != nil {
		t.Fatalf("repository managed exec: %v", err)
	}
	var final struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(response.Payload, &final); err != nil || final.ExitCode != 0 {
		t.Fatalf("repository managed exec response = %+v, %v", response, err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "/tmp/mecatl-managed-") || lines[1] != "managed-ok" {
		t.Fatalf("repository managed output = %q", output.String())
	}
	if _, err := os.Stat(lines[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository managed temporary directory survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(attachment.Logical.WorktreePath, "proof")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository managed temporary file escaped into worktree: %v", err)
	}
}

func TestRepositoryExecStreamRejectsDetachedAndUnknownLogicalBindings(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
	if err := fixture.composition.Attachments.register(binding, attachment); err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: fixture.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	daemon.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	if response, opErr := daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDetach, Binding: binding}); opErr != nil || response.Binding != binding {
		t.Fatalf("detach response=%+v err=%v", response, opErr)
	}

	payload, err := json.Marshal(map[string]string{"command": "printf unsafe"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := binding
	unknown.EnvironmentID = "logical-00000000000000000000000000000000"
	unknown.Ref = unknown.EnvironmentID + "@" + fmt.Sprint(unknown.Generation)
	for name, claim := range map[string]control.Binding{"detached": binding, "unknown": unknown} {
		t.Run(name, func(t *testing.T) {
			server, client := net.Pipe()
			serveDone := make(chan error, 1)
			go func() { serveDone <- daemon.ServeConn(t.Context(), server) }()
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleExec, Binding: claim, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			var response LifecycleResponse
			if err := codec.Read(client, &response); err != nil {
				t.Fatal(err)
			}
			_ = client.Close()
			if response.ErrorCode != "binding_mismatch" || !strings.Contains(response.ErrorText, control.ErrBindingMismatch.Error()) {
				t.Fatalf("stream exec response = %+v, want binding mismatch", response)
			}
			if err := <-serveDone; !errors.Is(err, control.ErrBindingMismatch) {
				t.Fatalf("serve error = %v, want binding mismatch", err)
			}
		})
	}
}

func TestRepositoryAttachmentInventoryAndDeleteSurviveRestart(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachments := make([]*RepositoryAttachment, 0, 2)
	for _, sessionID := range []string{"session-a", "session-b"} {
		attachment := fixture.attach(t)
		environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
		if err != nil {
			t.Fatal(err)
		}
		binding := control.Binding{Owner: "operator", SessionID: sessionID, EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
		if err := fixture.composition.Attachments.register(binding, attachment); err != nil {
			t.Fatal(err)
		}
		attachments = append(attachments, attachment)
	}
	if err := os.WriteFile(filepath.Join(attachments[1].Logical.WorktreePath, "dirty.txt"), []byte("retain\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: restarted.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	page := func(continuation string) LifecycleInventoryPage {
		payload, marshalErr := json.Marshal(LifecycleInventoryRequest{PageSize: 1, Continuation: continuation})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		response := daemon.handleStandardRequest(t.Context(), LifecycleRequest{Operation: LifecycleInventory, Binding: control.Binding{Owner: "operator"}, Payload: payload})
		if response.Err != nil {
			t.Fatal(response.Err)
		}
		var result LifecycleInventoryPage
		if err := json.Unmarshal(response.Payload, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := page("")
	second := page(first.Continuation)
	if len(first.Entries) != 1 || len(second.Entries) != 1 || first.Continuation == "" || second.Continuation != "" || first.Entries[0].SessionID != "session-a" || second.Entries[0].SessionID != "session-b" {
		t.Fatalf("restarted inventory pages = %+v / %+v", first, second)
	}
	clean := first.Entries[0]
	cleanBinding := control.Binding{Owner: clean.Owner, SessionID: clean.SessionID, EnvironmentID: clean.EnvironmentID, Ref: clean.Ref, Generation: clean.Generation}
	cleanResponse, err := daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDelete, Binding: cleanBinding})
	if err != nil {
		t.Fatal(err)
	}
	var cleanResult LifecycleDeleteResult
	if err := json.Unmarshal(cleanResponse.Payload, &cleanResult); err != nil || cleanResult.WorktreeRetained {
		t.Fatalf("restarted clean delete = %+v, %v", cleanResult, err)
	}
	if _, err := os.Stat(cleanResult.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restarted clean worktree still exists: %v", err)
	}
	dirty := second.Entries[0]
	binding := control.Binding{Owner: dirty.Owner, SessionID: dirty.SessionID, EnvironmentID: dirty.EnvironmentID, Ref: dirty.Ref, Generation: dirty.Generation}
	response, err := daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDelete, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	var result LifecycleDeleteResult
	if err := json.Unmarshal(response.Payload, &result); err != nil || !result.WorktreeRetained || result.WorktreePath != dirty.WorktreePath {
		t.Fatalf("restarted dirty delete = %+v, %v", result, err)
	}
	retained := page("")
	retained = page(retained.Continuation)
	if len(retained.Entries) != 1 || retained.Entries[0].State != EnvironmentReady || retained.Entries[0].Health != GenerationStale || retained.Entries[0].Error == "" {
		t.Fatalf("restarted retained inventory = %+v", retained)
	}
}

func TestRepositoryProductionInventoryPaginationAndLogicalDelete(t *testing.T) {
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	if err := os.Chmod(filepath.Join(repository, "README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &repositoryCompositionBackend{}
	provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
	composition, err := NewRepositoryComposition(filepath.Join(root, "state"), RepositoryRuntimeConfig{
		Backend: backend, Network: NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{}),
		GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, DialGuest: backend.dialData, DialControl: backend.dialControl,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified := repositoryVerifiedArtifacts(t, root)
	attachments := make([]*RepositoryAttachment, 0, 2)
	for _, sessionID := range []string{"session-a", "session-b"} {
		attachment, attachErr := composition.Attachments.Attach(t.Context(), LogicalEnvironmentRequest{Owner: "local", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
		if attachErr != nil {
			t.Fatal(attachErr)
		}
		attachments = append(attachments, attachment)
		environmentID, generation, parseErr := parseEnvironmentRef(attachment.Logical.Ref)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		binding := control.Binding{Owner: "local", SessionID: sessionID, EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
		if registerErr := composition.Attachments.register(binding, attachment); registerErr != nil {
			t.Fatal(registerErr)
		}
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{
		Control: auth, RepositoryAttachments: composition.Attachments,
		RepositoryProvisioner: func(context.Context, ProvisionRequest) (RepositoryPlacement, error) {
			return RepositoryPlacement{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory := func(continuation string) LifecycleInventoryPage {
		payload, marshalErr := json.Marshal(LifecycleInventoryRequest{PageSize: 1, Continuation: continuation})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		response := daemon.handleStandardRequest(t.Context(), LifecycleRequest{Operation: LifecycleInventory, Binding: control.Binding{Owner: "local"}, Payload: payload})
		if response.Err != nil {
			t.Fatal(response.Err)
		}
		var page LifecycleInventoryPage
		if unmarshalErr := json.Unmarshal(response.Payload, &page); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		return page
	}
	first := inventory("")
	second := inventory(first.Continuation)
	if len(first.Entries) != 1 || len(second.Entries) != 1 || first.Continuation == "" || second.Continuation != "" {
		t.Fatalf("repository inventory pages = %+v / %+v", first, second)
	}
	if first.Entries[0].SessionID != "session-a" || second.Entries[0].SessionID != "session-b" ||
		first.Entries[0].Generation != second.Entries[0].Generation || first.Entries[0].WorktreePath == second.Entries[0].WorktreePath {
		t.Fatalf("repository logical inventory is dishonest: %+v / %+v", first.Entries[0], second.Entries[0])
	}
	cleanBinding := control.Binding{Owner: first.Entries[0].Owner, SessionID: first.Entries[0].SessionID, EnvironmentID: first.Entries[0].EnvironmentID, Ref: first.Entries[0].Ref, Generation: first.Entries[0].Generation}
	cleanResponse, err := daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDelete, Binding: cleanBinding})
	if err != nil {
		t.Fatal(err)
	}
	var clean LifecycleDeleteResult
	if err := json.Unmarshal(cleanResponse.Payload, &clean); err != nil || clean.WorktreeRetained {
		t.Fatalf("clean logical delete = %+v, %v", clean, err)
	}
	if _, statErr := os.Stat(clean.WorktreePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("clean logical worktree still exists: %v", statErr)
	}
	if err := os.WriteFile(filepath.Join(attachments[1].Logical.WorktreePath, "dirty.txt"), []byte("retain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyEntry := second.Entries[0]
	dirtyBinding := control.Binding{Owner: dirtyEntry.Owner, SessionID: dirtyEntry.SessionID, EnvironmentID: dirtyEntry.EnvironmentID, Ref: dirtyEntry.Ref, Generation: dirtyEntry.Generation}
	dirtyResponse, err := daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDelete, Binding: dirtyBinding})
	if err != nil {
		t.Fatal(err)
	}
	var dirty LifecycleDeleteResult
	if err := json.Unmarshal(dirtyResponse.Payload, &dirty); err != nil || !dirty.WorktreeRetained {
		t.Fatalf("dirty logical delete = %+v, %v", dirty, err)
	}
	if _, statErr := os.Stat(filepath.Join(dirty.WorktreePath, "dirty.txt")); statErr != nil {
		t.Fatalf("dirty logical state was not retained: %v", statErr)
	}
	retained := inventory("")
	if len(retained.Entries) != 1 || retained.Entries[0].State != EnvironmentReady || retained.Entries[0].Health != GenerationStale || retained.Entries[0].Error == "" {
		t.Fatalf("retained logical inventory = %+v", retained)
	}
	reattached, err := composition.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "local", Checkout: repository, Artifacts: testArtifactSnapshot(verified)}, attachments[1].Environment.Ref())
	if err != nil {
		t.Fatalf("reattach retained dirty logical environment: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(reattached.Logical.WorktreePath, "dirty.txt")); statErr != nil {
		t.Fatalf("reattached dirty state is unavailable: %v", statErr)
	}
	if err := reattached.Logical.Detach(); err != nil {
		t.Fatalf("detach reattached dirty logical environment: %v", err)
	}
}

func TestRepositoryRuntimeAbortRejectsDifferentGeneration(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewRepositoryRuntime(RepositoryRuntimeConfig{
		Backend:     &rollbackRuntimeBackend{},
		Network:     NewNetworkController(func() gomicrovmnet.Provider { return &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")} }, &fakeGuestNetwork{}),
		DialGuest:   func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrEnvironmentUnavailable },
		DialControl: func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrEnvironmentUnavailable },
	})
	if err != nil {
		t.Fatal(err)
	}
	record := RepositoryVMRecord{Owner: "operator", RepositoryKey: "repo", VMID: "vm", Generation: 1, Endpoint: filepath.Join(root, "guest.sock"), RootFSPath: filepath.Join(root, "rootfs"), AuthorityDigest: "authority"}
	instance := &rollbackRuntimeInstance{}
	runtime.vms[record.VMID] = &repositoryRuntimeGeneration{record: record, instance: instance}
	other := record
	other.Generation++
	if err := runtime.Abort(t.Context(), other); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("different generation abort = %v, want inconsistency", err)
	}
	if instance.stops != 0 || instance.removes != 0 {
		t.Fatalf("different generation was torn down: stop=%d remove=%d", instance.stops, instance.removes)
	}
	if _, err := runtime.generation(record); err != nil {
		t.Fatalf("healthy generation ownership was erased: %v", err)
	}
}

func TestRepositoryRuntimeStartRejectsUnsafeObjectStoreBeforeBackend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		replaceLate bool
	}{
		{name: "symlink"},
		{name: "replacement race", replaceLate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(root, "repository.git")
			objects := filepath.Join(common, "objects")
			if err := os.MkdirAll(objects, 0o700); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if !tc.replaceLate {
				if err := os.Remove(objects); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, objects); err != nil {
					t.Fatal(err)
				}
			}

			provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
			if tc.replaceLate {
				provider.startHook = func() error {
					if err := os.Rename(objects, objects+".replaced"); err != nil {
						return err
					}
					return os.Symlink(target, objects)
				}
			}
			backend := &rollbackRuntimeBackend{instance: &rollbackRuntimeInstance{}}
			runtime, err := NewRepositoryRuntime(RepositoryRuntimeConfig{
				Backend: backend,
				Network: NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{}),
				DialGuest: func(context.Context, string) (io.ReadWriteCloser, error) {
					return nil, ErrEnvironmentUnavailable
				},
				DialControl: func(context.Context, string) (io.ReadWriteCloser, error) {
					return nil, ErrEnvironmentUnavailable
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			authority, err := newRepositoryBootAuthority()
			if err != nil {
				t.Fatal(err)
			}
			record := RepositoryVMRecord{
				Owner: "operator", RepositoryKey: "repo", GitCommonDirectory: common,
				VMID: "vm", Generation: 1, Endpoint: filepath.Join(root, "guest.sock"), RootFSPath: filepath.Join(root, "rootfs"),
			}
			_, startErr := runtime.Start(t.Context(), record, VerifiedArtifacts{}, authority)
			if !tc.replaceLate {
				if startErr == nil {
					t.Fatal("unsafe repository object store was accepted")
				}
				if backend.starts != 0 {
					t.Fatalf("backend Start calls = %d, want 0", backend.starts)
				}
				return
			}
			if backend.starts != 1 {
				t.Fatalf("backend Start calls = %d, want private snapshot launch", backend.starts)
			}
			mounted := backend.launch.Mounts[1].HostPath
			if mounted == objects || mounted == target || !strings.HasPrefix(mounted, root+string(filepath.Separator)) {
				t.Fatalf("backend received caller-controlled object path %q", mounted)
			}
		})
	}
}

func TestRepositoryRuntimeStartRollsBackEveryOwnedAcquisition(t *testing.T) {
	for _, tc := range []struct {
		stage              string
		networkDirConflict bool
		retainsOwnership   bool
	}{
		{stage: "network"},
		{stage: "network-dir", networkDirConflict: true},
		{stage: "backend"},
		{stage: "wait"},
		{stage: "status"},
		{stage: "authenticated-readiness"},
		{stage: "stop"},
		{stage: "remove", retainsOwnership: true},
		{stage: "wedged-remove", retainsOwnership: true},
		{stage: "cancelled-rollback"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			endpoint := "guest.sock"
			provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
			if tc.stage == "network" {
				provider.socket = ""
			}
			if tc.networkDirConflict {
				if err := os.WriteFile(endpoint+".network", []byte("conflict"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			instance := &rollbackRuntimeInstance{stage: tc.stage}
			backend := &rollbackRuntimeBackend{stage: tc.stage, instance: instance}
			runtime, err := NewRepositoryRuntime(RepositoryRuntimeConfig{
				Backend: backend, Network: NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{}),
				GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, UnixEndpoint: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.stage == "wedged-remove" {
				runtime.rollbackTimeout = time.Nanosecond
			}
			authority, err := newRepositoryBootAuthority()
			if err != nil {
				t.Fatal(err)
			}
			common := filepath.Join(root, "repository.git")
			if err := os.MkdirAll(filepath.Join(common, "objects"), 0o700); err != nil {
				t.Fatal(err)
			}
			record := RepositoryVMRecord{Owner: "operator", RepositoryKey: "repo", GitCommonDirectory: common, VMID: "vm", Generation: 1, Endpoint: endpoint, RootFSPath: filepath.Join(root, "rootfs")}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			instance.cancelParent = cancel
			defer cancel()
			_, startErr := runtime.Start(ctx, record, VerifiedArtifacts{}, authority)
			if startErr == nil {
				t.Fatalf("%s failure was accepted", tc.stage)
			}
			if tc.stage == "stop" && !strings.Contains(startErr.Error(), "injected stop failure") {
				t.Fatalf("stop cleanup failure not joined: %v", startErr)
			}
			if tc.stage == "remove" && !strings.Contains(startErr.Error(), "injected remove failure") {
				t.Fatalf("remove cleanup failure not joined: %v", startErr)
			}
			if tc.stage == "wedged-remove" && !errors.Is(startErr, context.DeadlineExceeded) {
				t.Fatalf("wedged cleanup was not bounded: %v", startErr)
			}
			if tc.stage == "cancelled-rollback" && (!instance.rollbackDetached || !instance.rollbackBounded) {
				t.Fatalf("rollback context detached=%t bounded=%t", instance.rollbackDetached, instance.rollbackBounded)
			}
			wantNetworkStops := 1
			if tc.networkDirConflict || tc.retainsOwnership {
				wantNetworkStops = 0
			}
			if tc.stage != "network-dir" && provider.stops != wantNetworkStops {
				t.Fatalf("network stops = %d, want %d", provider.stops, wantNetworkStops)
			}
			if tc.stage != "backend" && tc.stage != "network" && !tc.networkDirConflict && (instance.stops != 1 || instance.removes != 1) {
				t.Fatalf("instance cleanup = stop %d remove %d, want 1/1", instance.stops, instance.removes)
			}
			if tc.retainsOwnership {
				if _, statErr := os.Lstat(endpoint); statErr != nil {
					t.Fatalf("failed rollback lost owned endpoint: %v", statErr)
				}
				if _, statErr := os.Lstat(endpoint + ".network"); statErr != nil {
					t.Fatalf("failed rollback lost owned network directory: %v", statErr)
				}
				if _, err := runtime.generation(record); err != nil {
					t.Fatalf("failed rollback erased generation ownership: %v", err)
				}
				return
			}
			if _, statErr := os.Lstat(endpoint); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rollback retained %s: %v", endpoint, statErr)
			}
			if tc.networkDirConflict {
				if content, err := os.ReadFile(endpoint + ".network"); err != nil || string(content) != "conflict" {
					t.Fatalf("rollback did not preserve network conflict: %q, %v", content, err)
				}
			} else if _, statErr := os.Lstat(endpoint + ".network"); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rollback retained %s: %v", endpoint+".network", statErr)
			}
			if _, ok := runtime.listeners[endpoint]; ok {
				t.Fatal("rollback retained listener ownership")
			}
			if _, err := runtime.generation(record); !errors.Is(err, ErrEnvironmentUnavailable) {
				t.Fatalf("rollback retained generation: %v", err)
			}
			if snapshots, err := filepath.Glob(filepath.Join(root, ".git-objects-*")); err != nil || len(snapshots) != 0 {
				t.Fatalf("rollback retained Git object snapshots %v: %v", snapshots, err)
			}
		})
	}
}

type rollbackRuntimeBackend struct {
	stage    string
	instance *rollbackRuntimeInstance
	starts   int
	launch   GoMicroVMLaunch
}

func (b *rollbackRuntimeBackend) Start(_ context.Context, launch GoMicroVMLaunch) (GoMicroVMInstance, error) {
	b.starts++
	b.launch = launch
	if b.stage == "backend" {
		return nil, errors.New("injected backend failure")
	}
	return b.instance, nil
}

type rollbackRuntimeInstance struct {
	stage                             string
	stops, removes                    int
	cancelParent                      context.CancelFunc
	rollbackDetached, rollbackBounded bool
}

func (i *rollbackRuntimeInstance) WaitReady(context.Context) error {
	switch i.stage {
	case "wait", "stop", "remove", "wedged-remove":
		return errors.New("injected wait failure")
	case "cancelled-rollback":
		i.cancelParent()
		return errors.New("injected wait failure after cancellation")
	}
	return nil
}
func (i *rollbackRuntimeInstance) Status(context.Context) (RuntimeStatus, error) {
	if i.stage == "status" {
		return RuntimeStatus{}, errors.New("injected status failure")
	}
	return RuntimeStatus{Live: true, VMID: "vm", Generation: 1}, nil
}
func (i *rollbackRuntimeInstance) Stop(ctx context.Context) error {
	i.stops++
	if i.stage == "cancelled-rollback" {
		i.rollbackDetached = ctx.Err() == nil
		_, i.rollbackBounded = ctx.Deadline()
	}
	if i.stage == "stop" {
		return errors.New("injected stop failure")
	}
	return nil
}

func (i *rollbackRuntimeInstance) Remove(ctx context.Context) error {
	i.removes++
	switch i.stage {
	case "remove":
		return errors.New("injected remove failure")
	case "wedged-remove":
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestRepositoryProductionReadinessRequiresAuthenticatedGuestStartupAfterIPv6Disablement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	backend := &repositoryCompositionBackend{omitGuestServer: true}
	provider := &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")}
	runtime, err := NewRepositoryRuntime(RepositoryRuntimeConfig{
		Backend:     backend,
		Network:     NewNetworkController(func() gomicrovmnet.Provider { return provider }, &fakeGuestNetwork{}),
		GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll},
		DialGuest:   backend.dialData, DialControl: backend.dialControl,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := newRepositoryBootAuthority()
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "repository.git")
	if err := os.MkdirAll(filepath.Join(common, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := RepositoryVMRecord{
		Owner: "operator", RepositoryKey: "repository-key", GitCommonDirectory: common, VMID: "repository-vm", Generation: 7,
		Endpoint: filepath.Join(root, "guest.sock"), RootFSPath: filepath.Join(root, "rootfs"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := runtime.Start(ctx, record, VerifiedArtifacts{}, authority); err == nil ||
		!strings.Contains(err.Error(), "authenticated repository guest startup and IPv6 disablement") {
		t.Fatalf("readiness without post-enforcement guest proof = %v", err)
	}
	if provider.stops != 1 {
		t.Fatalf("failed readiness left network provider running: stops=%d", provider.stops)
	}
	if _, err := runtime.generation(record); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("failed readiness retained generation: %v", err)
	}
}

type repositoryCompositionBackend struct {
	mu              sync.Mutex
	starts          int
	launch          GoMicroVMLaunch
	server          *guestagent.RepositoryServer
	status          RuntimeStatus
	omitGuestServer bool
	attackFirst     bool
	attackerPayload chan []byte
	attackerClosed  chan struct{}
}

func (b *repositoryCompositionBackend) Start(_ context.Context, launch GoMicroVMLaunch) (GoMicroVMInstance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	b.launch = launch
	if len(launch.Mounts) != 2 || launch.Mounts[0].Tag != repositoryMountTag ||
		launch.Mounts[1].Tag != repositoryObjectMountTag || !launch.Mounts[1].ReadOnly {
		return nil, errors.New("repository launch contract missing")
	}
	hostMount := launch.Mounts[0].HostPath
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // test process identity
	contract := guestexec.DefaultRuntimeContract()
	contract.Identity = identity
	server, err := guestagent.NewRepositoryServer(guestagent.RepositoryServerConfig{
		Owner: launch.RepositoryOwner, RepositoryKey: launch.RepositoryKey, VMID: launch.VMID, Endpoint: launch.Endpoint,
		Generation: launch.Generation, AuthorityKey: launch.CapabilityKey, WorkloadIdentity: identity, RuntimeContract: contract,
		ResolveRoot: func(guestRoot string) (string, error) {
			rel, err := filepath.Rel(RepositoryGuestMountRoot, guestRoot)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				return "", errors.New("invalid guest root")
			}
			return filepath.Join(hostMount, filepath.FromSlash(rel)), nil
		},
	})
	if err != nil {
		return nil, err
	}
	if !b.omitGuestServer {
		b.server = server
	}
	b.status = RuntimeStatus{Live: true, Generation: launch.Generation, VMID: launch.VMID, PID: 4242, ProcessIdentity: "fake-hypervisor-boot", Endpoint: launch.Endpoint}
	return repositoryCompositionInstance{status: b.status}, nil
}

func (b *repositoryCompositionBackend) dialControl(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
	return b.dial(ctx)
}
func (b *repositoryCompositionBackend) dialData(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
	return b.dial(ctx)
}
func (b *repositoryCompositionBackend) dial(ctx context.Context) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	server := b.server
	attack := b.attackFirst
	b.attackFirst = false
	attackerPayload := b.attackerPayload
	attackerClosed := b.attackerClosed
	b.mu.Unlock()
	if server == nil {
		return nil, errors.New("repository guest is not booted")
	}
	host, guest := net.Pipe()
	if attack {
		go func() {
			defer guest.Close()
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			var challenge json.RawMessage
			if err := codec.Read(guest, &challenge); err == nil {
				attackerPayload <- append([]byte(nil), challenge...)
				var ignored json.RawMessage
				if err := codec.Read(guest, &ignored); err != nil {
					attackerClosed <- struct{}{}
				}
			}
		}()
		return host, nil
	}
	go func() {
		_ = server.ServeAuthenticated(ctx, guest)
		_ = guest.Close()
	}()
	return host, nil
}

type repositoryCompositionInstance struct{ status RuntimeStatus }

func (repositoryCompositionInstance) WaitReady(context.Context) error { return nil }
func (i repositoryCompositionInstance) Status(context.Context) (RuntimeStatus, error) {
	return i.status, nil
}
func (repositoryCompositionInstance) Stop(context.Context) error   { return nil }
func (repositoryCompositionInstance) Remove(context.Context) error { return nil }
