package executioncontroller

import (
	"errors"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestExpiredExactRunDoesNotPermanentlyBlockFinalReferenceRetirement(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	env := lifecycleAdminEnvironment(2, []any{map[string]any{"bindingID": "binding", "state": string(executionenv.ReferencePublished), operationIDField: "seed", "createdAt": now.Add(-time.Hour).Format(time.RFC3339Nano)}})
	setLifecycleRunClaim(env, now.Add(-time.Second), 4, 1)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	store.now = func() time.Time { return now }
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	if err := store.ReleaseReference(t.Context(), ref, "client", "owner", "binding"); err != nil {
		t.Fatalf("remove final reference after client loss: %v", err)
	}
	q := adminRequestFixture()
	if err := store.RetireExact(t.Context(), q); err != nil {
		t.Fatalf("expired exact run permanently blocked retirement: %v", err)
	}
	started, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(started.Object, "status", "lifecycleOperation", "type") != "RetireEnvironment" {
		t.Fatalf("retirement did not start: %v", started.Object["status"])
	}

	// Model the controller's already-covered retirement completion so this test
	// also proves the retained-delete admission immediately following this path.
	if err := store.retryUpdateStatus(t.Context(), "env", func(o *unstructured.Unstructured) error {
		unstructured.RemoveNestedField(o.Object, "status", "activeRun")
		unstructured.RemoveNestedField(o.Object, "status", "lifecycleOperation")
		setConditionObject(o, "Retired", true, "WorkspaceRetained", "retained")
		setConditionObject(o, "ExecutorTerminated", true, "Verified", "terminated")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q.OperationID = "delete-retained"
	if err := store.DeleteRetiredEnvironment(t.Context(), q); err != nil {
		t.Fatalf("retained delete after expired-run retirement: %v", err)
	}
}

func TestExpiredExactRunAdmitsReadyFalseLifecycleWithTerminalProof(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		existingProof bool
		administrator bool
	}{
		{name: "existing exact proof from prior operation", existingProof: true},
		{name: "newly verified terminal pod and pvc"},
		{name: "scoped administrator uses creator run tuple", existingProof: true, administrator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			setConditionObject(env, "Ready", false, "PodTerminated", "executor exited")
			setLifecycleRunClaim(env, now.Add(-time.Second), 4, 1)
			if tc.existingProof {
				setLifecycleTerminationProof(env, "prior-operation")
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			store := NewStore(client, "ns", testProfiles(), nil)
			if !tc.existingProof {
				store = store.WithKubeClient(kubernetesfake.NewSimpleClientset(terminalExecutor(), retainedPVC()))
			}
			store.now = func() time.Time { return now }
			q := adminRequestFixture()
			if tc.administrator {
				q.Client = "administrator"
				q.AdministratorFor = []string{"client"}
			}
			if err := store.RetireExact(t.Context(), q); err != nil {
				t.Fatalf("retire with exact terminal proof: %v", err)
			}
			got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if textNested(got.Object, "status", "activeRun", "claimID") != "" || textNested(got.Object, "status", "lifecycleOperation", "id") != q.OperationID || !exactTerminationProofMatches(got, q) {
				t.Fatalf("expired claim and proof were not atomically advanced: %v", got.Object["status"])
			}
		})
	}
}

func TestLifecycleRefusesLiveMalformedMismatchedClaimsAndActiveOperationWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		plant func(*unstructured.Unstructured)
	}{
		{name: "live claim", plant: func(o *unstructured.Unstructured) { setLifecycleRunClaim(o, now.Add(time.Minute), 4, 1) }},
		{name: "malformed claim", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(o.Object, map[string]any{"claimID": "claim", "expiresAt": "not-a-time"}, "status", "activeRun")
		}},
		{name: "mismatched epoch", plant: func(o *unstructured.Unstructured) { setLifecycleRunClaim(o, now.Add(-time.Second), 3, 1) }},
		{name: "mismatched generation", plant: func(o *unstructured.Unstructured) { setLifecycleRunClaim(o, now.Add(-time.Second), 4, 2) }},
		{name: "wrong active-run owner", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "wrong-owner", "status", "activeRun", "ownerHash")
		}},
		{name: "missing active-run owner", plant: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "activeRun", "ownerHash")
		}},
		{name: "wrong active-run client", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, hashText("wrong-client"), "status", "activeRun", "clientHash")
		}},
		{name: "missing active-run client", plant: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "activeRun", "clientHash")
		}},
		{name: "active operation", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(o.Object, map[string]any{"id": "writer", "operation": string(executionenv.OpFileReplace), "claimID": "claim", "runID": "run", "epoch": int64(4), "holderID": "holder"}, "status", "activeOperation")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			setConditionObject(env, "Ready", false, "PodTerminated", "executor exited")
			setLifecycleRunClaim(env, now.Add(-time.Second), 4, 1)
			setLifecycleTerminationProof(env, "prior-operation")
			tc.plant(env)
			before := env.DeepCopy()
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			store := NewStore(client, "ns", testProfiles(), nil)
			store.now = func() time.Time { return now }
			err := store.RetireExact(t.Context(), adminRequestFixture())
			var controlled *executionenv.Error
			if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
				t.Fatalf("retirement error=%v, want conflict", err)
			}
			after, getErr := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if getErr != nil {
				t.Fatal(getErr)
			}
			before.SetResourceVersion(after.GetResourceVersion())
			if !reflect.DeepEqual(before.Object, after.Object) {
				t.Fatalf("refused lifecycle changed state\nbefore=%v\nafter=%v", before.Object["status"], after.Object["status"])
			}
		})
	}
}

func setLifecycleRunClaim(o *unstructured.Unstructured, expiry time.Time, epoch, generation int64) {
	_ = unstructured.SetNestedMap(o.Object, map[string]any{
		"bindingID": "binding", "runID": "run", "claimID": "claim", operationIDField: "acquire",
		"ownerHash": "owner", "clientHash": hashText("client"),
		"epoch": epoch, "grantGeneration": generation, "expiresAt": expiry.UTC().Format(time.RFC3339Nano),
	}, "status", "activeRun")
}

func setLifecycleTerminationProof(o *unstructured.Unstructured, operationID string) {
	_ = unstructured.SetNestedMap(o.Object, map[string]any{
		operationIDField: operationID, "podUID": "pod-uid", "pvcUID": "pvc-uid", "epoch": int64(4),
		"podPhase": "Failed", "observedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}, "status", "terminationProof")
}
