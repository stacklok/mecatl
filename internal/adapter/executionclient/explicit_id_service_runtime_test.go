package executionclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type initialCreateBarrierStore struct {
	*memstore.Store
	target        session.SessionID
	entered       int
	mu            sync.Mutex
	release       chan struct{}
	winnerMode    session.PermissionMode
	persisted     chan struct{}
	releaseCommit chan struct{}
}

func (s *initialCreateBarrierStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if id == s.target {
		s.mu.Lock()
		if s.entered < 2 {
			s.entered++
			if s.entered == 2 {
				close(s.release)
			}
			gate := s.release
			s.mu.Unlock()
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			s.mu.Unlock()
		}
	}
	return s.Store.Load(ctx, id)
}

func (s *initialCreateBarrierStore) Create(ctx context.Context, sess *session.Session) error {
	if sess.ID != s.target || s.persisted == nil {
		return s.Store.Create(ctx, sess)
	}
	if sess.Mode != s.winnerMode {
		select {
		case <-s.persisted:
		case <-ctx.Done():
			return ctx.Err()
		}
		return s.Store.Create(ctx, sess)
	}
	if err := s.Store.Create(ctx, sess); err != nil {
		return err
	}
	close(s.persisted)
	select {
	case <-s.releaseCommit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestServiceExplicitIDConcurrentMismatchCannotAbortPersistedWinner(t *testing.T) {
	for _, sharedEngine := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared_engine", false: "per_session_engine"}[sharedEngine], func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
			controllerStore := executioncontroller.NewStore(dyn, "ns", explicitIDProfiles(t), &recordingExecutor{})
			fixture := startFixture(t, controllerStore, nil)
			defer fixture.stop()
			client, err := New(fixture.endpoint, fixture.clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			provider, err := NewProvider(client, "coding")
			if err != nil {
				t.Fatal(err)
			}
			stopReady := make(chan struct{})
			readyDone := make(chan struct{})
			go func() {
				defer close(readyDone)
				publishCreatedEnvironmentsReady(dyn, stopReady)
			}()
			defer func() { close(stopReady); <-readyDone }()

			const id session.SessionID = "sched--remote-explicit-mismatch"
			store := &initialCreateBarrierStore{
				Store: memstore.New(), target: id, release: make(chan struct{}), winnerMode: session.ModeDefault,
				persisted: make(chan struct{}), releaseCommit: make(chan struct{}),
			}
			engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
			newService := func() *server.Service {
				cfg := server.Config{
					Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", OwnershipEnforced: true,
					SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
						return server.SessionEngineResult{Engine: engine}, nil
					},
				}
				if sharedEngine {
					cfg.SharedEngineRoot = remoteRoot
				}
				svc, svcErr := server.NewService(cfg)
				if svcErr != nil {
					t.Fatal(svcErr)
				}
				return svc
			}
			winnerSvc, loserSvc := newService(), newService()
			defer winnerSvc.Close()
			defer loserSvc.Close()
			principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
			ctx, cancel := context.WithTimeout(session.WithPrincipal(context.Background(), principal), 10*time.Second)
			defer cancel()
			type result struct {
				sess *session.Session
				err  error
			}
			winnerDone := make(chan result, 1)
			loserDone := make(chan result, 1)
			go func() {
				sess, createErr := winnerSvc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
				winnerDone <- result{sess: sess, err: createErr}
			}()
			go func() {
				sess, createErr := loserSvc.CreateSessionWithProfile(ctx, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
				loserDone <- result{sess: sess, err: createErr}
			}()

			loser := <-loserDone
			if !errors.Is(loser.err, server.ErrInvalidArgument) {
				t.Fatalf("mismatched loser error=%v, want original ErrInvalidArgument", loser.err)
			}
			if loser.sess != nil {
				t.Fatalf("mismatched loser returned stale session %+v", loser.sess)
			}
			intents, err := client.ListReferenceIntents(ctx, executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject})
			if err != nil || len(intents) != 1 || intents[0].State != executionenv.ReferencePendingCreate {
				t.Fatalf("pending intents after loser close=%+v err=%v, want one retained PendingCreate", intents, err)
			}
			close(store.releaseCommit)
			winner := <-winnerDone
			if winner.err != nil || winner.sess == nil {
				t.Fatalf("winner=(%+v,%v), want success", winner.sess, winner.err)
			}
			attached, err := client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{
				Environment: executionenv.EnvironmentRef{ID: winner.sess.EnvironmentRef.ID, Revision: winner.sess.EnvironmentRef.Revision},
				Owner:       executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject}, BindingID: string(id),
			}, Purpose: executionenv.PurposeSession})
			if err != nil || !attached.Ready || toSessionRef(attached.Environment) != winner.sess.EnvironmentRef {
				t.Fatalf("published attach=(%+v,%v), want exact winner ref %+v", attached, err, winner.sess.EnvironmentRef)
			}
		})
	}
}

