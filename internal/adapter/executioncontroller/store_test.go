package executioncontroller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestReleaseReferenceChecksIdentityBeforeLifecycle(t *testing.T) {
	for _, transitioning := range []bool{false, true} {
		for _, identity := range []string{"absent", "foreign client", "foreign owner", "stale revision", "exact owner"} {
			name := "ready/" + identity
			if transitioning {
				name = "transitioning/" + identity
			}
			t.Run(name, func(t *testing.T) {
				env := runFixtureEnvironment(7, 3)
				unstructured.RemoveNestedField(env.Object, "status", "activeRun")
				if transitioning {
					if err := unstructured.SetNestedMap(env.Object, map[string]any{"id": "retire", "kind": "retire"}, "status", "lifecycleOperation"); err != nil {
						t.Fatal(err)
					}
				}
				dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
				store := NewStore(dynamic, "ns", testProfiles())
				ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
				client, owner := "client", "owner"
				switch identity {
				case "absent":
					ref.ID = "absent"
				case "foreign client":
					client = "other-client"
				case "foreign owner":
					owner = "other-owner"
				case "stale revision":
					ref.Revision = "stale"
				}
				err := store.ReleaseReference(t.Context(), ref, client, owner, "binding")
				after, getErr := dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if getErr != nil {
					t.Fatal(getErr)
				}
				if identity == "exact owner" && !transitioning {
					refs, refsErr := referenceRecords(after.(*unstructured.Unstructured))
					if err != nil || refsErr != nil || len(refs) != 0 {
						t.Fatalf("authorized release failed: %v, %v", err, refsErr)
					}
					return
				}
				want := &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
				if identity == "exact owner" {
					want = &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
				}
				var controlled *executionenv.Error
				if !errors.As(err, &controlled) || !reflect.DeepEqual(controlled, want) {
					t.Fatalf("release disclosed lifecycle or returned wrong owner error: got %v, want %v", err, want)
				}
				if !reflect.DeepEqual(env, after) {
					t.Fatal("rejected release mutated environment")
				}
				for _, action := range dynamic.Actions() {
					if action.GetVerb() != "get" {
						t.Fatalf("rejected release attempted mutation: %v", action)
					}
				}
			})
		}
	}
}

func TestRenewRunRejectsActiveRunOwnerClientTupleDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(*unstructured.Unstructured)
	}{
		{name: "wrong owner", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "wrong-owner", "status", "activeRun", "ownerHash")
		}},
		{name: "missing owner", plant: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "activeRun", "ownerHash")
		}},
		{name: "wrong client", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, hashText("wrong-client"), "status", "activeRun", "clientHash")
		}},
		{name: "missing client", plant: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "activeRun", "clientHash")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := runFixtureEnvironment(7, 3)
			tc.plant(env)
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			store := NewStore(client, "ns", testProfiles())
			ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
			req := executionenv.RunClaimRequest{BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: executionenv.MinRunTTL}
			if _, err := store.RenewRun(t.Context(), ref, "client", "owner", req); err == nil {
				t.Fatal("renewal accepted active-run identity drift")
			}
		})
	}
}

func TestRenewRunRejectsExpiredClaimAtBoundary(t *testing.T) {
	now := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	env := &unstructured.Unstructured{
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
				"epoch":           int64(7),
				"grantGeneration": int64(3),
				"fenceState":      fenceHealthy,
				"references":      []any{},
				"activeRun": map[string]any{
					"bindingID":       "binding",
					"runID":           "run",
					"claimID":         "claim",
					"operationID":     "acquire",
					"ownerHash":       "owner",
					"clientHash":      hashText("client"),
					"epoch":           int64(7),
					"grantGeneration": int64(3),
					"expiresAt":       now.Format(time.RFC3339Nano),
				},
			},
		},
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles())
	store.now = func() time.Time { return now }
	req := executionenv.RunClaimRequest{BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: executionenv.MinRunTTL}
	if _, err := store.RenewRun(t.Context(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", req); err == nil {
		t.Fatal("renewal at the expiry boundary resurrected the claim")
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if expiry := textNested(got.Object, "status", "activeRun", "expiresAt"); expiry != now.Format(time.RFC3339Nano) {
		t.Fatalf("expired claim changed to %q", expiry)
	}
}

func TestRenewRunReplaysExactOperationWithoutExtendingLease(t *testing.T) {
	now := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	env := &unstructured.Unstructured{
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
				"epoch":           int64(7),
				"grantGeneration": int64(3),
				"fenceState":      fenceHealthy,
				"references":      []any{},
				"activeRun": map[string]any{
					"bindingID":       "binding",
					"runID":           "run",
					"claimID":         "claim",
					"operationID":     "acquire",
					"ownerHash":       "owner",
					"clientHash":      hashText("client"),
					"epoch":           int64(7),
					"grantGeneration": int64(3),
					"expiresAt":       now.Add(time.Minute).Format(time.RFC3339Nano),
				},
			},
		},
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles())
	clock := now
	store.now = func() time.Time { return clock }
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	req := executionenv.RunClaimRequest{BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: executionenv.MinRunTTL}
	first, err := store.RenewRun(t.Context(), ref, "client", "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Second)
	replay, err := store.RenewRun(t.Context(), ref, "client", "owner", req)
	if err != nil || !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("replay extended or rejected lease: first=%v replay=%v err=%v", first.ExpiresAt, replay.ExpiresAt, err)
	}
	req.TTL = time.Minute
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", req); err == nil {
		t.Fatal("same operation accepted conflicting inputs")
	}
	clock = first.ExpiresAt
	req.TTL = executionenv.MinRunTTL
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", req); err == nil {
		t.Fatal("expired renewal receipt resurrected claim")
	}
}

