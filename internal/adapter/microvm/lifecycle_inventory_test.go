package microvm

import (
	"context"
	"encoding/json"
	"net"
	"testing"
)

func TestMicroVMLifecycleUX_ClientInventoryAndDeleteStayGenerationBound(t *testing.T) {
	t.Parallel()
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	requests := make(chan lifecycleRequest, 3)
	go func() {
		for i := 0; i < 3; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request lifecycleRequest
			if readFrame(conn, &request) == nil {
				requests <- request
				switch request.Operation {
				case "inventory":
					var inventoryRequest InventoryRequest
					_ = json.Unmarshal(request.Payload, &inventoryRequest)
					entry := InventoryEntry{Owner: "local", SessionID: "s1", EnvironmentID: "env-1", Ref: "env-1@7", Generation: 7, WorktreePath: "/worktrees/s1", Health: GenerationHealthy}
					page := InventoryPage{Entries: []InventoryEntry{entry}}
					if inventoryRequest.Continuation == "" {
						page.Continuation = "opaque-next"
					}
					payload, _ := json.Marshal(page)
					_ = writeFrame(conn, lifecycleResponse{Payload: payload})
				case "delete":
					payload, _ := json.Marshal(DeleteResult{WorktreePath: "/worktrees/s1", WorktreeRetained: true})
					_ = writeFrame(conn, lifecycleResponse{Payload: payload})
				}
			}
			_ = conn.Close()
		}
	}()

	client, err := New("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Inventory(context.Background(), "local")
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Generation != 7 || page.Continuation != "opaque-next" {
		t.Fatalf("Inventory() = %+v, %v", page, err)
	}
	lastPage, err := client.Inventory(context.Background(), "local", InventoryRequest{PageSize: 1, Continuation: page.Continuation})
	if err != nil || len(lastPage.Entries) != 1 || lastPage.Continuation != "" {
		t.Fatalf("Inventory(next) = %+v, %v", lastPage, err)
	}
	claim := GenerationBinding{Owner: "local", SessionID: "s1", EnvironmentID: "env-1", Ref: "env-1@7", Generation: 7}
	deleted, err := client.DeleteGeneration(context.Background(), claim)
	if err != nil || !deleted.WorktreeRetained || deleted.WorktreePath != "/worktrees/s1" {
		t.Fatalf("DeleteGeneration() = %+v, %v", deleted, err)
	}

	firstInventoryRequest, nextInventoryRequest, deleteRequest := <-requests, <-requests, <-requests
	if firstInventoryRequest.Binding != (binding{Owner: "local"}) || nextInventoryRequest.Binding != (binding{Owner: "local"}) {
		t.Fatalf("owner scope drifted: inventory=%+v next=%+v", firstInventoryRequest.Binding, nextInventoryRequest.Binding)
	}
	var nextPageRequest InventoryRequest
	if err := json.Unmarshal(nextInventoryRequest.Payload, &nextPageRequest); err != nil || nextPageRequest.Continuation != "opaque-next" || nextPageRequest.PageSize != 1 {
		t.Fatalf("next inventory request = %+v, %v", nextPageRequest, err)
	}
	if deleteRequest.Binding != (binding(claim)) {
		t.Fatalf("delete binding = %+v, want %+v", deleteRequest.Binding, claim)
	}
}
