package executionclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
)

// Keep admission and reference mutations real; control only readiness and lost
// responses, synchronously at the private provider seam (no polling goroutine).
type selectedTemplateBackend struct {
	*executioncontroller.Store
	dyn           *dynamicfake.FakeDynamicClient
	admission     atomic.Pointer[executioncontroller.Store]
	loseCommit    atomic.Bool
	publishCommit bool
}

func (b *selectedTemplateBackend) EnsurePendingOwnedRevision(ctx context.Context, client, hash string, owner executionenv.Owner, binding, id, revision, fp, operation string) (executioncontroller.Allocation, error) {
	allocation, err := b.admission.Load().EnsurePendingOwnedRevision(ctx, client, hash, owner, binding, id, revision, fp, operation)
	if err != nil {
		return allocation, err
	}
	patch := []byte(`{"status":{"pod":{"name":"executor"},"pvc":{"name":"retained-workspace","uid":"retained-uid"},"conditions":[{"type":"Ready","status":"True"}]}}`)
	if _, err := b.dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Patch(ctx, allocation.Environment.ID, types.MergePatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
		return executioncontroller.Allocation{}, err
	}
	return allocation, nil
}

func (b *selectedTemplateBackend) CommitReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	if b.loseCommit.Swap(false) {
		if b.publishCommit {
			if err := b.Store.CommitReference(ctx, ref, client, owner, binding, operation); err != nil {
				return err
			}
		}
		return errors.New("commit response lost")
	}
	return b.Store.CommitReference(ctx, ref, client, owner, binding, operation)
}

type ambiguousSelectedSessionStore struct {
	*memstore.Store
	loseCreate  bool
	unavailable bool
}

func (s *ambiguousSelectedSessionStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if s.unavailable {
		return nil, errors.New("session storage unavailable")
	}
	return s.Store.Load(ctx, id)
}

func (s *ambiguousSelectedSessionStore) Create(ctx context.Context, sess *session.Session) error {
	if err := s.Store.Create(ctx, sess); err != nil {
		return err
	}
	if s.loseCreate {
		s.loseCreate = false
		return errors.New("session publication response lost")
	}
	return nil
}