func TestStoreEnsureUsesStableLookupAndRejectsFingerprintDrift(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	profiles := testProfiles()
	other := profiles.byName["go"]
	other.Spec.Image = "example@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherRevision := fixtureRevision(t, other.Spec)
	other.Digest = "sha256:" + otherRevision[3:]
	profiles.revisions["other"] = map[string]resolvedProfile{otherRevision: other}
	profiles.eligibility["other"] = map[string]TemplatePolicy{otherRevision: {}}
	profiles.defaultRevision["other"] = otherRevision
	s := NewStore(client, "ns", profiles)
	ctx := context.Background()
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	a, err := s.EnsurePendingOwnedRevision(
		ctx,
		"spiffe://client",
		ownerHash(owner),
		owner,
		"binding",
		"go",
		profiles.defaultRevision["go"],
		"fp1",
		"op",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsurePendingOwnedRevision(
		ctx,
		"spiffe://client",
		ownerHash(owner),
		owner,
		"binding",
		"go",
		profiles.defaultRevision["go"],
		"fp1",
		"op",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if a.Environment != b.Environment || a.Epoch != b.Epoch {
		t.Fatalf("identity changed: %+v %+v", a, b)
	}
	_, err = s.EnsurePendingOwnedRevision(
		ctx,
		"spiffe://client",
		ownerHash(owner),
		owner,
		"binding",
		"other",
		otherRevision,
		"fp2",
		"op",
		nil,
	)
	if err == nil {
		t.Fatal("profile drift adopted existing allocation")
	}
}
func TestAllocationNameExcludesMutableProfile(t *testing.T) {
	a := allocationName("client", "owner", "binding")
	b := allocationName("client", "owner", "binding")
	if a != b {
		t.Fatal("unstable allocation name")
	}
}

func runFixtureContext() executionenv.RequestContext {
	return executionenv.RequestContext{
		Environment:     executionenv.EnvironmentRef{ID: "env", Revision: "rev"},
		BindingID:       "binding",
		RunID:           "run",
		ClaimID:         "claim",
		Epoch:           1,
		GrantGeneration: 1,
	}
}

func requireOnlyReads(t *testing.T, client *dynamicfake.FakeDynamicClient) {
	t.Helper()
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("tool call wrote the environment: %s %s",
				action.GetVerb(), action.GetSubresource())
		}
	}
}

func TestExecutorRouteWritesNoEnvironmentStatus(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	s := NewStore(client, "ns", testProfiles())
	want := ExecutorPin{PodName: "pod", PodUID: "pod-uid", Fingerprint: fixtureFingerprint}
	for _, capability := range []string{CapabilityFilesystem, CapabilityForegroundCommand} {
		route, err := s.ExecutorRoute(
			t.Context(),
			"client",
			"owner",
			runFixtureContext(),
			capability,
		)
		if err != nil || route.Pin != want || route.MaxCommandDuration <= 0 {
			t.Fatalf("%s route=%+v err=%v", capability, route, err)
		}
	}
	requireOnlyReads(t, client)
}

func TestExecutorRouteRefusesUnadmittedCapability(t *testing.T) {
	env := runFixtureEnvironment(1, 1)
	filesOnly := []string{"filesystem"}
	_ = unstructured.SetNestedStringSlice(env.Object, filesOnly, "spec", admittedCapabilitiesField)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	s := NewStore(client, "ns", testProfiles())
	_, err := s.ExecutorRoute(
		t.Context(),
		"client",
		"owner",
		runFixtureContext(),
		CapabilityForegroundCommand,
	)
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodePermissionDenied {
		t.Fatalf("error=%v", err)
	}
}

func TestExecutorRouteDeniesAStaleClaimAsRetryable(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	s := NewStore(client, "ns", testProfiles())
	stale := runFixtureContext()
	stale.ClaimID = "superseded"
	_, err := s.ExecutorRoute(t.Context(), "client", "owner", stale, CapabilityFilesystem)
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeUnauthenticated || !pe.Retryable {
		t.Fatalf("error=%v", err)
	}
}
