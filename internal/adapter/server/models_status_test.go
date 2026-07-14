package server_test

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func cannedProviderStatus() []*mecatlv1.ProviderStatus {
	return []*mecatlv1.ProviderStatus{
		{ProviderId: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}
}

// TestGRPCListModelsCarriesProviderStatus proves ListModelsResponse.provider_status
// (issue #262) is threaded through the gRPC surface alongside models.
func TestGRPCListModelsCarriesProviderStatus(t *testing.T) {
	svc := modelsService(t, cannedModels())
	svc.SetProviderStatus(cannedProviderStatus())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	status := resp.GetProviderStatus()
	if len(status) != 1 {
		t.Fatalf("provider_status = %d rows, want 1", len(status))
	}
	if status[0].GetProviderId() != "toolhive" || status[0].GetState() != "unreachable" || status[0].GetHint() == "" {
		t.Fatalf("provider_status[0] = %+v", status[0])
	}
}

// TestHTTPListModelsCarriesProviderStatus is the HTTP twin.
func TestHTTPListModelsCarriesProviderStatus(t *testing.T) {
	svc := modelsService(t, cannedModels())
	svc.SetProviderStatus(cannedProviderStatus())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.ListModelsResponse
	if code := httpGet(t, srv, "/v1/models", &resp); code != 200 {
		t.Fatalf("GET /v1/models status = %d", code)
	}
	if len(resp.GetProviderStatus()) != 1 || resp.GetProviderStatus()[0].GetProviderId() != "toolhive" {
		t.Fatalf("http provider_status = %+v", resp.GetProviderStatus())
	}
}

// TestListModelsProviderStatus_EmptyByDefault proves a Service with no
// SetProviderStatus call ever made returns an empty (never nil-panicking)
// provider_status slice — the byte-identical default for every deployment
// without an intent-driven provider.
func TestListModelsProviderStatus_EmptyByDefault(t *testing.T) {
	svc := modelsService(t, cannedModels())
	if got := svc.ProviderStatuses(); len(got) != 0 {
		t.Fatalf("ProviderStatuses() = %+v, want empty", got)
	}
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	resp, err := client.ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.GetProviderStatus()) != 0 {
		t.Fatalf("provider_status = %+v, want empty", resp.GetProviderStatus())
	}
}

// TestSetProviderStatus_NilStoresEmpty mirrors SetModels' nil-safety: a nil
// argument stores an empty, non-nil slice.
func TestSetProviderStatus_NilStoresEmpty(t *testing.T) {
	svc := modelsService(t, cannedModels())
	svc.SetProviderStatus(cannedProviderStatus())
	if len(svc.ProviderStatuses()) != 1 {
		t.Fatal("setup: expected one status row before the nil swap")
	}
	svc.SetProviderStatus(nil)
	if got := svc.ProviderStatuses(); got == nil || len(got) != 0 {
		t.Fatalf("ProviderStatuses() after nil swap = %+v, want an empty non-nil slice", got)
	}
}

// TestListModels_InvokesRefresher proves ListModels calls the installed
// refresher (issue #262, R1.4) before returning its snapshot; a nil refresher
// (the default) never calls anything.
func TestListModels_InvokesRefresher(t *testing.T) {
	svc := modelsService(t, cannedModels())

	var calls atomic.Int32
	svc.SetModelsRefresher(func(context.Context) { calls.Add(1) })

	_ = svc.ListModels(context.Background())
	_ = svc.ListModels(context.Background())
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresher calls = %d, want 2 (once per ListModels call)", got)
	}
}

// TestListModels_NilRefresherIsNoOp proves the DEFAULT (no SetModelsRefresher
// call) leaves ListModels a pure snapshot read — byte-identical to every
// deployment without this feature.
func TestListModels_NilRefresherIsNoOp(t *testing.T) {
	svc := modelsService(t, cannedModels())
	got := svc.ListModels(context.Background()) // must not panic with no refresher installed
	if len(got) != 2 {
		t.Fatalf("ListModels() = %d models, want 2", len(got))
	}
}