func TestServiceExplicitIDPrepublicationFailureCannotAbortPersistedWinner(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	controllerStore := executioncontroller.NewStore(dyn, "ns", explicitIDProfiles(t), &recordingExecutor{})
	fixture := startFixture(t, controllerStore, nil)
	defer fixture.stop()
	client, err := New(fixture.endpoint, fixture.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider, err := NewProvider(client, "coding")
	if err != nil {
		t.Fatal(err)
	}
	stopReady := make(chan struct{})
	readyDone := make(chan struct{})
	go func() { defer close(readyDone); publishCreatedEnvironmentsReady(dyn, stopReady) }()
	defer func() { close(stopReady); <-readyDone }()

	const id session.SessionID = "sched--remote-explicit-prepublish"
	store := &initialCreateBarrierStore{
		Store: memstore.New(), target: id, release: make(chan struct{}), winnerMode: session.ModeDefault,
		persisted: make(chan struct{}), releaseCommit: make(chan struct{}),
	}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	factoryErr := errors.New("factory rejected loser")
	newService := func() *server.Service {
		svc, svcErr := server.NewService(server.Config{
			Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", OwnershipEnforced: true,
			SessionEngine: func(ctx context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
				if mode == session.ModePlan {
					select {
					case <-store.persisted:
					case <-ctx.Done():
						return server.SessionEngineResult{}, ctx.Err()
					}
					return server.SessionEngineResult{}, factoryErr
				}
				return server.SessionEngineResult{Engine: engine}, nil
			},
		})
		if svcErr != nil {
			t.Fatal(svcErr)
		}
		return svc
	}
	winnerSvc, loserSvc := newService(), newService()
	defer winnerSvc.Close()
	defer loserSvc.Close()
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx, cancel := context.WithTimeout(session.WithPrincipal(context.Background(), principal), 10*time.Second)
	defer cancel()
	type result struct {
		sess *session.Session
		err  error
	}
	winnerDone := make(chan result, 1)
	go func() {
		created, createErr := winnerSvc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
		winnerDone <- result{sess: created, err: createErr}
	}()
	loser, loserErr := loserSvc.CreateSessionWithProfile(ctx, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
	if !errors.Is(loserErr, server.ErrInvalidArgument) || !strings.Contains(loserErr.Error(), "different request") {
		t.Fatalf("prepublication loser error=%v, want persisted-winner request mismatch", loserErr)
	}
	if loser != nil {
		t.Fatalf("prepublication loser returned stale session %+v", loser)
	}
	intents, err := client.ListReferenceIntents(ctx, executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject})
	if err != nil || len(intents) != 1 || intents[0].State != executionenv.ReferencePendingCreate {
		t.Fatalf("pending intents after loser close=%+v err=%v, want one retained PendingCreate", intents, err)
	}
	close(store.releaseCommit)
	winner := <-winnerDone
	if winner.err != nil || winner.sess == nil {
		t.Fatalf("winner=(%+v,%v), want success", winner.sess, winner.err)
	}
	attached, err := client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{
		Environment: executionenv.EnvironmentRef{ID: winner.sess.EnvironmentRef.ID, Revision: winner.sess.EnvironmentRef.Revision},
		Owner:       executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject}, BindingID: string(id),
	}, Purpose: executionenv.PurposeSession})
	if err != nil || !attached.Ready || toSessionRef(attached.Environment) != winner.sess.EnvironmentRef {
		t.Fatalf("published attach=(%+v,%v), want exact winner ref %+v", attached, err, winner.sess.EnvironmentRef)
	}
}

