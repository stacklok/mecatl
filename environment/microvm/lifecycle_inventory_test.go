package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
)

func TestMicroVMLifecycleUX_InventoryIsOwnerScopedAndReportsGenerationHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	healthy := readyRecord("healthy", 1)
	healthy.Owner, healthy.SessionID, healthy.WorktreePath = "owner-a", "session-healthy", "/worktrees/healthy"
	stale := readyRecord("stale", 2)
	stale.Owner, stale.SessionID, stale.WorktreePath = "owner-a", "session-stale", "/worktrees/stale"
	broken := readyRecord("broken", 3)
	broken.Owner, broken.SessionID, broken.WorktreePath = "owner-a", "session-broken", "/worktrees/broken"
	foreign := readyRecord("foreign", 4)
	foreign.Owner, foreign.WorktreePath = "owner-b", "/worktrees/foreign"
	retained := readyRecord("retained", 5)
	retained.Owner, retained.SessionID, retained.WorktreePath = "owner-a", "session-retained", "/worktrees/retained"
	retained.State, retained.Tombstone, retained.PreserveWorktree = EnvironmentDestroyed, true, true

	registry := newLifecycleRegistry(healthy)
	for _, record := range []EnvironmentRecord{stale, broken, foreign, retained} {
		if err := registry.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	runtime := inventoryRuntime{statuses: map[string]RuntimeStatus{
		healthy.EnvironmentID: exactRuntimeStatus(healthy),
		stale.EnvironmentID:   {},
	}, errors: map[string]error{broken.EnvironmentID: errors.New("runtime probe failed")}}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: registry, Runtime: runtime, Worktrees: &lifecycleWorktrees{}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	response := daemon.Handle(ctx, server, LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleInventory, Binding: control.Binding{Owner: "owner-a"},
	})
	if response.Err != nil {
		t.Fatal(response.Err)
	}
	var page LifecycleInventoryPage
	if err := json.Unmarshal(response.Payload, &page); err != nil {
		t.Fatal(err)
	}
	entries := page.Entries
	if page.Continuation != "" {
		t.Fatalf("unexpected continuation for short inventory: %q", page.Continuation)
	}
	if len(entries) != 4 {
		t.Fatalf("owner inventory length = %d, want 4: %+v", len(entries), entries)
	}
	want := map[string]LifecycleGenerationHealth{"healthy": GenerationHealthy, "stale": GenerationStale, "broken": GenerationError, "retained": GenerationStale}
	for _, entry := range entries {
		if entry.Owner != "owner-a" || entry.WorktreePath == foreign.WorktreePath {
			t.Fatalf("cross-owner inventory leak: %+v", entry)
		}
		if got := want[entry.EnvironmentID]; got == "" || entry.Health != got || entry.Ref == "" || entry.Generation == 0 || entry.WorktreePath == "" {
			t.Fatalf("inventory entry = %+v, want health %q and exact identity", entry, got)
		}
		if (entry.Health == GenerationHealthy) == (entry.Error != "") {
			t.Fatalf("inventory health detail is not actionable: %+v", entry)
		}
	}

	missingOwner := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInventory})
	if !errors.Is(missingOwner.Err, control.ErrBindingMismatch) {
		t.Fatalf("ownerless inventory error = %v, want binding mismatch", missingOwner.Err)
	}
}

