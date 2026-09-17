package executioncontroller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func lifecycleEnvironment(now time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment",
		"metadata": map[string]any{"name": "env", "namespace": "ns"},
		"spec":     map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "profile": "go", "profileDigest": "sha256:profile", "desired": "Active"},
		"status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "references": []any{
			map[string]any{"bindingID": "source", "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)},
		}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
}

func TestEnsurePendingOwnedPersistsOwnerAttestation(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	store := NewStore(client, "ns", testProfiles(), nil)
	owner := executionenv.Owner{Issuer: "https://issuer.example", Subject: "alice"}
	allocation, err := store.EnsurePendingOwned(t.Context(), "client", ownerHash(owner), owner, "binding", "go", "fingerprint", "ensure-operation")
	if err != nil {
		t.Fatal(err)
	}
	env, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), allocation.Environment.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(env.Object, "spec", "ownerIssuer") != owner.Issuer || textNested(env.Object, "spec", "ownerSubject") != owner.Subject {
		t.Fatalf("owner attestation was not persisted: %v", env.Object["spec"])
	}
}

func TestRunClaimIsEnvironmentWideAndExact(t *testing.T) {
	now := time.Now().UTC()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), lifecycleEnvironment(now))
	store := NewStore(client, "ns", testProfiles(), nil)
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	first, err := store.AcquireRun(t.Context(), ref, "client", "owner", "source", "run-a", "op-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch != 2 || first.ClaimID == "" || first.GrantGeneration != 1 {
		t.Fatalf("claim=%+v", first)
	}
	repeat, err := store.AcquireRun(t.Context(), ref, "client", "owner", "source", "run-a", "op-a", time.Minute)
	if err != nil || repeat.ClaimID != first.ClaimID || repeat.Epoch != first.Epoch {
		t.Fatalf("repeat=%+v err=%v", repeat, err)
	}
	if _, err := store.AcquireRun(t.Context(), ref, "client", "owner", "source", "run-b", "op-b", time.Minute); err == nil {
		t.Fatal("overlapping claim admitted")
	}
	stale := executionenv.RunClaimRequest{Environment: ref, BindingID: "source", RunID: "run-a", ClaimID: "wrong", Epoch: first.Epoch, GrantGeneration: first.GrantGeneration, OperationID: "release", TTL: time.Minute}
	if err := store.ReleaseRun(t.Context(), ref, "client", "owner", stale); err == nil {
		t.Fatal("stale release admitted")
	}
	stale.ClaimID = first.ClaimID
	if err := store.ReleaseRun(t.Context(), ref, "client", "owner", stale); err != nil {
		t.Fatal(err)
	}
	second, err := store.AcquireRun(t.Context(), ref, "client", "owner", "source", "run-b", "op-b", time.Minute)
	if err != nil || second.Epoch != first.Epoch+1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestExpiredClaimWithActiveOperationCannotBeReplaced(t *testing.T) {
	now := time.Now().UTC()
	env := lifecycleEnvironment(now)
	statusMap, _, _ := unstructured.NestedMap(env.Object, "status")
	statusMap["epoch"] = int64(4)
	statusMap["activeRun"] = map[string]any{"bindingID": "source", "runID": "old", "claimID": "old-claim", "operationID": "old-acquire", "epoch": int64(4), "grantGeneration": int64(1), "expiresAt": now.Add(-time.Minute).Format(time.RFC3339Nano)}
	statusMap["activeOperation"] = map[string]any{"id": "still-running", "claimID": "old-claim", "runID": "old", "epoch": int64(4)}
	env.Object["status"] = statusMap
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	_, err := store.AcquireRun(t.Context(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", "source", "new", "new-op", time.Minute)
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeFenceUnknown {
		t.Fatalf("error=%v", err)
	}
}

func TestExactReferenceIntentLookupIsNotTruncatedByGlobalLimit(t *testing.T) {
	now := time.Now().UTC()
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	objects := make([]runtime.Object, 0, 67)
	for i := 0; i < 66; i++ {
		env := lifecycleEnvironment(now)
		env.SetName(fmt.Sprintf("env-%02d", i))
		_ = unstructured.SetNestedField(env.Object, fmt.Sprintf("rev-%02d", i), "spec", "revision")
		_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "spec", "ownerHash")
		_ = unstructured.SetNestedField(env.Object, owner.Issuer, "spec", "ownerIssuer")
		_ = unstructured.SetNestedField(env.Object, owner.Subject, "spec", "ownerSubject")
		if err := setReferenceRecords(env, []referenceRecord{{BindingID: fmt.Sprintf("binding-%02d", i), State: executionenv.ReferencePendingDelete, OperationID: fmt.Sprintf("delete-%02d", i), CreatedAt: now}}); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, env)
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	store := NewStore(client, "ns", testProfiles(), nil)
	target := executionenv.EnvironmentRef{ID: "env-65", Revision: "rev-65"}
	intent, err := store.FindReferenceIntent(t.Context(), target, "client", ownerHash(owner), "binding-65")
	if err != nil || intent.OperationID != "delete-65" {
		t.Fatalf("exact target beyond global list limit: intent=%+v err=%v", intent, err)
	}
	if _, err := store.FindReferenceIntent(t.Context(), target, "other-client", ownerHash(owner), "binding-65"); err == nil {
		t.Fatal("other client enumerated exact intent")
	}
	if _, err := store.FindReferenceIntent(t.Context(), target, "client", ownerHash(executionenv.Owner{Issuer: "issuer", Subject: "mallory"}), "binding-65"); err == nil {
		t.Fatal("other owner enumerated exact intent")
	}
}

func TestClientScopedIntentReconciliationRejectsEachOwnerMutationWithoutSideEffects(t *testing.T) {
	now := time.Now().UTC()
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	mutations := map[string]func(*unstructured.Unstructured){
		"owner hash": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, hashText("other-owner"), "spec", "ownerHash")
		},
		"owner issuer": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "other-issuer", "spec", "ownerIssuer")
		},
		"owner subject": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "mallory", "spec", "ownerSubject")
		},
		"client binding": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, hashText("other-client"), "spec", "clientHash")
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			env := lifecycleEnvironment(now)
			_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "spec", "ownerHash")
			_ = unstructured.SetNestedField(env.Object, owner.Issuer, "spec", "ownerIssuer")
			_ = unstructured.SetNestedField(env.Object, owner.Subject, "spec", "ownerSubject")
			refs, err := referenceRecords(env)
			if err != nil {
				t.Fatal(err)
			}
			refs = append(refs, referenceRecord{BindingID: "pending", State: executionenv.ReferencePendingDelete, OperationID: "delete", CreatedAt: now})
			if err := setReferenceRecords(env, refs); err != nil {
				t.Fatal(err)
			}
			mutate(env)
			before := env.DeepCopy()
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			store := NewStore(client, "ns", testProfiles(), nil)
			intents, listErr := store.ListReferenceIntentsForClient(t.Context(), "client", 64)
			if listErr == nil && len(intents) != 0 {
				t.Fatalf("mutated identity exposed intents: %+v", intents)
			}
			after, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Object, after.Object) {
				t.Fatal("negative reconciliation mutated durable state")
			}
		})
	}
}

