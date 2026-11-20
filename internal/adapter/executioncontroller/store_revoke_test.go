package executioncontroller

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestRevokeEnvironmentFencesOldClaimWithoutChangingExecutionEpoch(t *testing.T) {
	env := runFixtureEnvironment(7, 3)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles())
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	next, err := store.RevokeEnvironment(t.Context(), adminLifecycleRequest{Environment: ref, Client: "client", OwnerHash: "owner", OperationID: "revoke-1"}, 3)
	if err != nil || next != 4 {
		t.Fatalf("revoke=(%d,%v)", next, err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if intNested(got.Object, "status", "epoch") != 7 || intNested(got.Object, "status", "grantGeneration") != 4 {
		t.Fatalf("status=%v", got.Object["status"])
	}
	otherReplica := NewStore(client, "ns", testProfiles())
	oldGrant := executionenv.RequestContext{
		Environment:     ref,
		BindingID:       "binding",
		RunID:           "run",
		ClaimID:         "claim",
		Epoch:           7,
		GrantGeneration: 3,
	}
	_, err = otherReplica.ExecutorRoute(
		t.Context(),
		"client",
		"owner",
		oldGrant,
		CapabilityFilesystem,
	)
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeUnauthenticated {
		t.Fatalf("old grant error=%v", err)
	}
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", executionenv.RunClaimRequest{Environment: ref, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: time.Minute}); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("old renewal error=%v", err)
	}
	replayed, err := store.RevokeEnvironment(t.Context(), adminLifecycleRequest{Environment: ref, Client: "client", OwnerHash: "owner", OperationID: "revoke-1"}, 3)
	if err != nil || replayed != 4 {
		t.Fatalf("replayed revoke=(%d,%v)", replayed, err)
	}
	if _, err := store.RevokeEnvironment(t.Context(), adminLifecycleRequest{Environment: ref, Client: "client", OwnerHash: "owner", OperationID: "revoke-2"}, 3); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
}

func TestRevokeEnvironmentOverflowFailsClosed(t *testing.T) {
	env := runFixtureEnvironment(1, math.MaxInt64)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles())
	if _, err := store.RevokeEnvironment(context.Background(), adminLifecycleRequest{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, Client: "client", OwnerHash: "owner", OperationID: "overflow"}, math.MaxInt64); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestProfileEnvironmentAdmissionLimit(t *testing.T) {
	env := lifecycleEnvironment(time.Now().UTC())
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"}, env)
	profiles := testProfiles()
	profile := profiles.byName["go"]
	profiles.capacityLimit["go"] = 1
	profiles.revisions["go"][profiles.defaultRevision["go"]] = profile
	store := NewStore(client, "ns", profiles).WithKubeClient(
		kubernetesfake.NewSimpleClientset(
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      profileAllocationConfigMap("go"),
					Namespace: "ns",
				},
				Data: map[string]string{profileAllocationKey("go"): "[]"},
			},
		),
	)
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	_, err := store.EnsurePendingOwnedRevision(
		t.Context(),
		"client",
		ownerHash(owner),
		owner,
		"new-binding",
		"go",
		profiles.defaultRevision["go"],
		"new-fingerprint",
		"new-operation",
		nil,
	)
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeResourceExhausted {
		t.Fatalf("limit error=%v", err)
	}
}

func runFixtureEnvironment(epoch, generation uint64) *unstructured.Unstructured {
	env := unpinnedRunFixture(epoch, generation)
	_ = unstructured.SetNestedField(env.Object, "pod-uid", "status", "pod", "uid")
	writeServerCertificatePin(env, serverCertificatePin{
		SecretName:  "executor-env-tls",
		Fingerprint: fixtureFingerprint,
		PodUID:      "pod-uid",
	})
	return env
}

var fixtureFingerprint = certificateFingerprint([]byte("executor"))

func unpinnedRunFixture(epoch, generation uint64) *unstructured.Unstructured {
	now := time.Now().UTC()
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "execution.mecatl.dev/v1alpha1",
			"kind":       "ExecutionEnvironment",
			"metadata":   map[string]any{"name": "env", "namespace": "ns"},
			"spec": map[string]any{
				"schemaVersion":    int64(3),
				"revision":         "rev",
				"ownerHash":        "owner",
				"clientHash":       hashText("client"),
				"templateID":       "go",
				"templateRevision": testProfiles().defaultRevision["go"],
				"templateDigest":   testProfiles().byName["go"].Digest,
				"desired":          "Active",
			},
			"status": map[string]any{
				"schemaVersion":   int64(3),
				"epoch":           int64(epoch),
				"grantGeneration": int64(generation),
				"fenceState":      "Healthy",
				"references": []any{
					map[string]any{
						"bindingID":   "binding",
						"state":       "Published",
						"operationID": "seed",
						"createdAt":   now.Format(time.RFC3339Nano),
					},
				},
				"activeRun": map[string]any{
					"bindingID":       "binding",
					"runID":           "run",
					"claimID":         "claim",
					"operationID":     "acquire",
					"ownerHash":       "owner",
					"clientHash":      hashText("client"),
					"epoch":           int64(epoch),
					"grantGeneration": int64(generation),
					"expiresAt":       now.Add(time.Minute).Format(time.RFC3339Nano),
				},
				"pod":        map[string]any{"name": "pod"},
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			},
		},
	}
}