func TestMicroVMLifecycleUX_InventoryPaginationIsCompleteOpaqueAndOwnerBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	registry := newLifecycleRegistry()
	for i := 0; i < 300; i++ {
		record := readyRecord(fmt.Sprintf("env-%03d", i), uint32(i+1))
		record.Owner = "owner-a"
		record.SessionID = fmt.Sprintf("session-%03d", i)
		record.WorktreePath = fmt.Sprintf("/worktrees/%03d", i)
		if i%17 == 0 {
			record.State, record.Tombstone, record.PreserveWorktree = EnvironmentDestroyed, true, true
		}
		if err := registry.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: registry, Runtime: inventoryRuntime{}, Worktrees: &lifecycleWorktrees{}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	readPage := func(owner, token string) (LifecycleInventoryPage, error) {
		payload, marshalErr := json.Marshal(LifecycleInventoryRequest{PageSize: 37, Continuation: token})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInventory, Binding: control.Binding{Owner: owner}, Payload: payload})
		if response.Err != nil {
			return LifecycleInventoryPage{}, response.Err
		}
		if len(response.Payload) >= int(control.DefaultMaxMessageBytes) {
			t.Fatalf("inventory page bytes = %d, exceed message bound", len(response.Payload))
		}
		var page LifecycleInventoryPage
		if err := json.Unmarshal(response.Payload, &page); err != nil {
			t.Fatal(err)
		}
		return page, nil
	}

	var all []LifecycleInventoryEntry
	var token string
	for {
		page, pageErr := readPage("owner-a", token)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(page.Entries) == 0 || len(page.Entries) > 37 {
			t.Fatalf("page length = %d, want 1..37", len(page.Entries))
		}
		all = append(all, page.Entries...)
		if page.Continuation == "" {
			break
		}
		token = page.Continuation
	}
	if len(all) != 300 {
		t.Fatalf("paginated inventory length = %d, want 300", len(all))
	}
	if all[0].SessionID != "session-000" || all[len(all)-1].SessionID != "session-299" {
		t.Fatalf("pagination order drifted: first=%q last=%q", all[0].SessionID, all[len(all)-1].SessionID)
	}
	if all[0].State != EnvironmentDestroyed || all[0].Health != GenerationStale || !strings.Contains(all[0].Error, "dirty worktree retained") {
		t.Fatalf("retained destroyed generation missing from page: %+v", all[0])
	}
	first, err := readPage("owner-a", "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := readPage("owner-a", "")
	if err != nil || first.Continuation == "" || first.Continuation != again.Continuation {
		t.Fatalf("continuation is not deterministic: first=%q again=%q err=%v", first.Continuation, again.Continuation, err)
	}
	tamperedBytes := []byte(first.Continuation)
	if tamperedBytes[len(tamperedBytes)-1] == 'A' {
		tamperedBytes[len(tamperedBytes)-1] = 'B'
	} else {
		tamperedBytes[len(tamperedBytes)-1] = 'A'
	}
	if _, err := readPage("owner-a", string(tamperedBytes)); err == nil {
		t.Fatal("tampered continuation was accepted")
	}
	if _, err := readPage("owner-b", first.Continuation); err == nil {
		t.Fatal("cross-owner continuation was accepted")
	}
	lastRecord, err := registry.Lookup(ctx, all[len(all)-1].EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	deleted := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: bindingForRecord(lastRecord)})
	if deleted.Err != nil {
		t.Fatalf("delete generation beyond former lifetime cap: %v", deleted.Err)
	}
	lastRecord, err = registry.Lookup(ctx, lastRecord.EnvironmentID)
	if err != nil || lastRecord.State != EnvironmentDestroyed {
		t.Fatalf("last generation was not deletable: %+v, %v", lastRecord, err)
	}
}

func TestMicroVMLifecycleUX_DeleteReportsRetainedDirtyWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record := readyRecord("dirty", 8)
	record.Owner, record.SessionID, record.WorktreePath = "owner-a", "session-dirty", "/worktrees/dirty"
	registry := newLifecycleRegistry(record)
	runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: registry, Runtime: runtime, Worktrees: &lifecycleWorktrees{dirty: true}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: bindingForRecord(record)})
	if response.Err != nil {
		t.Fatal(response.Err)
	}
	var result LifecycleDeleteResult
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRetained || result.WorktreePath != record.WorktreePath {
		t.Fatalf("delete result = %+v", result)
	}
	stored, err := registry.Lookup(ctx, record.EnvironmentID)
	if err != nil || !stored.PreserveWorktree || !stored.WorktreeDeleted {
		t.Fatalf("dirty retention checkpoint = %+v, %v", stored, err)
	}
}

func TestMicroVMLifecycleUX_ReconcileNeverCreatesMissingGeneration(t *testing.T) {
	t.Parallel()
	record := readyRecord("existing", 1)
	record.Owner = "owner-a"
	registry := newLifecycleRegistry(record)
	creator := &daemonTestCreator{registry: registry, record: readyRecord("replacement", 2)}
	reconciler := &recordingReconciler{}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{
		Control: auth, Creator: creator, Registry: registry,
		Runtime:   &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}},
		Worktrees: &lifecycleWorktrees{}, Reconciler: reconciler,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	response := daemon.Handle(context.Background(), server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleReconcile, Binding: control.Binding{Owner: "owner-a"}})
	if response.Err != nil || reconciler.calls != 1 || creator.calls != 0 {
		t.Fatalf("reconcile response=%+v reconcile=%d create=%d", response, reconciler.calls, creator.calls)
	}
}

type recordingReconciler struct{ calls int }

func (r *recordingReconciler) Reconcile(context.Context) error {
	r.calls++
	return nil
}

type inventoryRuntime struct {
	statuses map[string]RuntimeStatus
	errors   map[string]error
}

func (r inventoryRuntime) Inspect(_ context.Context, record EnvironmentRecord) (RuntimeStatus, error) {
	return r.statuses[record.EnvironmentID], r.errors[record.EnvironmentID]
}
func (inventoryRuntime) Reattach(context.Context, EnvironmentRecord) error { return nil }
func (inventoryRuntime) Detach(context.Context, EnvironmentRecord) error   { return nil }
func (inventoryRuntime) Destroy(context.Context, EnvironmentRecord) error  { return nil }