func TestServiceExplicitIDRetryReusesRemoteAllocationAcrossRestartAndConcurrency(t *testing.T) {
	profiles := explicitIDProfiles(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	controllerStore := executioncontroller.NewStore(dyn, "ns", profiles, &recordingExecutor{})
	fixture := startFixture(t, controllerStore, nil)
	defer fixture.stop()
	client, err := New(fixture.endpoint, fixture.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider, err := NewProvider(client, "coding")
	if err != nil {
		t.Fatal(err)
	}
	stopReady := make(chan struct{})
	readyDone := make(chan struct{})
	go func() {
		defer close(readyDone)
		publishCreatedEnvironmentsReady(dyn, stopReady)
	}()
	defer func() {
		close(stopReady)
		<-readyDone
	}()

	const id session.SessionID = "sched--remote-explicit-retry"
	store := &initialCreateBarrierStore{Store: memstore.New(), target: id, release: make(chan struct{})}
	newService := func() *server.Service {
		engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
		svc, svcErr := server.NewService(server.Config{
			Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", OwnershipEnforced: true,
			SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
				return server.SessionEngineResult{Engine: engine}, nil
			},
		})
		if svcErr != nil {
			t.Fatal(svcErr)
		}
		return svc
	}
	alice := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx, cancel := context.WithTimeout(session.WithPrincipal(context.Background(), alice), 10*time.Second)
	defer cancel()
	type createResult struct {
		session *session.Session
		err     error
	}
	createConcurrently := func(services []*server.Service) []*session.Session {
		t.Helper()
		out := make(chan createResult, len(services))
		for _, svc := range services {
			go func() {
				created, createErr := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
				out <- createResult{session: created, err: createErr}
			}()
		}
		results := make([]*session.Session, 0, len(services))
		for range services {
			select {
			case result := <-out:
				if result.err != nil || result.session == nil {
					t.Fatalf("concurrent create=(%+v,%v), want success", result.session, result.err)
				}
				results = append(results, result.session)
			case <-ctx.Done():
				t.Fatalf("concurrent create did not finish: %v", ctx.Err())
			}
		}
		return results
	}

	initialServices := []*server.Service{newService(), newService()}
	initial := createConcurrently(initialServices)
	for _, svc := range initialServices {
		svc.Close()
	}
	first := initial[0]
	if initial[1].EnvironmentRef != first.EnvironmentRef {
		t.Fatalf("initial creates diverged: first=%+v second=%+v", first.EnvironmentRef, initial[1].EnvironmentRef)
	}

	services := []*server.Service{newService(), newService()}
	defer services[0].Close()
	defer services[1].Close()
	results := createConcurrently(services)
	for i := range results {
		if results[i].EnvironmentRef != first.EnvironmentRef {
			t.Fatalf("identical retry %d=%+v, want same ref %+v", i, results[i].EnvironmentRef, first.EnvironmentRef)
		}
	}
	list, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("allocations=%d err=%v, want one", len(list.Items), err)
	}
	allocationBefore := list.Items[0].DeepCopy()

	bobCtx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	if _, err := services[0].CreateSessionWithProfile(bobCtx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); err == nil {
		t.Fatal("changed owner adopted explicit-ID session")
	}
	if _, err := services[0].CreateSessionWithProfile(ctx, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed mode error=%v, want ErrInvalidArgument", err)
	}
	if _, err := services[0].CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{MaxTurns: 1}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed limits error=%v, want ErrInvalidArgument", err)
	}
	if _, err := services[0].CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithSessionID(id)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("changed profile error=%v, want ErrInvalidArgument", err)
	}
	afterList, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(afterList.Items) != 1 {
		t.Fatalf("rejected retries changed allocation count to %d: %v", len(afterList.Items), err)
	}
	after, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), allocationBefore.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	allocationBefore.SetResourceVersion(after.GetResourceVersion())
	if allocationBefore.GetUID() != after.GetUID() || !reflect.DeepEqual(allocationBefore.Object["spec"], after.Object["spec"]) {
		t.Fatal("rejected retry adopted a different allocation identity")
	}
}

func explicitIDProfiles(t *testing.T) *executioncontroller.Profiles {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := []byte("profiles:\n  coding:\n    image: example.test/executor@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    storageClass: standard\n    storageSize: 1Gi\n    cpuRequest: 100m\n    memoryRequest: 128Mi\n    cpuLimit: 1\n    memoryLimit: 1Gi\n    ephemeralStorageRequest: 256Mi\n    ephemeralStorageLimit: 1Gi\n    tmpSizeLimit: 128Mi\n    runtimeClassName: gvisor\n    maxFileBytes: 5242880\n    maxCommandBytes: 1048576\n    maxCommandDuration: 1m\n    maxEnvironments: 10\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := executioncontroller.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	return profiles
}

func publishCreatedEnvironmentsReady(client *dynamicfake.FakeDynamicClient, stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			list, err := client.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				continue
			}
			for i := range list.Items {
				o := list.Items[i].DeepCopy()
				_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": "executor"}, "status", "pod")
				_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
				_, _ = client.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(context.Background(), o, metav1.UpdateOptions{})
			}
		}
	}
}
