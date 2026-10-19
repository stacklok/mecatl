package executionclient

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestNativeBorrowedSchedulePublishesDistinctFireReferences(t *testing.T) {
	profiles := explicitIDProfiles(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	controller := executioncontroller.NewStore(dyn, "ns", profiles, &recordingExecutor{})
	fixture := startFixture(t, controller, nil)
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
	stopReady, readyDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(readyDone); publishCreatedEnvironmentsReady(dyn, stopReady) }()
	defer func() { close(stopReady); <-readyDone }()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("done"), mockllm.TextTurn("done"), mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
	svc, err := server.NewService(server.Config{Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "native-test", ExecutionAccess: provider, DefaultLimits: session.Limits{MaxTurns: 5}, OwnershipEnforced: true,
		RetentionPolicy: server.RetentionPolicy{ScheduledMaxCount: 1}, LocalStorageMaintenanceSingleWriter: true, StorageManagementAuthorized: func(_ context.Context) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(t.Context(), owner)
	origin, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("native-origin"))
	if err != nil {
		t.Fatal(err)
	}
	spec := port.ScheduleSpec{Name: "borrowed", Prompt: "say done", Mode: session.ModePlan, Trigger: port.TriggerSpec{OneShot: time.Now().Add(time.Hour)}, OriginSessionID: origin.ID}
	active, err := svc.StartRunContent(ctx, origin.ID, "origin", nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateSchedule(ctx, spec)
	if err != nil {
		t.Fatalf("create while origin running: %v", err)
	}
	if created.Spec.EnvironmentRef != origin.EnvironmentRef || created.Spec.PlacementOwned || created.Spec.PlacementScope != "native-test" {
		t.Fatalf("incorrect borrowed placement: %+v", created.Spec)
	}
	if _, err := svc.ScheduleFirePlacement(ctx, created.Spec, "sched--busy"); err == nil {
		t.Fatal("busy origin reserved a fire reference")
	}
	for range active.Events() {
	}
	svc.FinishRun(origin.ID, active)

	t.Run("LostReservationResponse", func(t *testing.T) {
		before, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, origin.EnvironmentRef.ID, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		source, err := store.Load(ctx, origin.ID)
		if err != nil {
			t.Fatal(err)
		}
		fireCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		const fireID = "sched--lost-reservation"
		var reservation *executionv1.ReserveSuccessorRequest
		rpc := &droppedReservationRPC{ExecutionProviderServiceClient: client.rpc}
		rpc.afterReserve = func(req *executionv1.ReserveSuccessorRequest) {
			reservation = req
			intent, err := client.FindReferenceIntent(ctx, executionenv.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, executionenv.EnvironmentRef{ID: origin.EnvironmentRef.ID, Revision: origin.EnvironmentRef.Revision}, fireID)
			if err != nil || intent.State != executionenv.ReferencePendingCreate || intent.OperationID != req.OperationId || intent.SourceBindingID != string(origin.ID) {
				t.Fatalf("reservation not persisted before response loss: %+v, %v", intent, err)
			}
			cancel()
		}
		client.rpc = rpc
		defer func() { client.rpc = rpc.ExecutionProviderServiceClient }()
		binding, err := svc.ScheduleFirePlacement(fireCtx, created.Spec, fireID)
		if !errors.Is(err, server.ErrPlacementUnavailable) || !reflect.DeepEqual(binding, server.PlacementBinding{}) {
			t.Fatalf("ambiguous reservation returned a binding or hid the error: %v", err)
		}
		if reservation == nil || rpc.lastAbort == nil {
			t.Fatal("persisted reservation was not aborted after response loss")
		}
		wantAbort := &executionv1.ReferenceMutationRequest{Environment: reservation.Environment, Owner: reservation.Owner, BindingId: fireID, OperationId: reservation.OperationId}
		if reservation.SourceBindingId != string(origin.ID) || reservation.DestinationBindingId != fireID || reservation.OperationId == "" || !proto.Equal(rpc.lastAbort, wantAbort) {
			t.Fatal("abort did not target the exact reserved destination, owner, environment, and operation")
		}
		if rpc.abortErr != nil || !rpc.abortBounded || rpc.commits != 0 {
			t.Fatalf("cleanup context error=%v bounded=%t commits=%d", rpc.abortErr, rpc.abortBounded, rpc.commits)
		}
		if _, err := store.Load(ctx, fireID); !errors.Is(err, port.ErrSessionNotFound) {
			t.Fatalf("failed reservation published a session: %v", err)
		}
		after, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, origin.EnvironmentRef.ID, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Object["status"], after.Object["status"]) {
			t.Fatal("pending reference was not reclaimed or source environment status changed")
		}
		unchanged, err := store.Load(ctx, origin.ID)
		if err != nil || !reflect.DeepEqual(source, unchanged) {
			t.Fatalf("source session changed: %v", err)
		}
	})

	foreign := session.WithPrincipal(ctx, &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	if _, err := svc.ScheduleFirePlacement(foreign, created.Spec, "sched--foreign"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("foreign owner: %v", err)
	}
	changed := created.Spec
	changed.PlacementScope = "another-deployment"
	if _, err := svc.ScheduleFirePlacement(ctx, changed, "sched--scope"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("scope: %v", err)
	}
	changed = created.Spec
	changed.EnvironmentRef.Revision = "not-the-origin"
	if _, err := svc.ScheduleFirePlacement(ctx, changed, "sched--wrong-ref"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("ref: %v", err)
	}
	for _, id := range []session.SessionID{"sched--first", "sched--second"} {
		binding, err := svc.ScheduleFirePlacement(ctx, created.Spec, id)
		if err != nil {
			t.Fatalf("reserve %s: %v", id, err)
		}
		fire, err := svc.CreateSessionWithProfile(ctx, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding), server.WithScheduledRelationship(spec.Name, origin.ID))
		if err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
		run, err := svc.StartScheduledRunContent(ctx, fire.ID, "fire", nil)
		if err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		for range run.Events() {
		}
		svc.FinishRun(fire.ID, run)
		svc.CloseSession(fire.ID)
		stored, err := store.Load(ctx, id)
		if err != nil || stored.EnvironmentRef != origin.EnvironmentRef {
			t.Fatalf("fire %s missing exact session: %v", id, err)
		}
	}
	current, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, origin.EnvironmentRef.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, _, err := unstructured.NestedSlice(current.Object, "status", "references")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range refs {
		ref, ok := row.(map[string]any)
		if ok && ref["state"] == "Published" {
			found[ref["bindingID"].(string)] = true
		}
	}
	for _, id := range []string{string(origin.ID), "sched--first", "sched--second"} {
		if !found[id] {
			t.Fatalf("reference %s not published: %v", id, found)
		}
	}
	if _, active, err := unstructured.NestedMap(current.Object, "status", "activeRun"); err != nil || active {
		t.Fatalf("run claim retained after FinishRun: %v, %v", active, err)
	}
	resumed, err := svc.StartRunContent(ctx, origin.ID, "resume", nil)
	if err != nil {
		t.Fatalf("origin not resumable: %v", err)
	}
	for range resumed.Events() {
	}
	svc.FinishRun(origin.ID, resumed)
	moved, err := store.Load(ctx, origin.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved.EnvironmentRef.Revision = "moved-away"
	if err := store.Save(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScheduleFirePlacement(ctx, created.Spec, "sched--moved"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("changed origin ref must fail closed: %v", err)
	}
	moved.EnvironmentRef = origin.EnvironmentRef
	if err := store.Save(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{Name: "independent", Prompt: "unsupported", Mode: session.ModePlan, Trigger: port.TriggerSpec{OneShot: time.Now().Add(time.Hour)}}); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("independent native schedule must fail before allocation: %v", err)
	}
	all, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(all.Items) != 1 {
		t.Fatalf("independent create allocated an environment: %d %v", len(all.Items), err)
	}
	plan, err := svc.PlanSessionCleanup(ctx, server.CleanupScope{Kinds: []session.SessionKind{session.SessionKindScheduled}})
	if err != nil || !plan.Available || len(plan.Eligible) != 1 {
		t.Fatalf("scheduled retention plan: %+v %v", plan, err)
	}
	job, err := svc.ApplySessionCleanup(ctx, plan.Token)
	if err != nil || job.Deleted != 1 {
		t.Fatalf("scheduled retention apply: %+v %v", job, err)
	}
	current, err = dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, origin.EnvironmentRef.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, _, err = unstructured.NestedSlice(current.Object, "status", "references")
	if err != nil {
		t.Fatal(err)
	}
	found = map[string]bool{}
	for _, row := range refs {
		ref, ok := row.(map[string]any)
		if ok && ref["state"] == "Published" {
			found[ref["bindingID"].(string)] = true
		}
	}
	if found["sched--first"] || !found["sched--second"] || !found[string(origin.ID)] {
		t.Fatalf("fire reference retention/cleanup mismatch: %v", found)
	}
	if err := svc.DeleteSession(ctx, origin.ID); err != nil {
		t.Fatalf("delete origin: %v", err)
	}
	if _, err := svc.ScheduleFirePlacement(ctx, created.Spec, "sched--orphan"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("deleted origin must fail closed: %v", err)
	}
}