func selectedTemplateService(t *testing.T, provider *Provider, store *ambiguousSelectedSessionStore, shared bool) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "offline"})
	cfg := server.Config{Engine: engine, Store: store, PlacementProvider: provider, ReferenceIntents: provider, PlacementScope: "test", OwnershipEnforced: true,
		ExecutionTemplateAllowed: func(*session.Principal, string, string) bool { return true },
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine}, nil
		},
	}
	if shared {
		cfg.SharedEngineRoot = remoteRoot
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSelectedTemplateListThenDeprecationRejectsAtAllocationSeam(t *testing.T) {
	profiles := explicitIDProfiles(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	kube := kubefake.NewSimpleClientset()
	backend := &selectedTemplateBackend{Store: executioncontroller.NewStore(dyn, "ns", profiles, nil).WithKubeClient(kube), dyn: dyn}
	backend.admission.Store(backend.Store)
	fixture := startFixture(t, backend, nil)
	defer fixture.stop()
	client, err := New(fixture.endpoint, fixture.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider, err := NewTemplateProvider(client, "coding", profiles.DefaultRevision("coding"))
	if err != nil {
		t.Fatal(err)
	}
	store := &ambiguousSelectedSessionStore{Store: memstore.New()}
	svc := selectedTemplateService(t, provider, store, true)
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	items, _, err := svc.ListExecutionTemplates(ctx)
	if err != nil || len(items) != 1 || !items[0].DeclaredExecutionFiles || !items[0].DeclaredBuiltInShell {
		t.Fatalf("catalog=%+v err=%v", items, err)
	}
	// Reload policy after discovery, preserving the same real allocation store.
	backend.admission.Store(executioncontroller.NewStore(dyn, "ns", explicitIDProfilesWithPolicy(t, true), nil).WithKubeClient(kube))
	selection := server.ExecutionSelection{Kind: server.PlacementSelectorTemplate, ID: items[0].ID, Revision: items[0].Revision}
	created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithExecutionSelection(selection))
	if !errors.Is(err, server.ErrPlacementNotFound) || created != nil {
		t.Fatalf("stale bind=%v, %v", created, err)
	}
	allocations, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(allocations.Items) != 0 {
		t.Fatalf("stale bind allocated: %v, %v", allocations, err)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" {
			t.Fatalf("stale bind reached allocation create: %v", action)
		}
	}
	if len(kube.Actions()) != 0 {
		t.Fatalf("stale bind reached capacity/Pod/PVC seam: %v", kube.Actions())
	}
}

type selectedTemplateExecutor struct{ *executionexecutor.Executor }

func (e selectedTemplateExecutor) Execute(ctx context.Context, _ string, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	return e.Executor.Execute(ctx, q)
}

func TestSelectedTemplateAmbiguousPublicationRetainsReferenceAndRecovers(t *testing.T) {
	for _, shared := range []bool{true, false} {
		for _, failure := range []string{"session_published", "commit_pending", "commit_published"} {
			t.Run(map[bool]string{true: "shared", false: "per_session"}[shared]+"/"+failure, func(t *testing.T) {
				profiles := explicitIDProfiles(t)
				dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
				workspace := t.TempDir()
				if err := os.WriteFile(filepath.Join(workspace, "sentinel"), []byte("retained workspace\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				executor, err := executionexecutor.New(workspace, executionexecutor.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				defer executor.Close()
				backend := &selectedTemplateBackend{Store: executioncontroller.NewStore(dyn, "ns", profiles, selectedTemplateExecutor{executor}), dyn: dyn, publishCommit: failure == "commit_published"}
				backend.admission.Store(backend.Store)
				backend.loseCommit.Store(failure != "session_published")
				fixture := startFixture(t, backend, nil)
				defer fixture.stop()
				client, err := New(fixture.endpoint, fixture.clientTLS)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				provider, err := NewTemplateProvider(client, "coding", profiles.DefaultRevision("coding"))
				if err != nil {
					t.Fatal(err)
				}
				store := &ambiguousSelectedSessionStore{Store: memstore.New(), loseCreate: failure == "session_published"}
				svc := selectedTemplateService(t, provider, store, shared)
				ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
				selection := server.ExecutionSelection{Kind: server.PlacementSelectorTemplate, ID: "coding", Revision: profiles.DefaultRevision("coding")}
				create := func(s *server.Service) (*session.Session, error) {
					return s.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("selected-ambiguous"), server.WithExecutionSelection(selection))
				}
				if _, err := create(svc); !errors.Is(err, server.ErrInternal) {
					t.Fatalf("ambiguous create=%v", err)
				}
				svc.Close()
				persisted, err := store.Load(ctx, "selected-ambiguous")
				if err != nil || persisted.ExecutionTemplateRevision != selection.Revision {
					t.Fatalf("published selection=%v, %v", persisted, err)
				}
				resource := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns")
				before, err := resource.Get(ctx, persisted.EnvironmentRef.ID, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				intents, err := client.ListAllReferenceIntents(ctx)
				wantIntents := 1
				if failure == "commit_published" {
					wantIntents = 0
				}
				if err != nil || len(intents) != wantIntents {
					t.Fatalf("retained pending intents=%v, %v", intents, err)
				}
				restarted := selectedTemplateService(t, provider, store, shared)
				store.unavailable = true
				restarted.ReconcileReferenceIntents(ctx)
				if retained, err := client.ListAllReferenceIntents(ctx); err != nil || len(retained) != wantIntents {
					t.Fatalf("uncertain publication was not retained: %v, %v", retained, err)
				}
				store.unavailable = false
				restarted.ReconcileReferenceIntents(ctx)
				if intents, err := client.ListAllReferenceIntents(ctx); err != nil || len(intents) != 0 {
					t.Fatalf("recovery left intents=%v, %v", intents, err)
				}
				recovered, err := create(restarted)
				if err != nil || recovered.EnvironmentRef != persisted.EnvironmentRef || recovered.ExecutionTemplateRevision != selection.Revision {
					t.Fatalf("recovered=%v, %v", recovered, err)
				}
				after, err := resource.Get(ctx, persisted.EnvironmentRef.ID, metav1.GetOptions{})
				if err != nil || !reflect.DeepEqual(before.Object["spec"], after.Object["spec"]) {
					t.Fatalf("recovery changed allocation: %v", err)
				}
				allocations, err := resource.List(ctx, metav1.ListOptions{})
				if err != nil || len(allocations.Items) != 1 {
					t.Fatalf("recovery allocated replacement: %v, %v", allocations, err)
				}
				if !reflect.DeepEqual(before.Object["status"].(map[string]any)["pvc"], after.Object["status"].(map[string]any)["pvc"]) {
					t.Fatal("recovery changed retained PVC identity")
				}
				owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
				claim, err := client.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: executionenv.EnvironmentRef{ID: recovered.EnvironmentRef.ID, Revision: recovered.EnvironmentRef.Revision}, Owner: owner, BindingID: string(recovered.ID), RunID: "recovered-read", OperationID: "recovered-read", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: string(recovered.ID), RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}
				content, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "sentinel"})
				if err != nil || string(content.Data) != "retained workspace\n" {
					t.Fatalf("recovered workspace lost sentinel: %q, %v", content.Data, err)
				}
				if err := client.ReleaseRun(ctx, executionenv.RunClaimRequest{Environment: claim.Environment, Owner: owner, BindingID: string(recovered.ID), RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationID: "release-recovered"}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