func TestClientScopedIntentListReturnsAttestedOwnerOnlyToOwningClient(t *testing.T) {
	now := time.Now().UTC()
	env := lifecycleEnvironment(now)
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	if err := unstructured.SetNestedField(env.Object, ownerHash(owner), "spec", "ownerHash"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(env.Object, owner.Issuer, "spec", "ownerIssuer"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(env.Object, owner.Subject, "spec", "ownerSubject"); err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	if err := store.ReserveSuccessor(t.Context(), ref, "client", ownerHash(owner), "source", "destination", "fork-op"); err != nil {
		t.Fatal(err)
	}
	intents, err := store.ListReferenceIntentsForClient(t.Context(), "client", 64)
	if err != nil || len(intents) != 1 || intents[0].Owner != owner || intents[0].BindingID != "destination" {
		t.Fatalf("intents=%+v err=%v", intents, err)
	}
	other, err := store.ListReferenceIntentsForClient(t.Context(), "other-client", 64)
	if err != nil || len(other) != 0 {
		t.Fatalf("other-client intents=%+v err=%v", other, err)
	}
}

func TestReferenceTransactionsRetainUnknownAndNeverChangeSource(t *testing.T) {
	now := time.Now().UTC()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), lifecycleEnvironment(now))
	store := NewStore(client, "ns", testProfiles(), nil)
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	if err := store.ReserveSuccessor(t.Context(), ref, "client", "owner", "source", "destination", "fork-op"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveSuccessor(t.Context(), ref, "client", "owner", "source", "destination", "fork-op"); err != nil {
		t.Fatal(err)
	}
	intents, err := store.ListReferenceIntents(t.Context(), "client", "owner", 64)
	if err != nil || len(intents) != 1 || intents[0].BindingID != "destination" || intents[0].State != executionenv.ReferencePendingCreate {
		t.Fatalf("intents=%+v err=%v", intents, err)
	}
	if err := store.CommitReference(t.Context(), ref, "client", "owner", "destination", "fork-op"); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareReferenceDelete(t.Context(), ref, "client", "owner", "destination", "delete-op"); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelReferenceDelete(t.Context(), ref, "client", "owner", "destination", "delete-op"); err != nil {
		t.Fatal(err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(context.Background(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := referenceRecords(got)
	if err != nil {
		t.Fatal(err)
	}
	if !publishedReference(refs, "source") || !publishedReference(refs, "destination") {
		t.Fatalf("references=%+v", refs)
	}
}
