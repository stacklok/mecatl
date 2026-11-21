package executioncontroller

import (
	"context"
	"errors"
	"reflect"
	"sync"
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
				store := NewStore(dynamic, "ns", testProfiles(), nil)
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
			store := NewStore(client, "ns", testProfiles(), nil)
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
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "templateID": "go", "templateRevision": testProfiles().defaultRevision["go"], "templateDigest": testProfiles().byName["go"].Digest, "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(7), "grantGeneration": int64(3), "fenceState": fenceHealthy, "references": []any{}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "ownerHash": "owner", "clientHash": hashText("client"), "epoch": int64(7), "grantGeneration": int64(3), "expiresAt": now.Format(time.RFC3339Nano)}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
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
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "templateID": "go", "templateRevision": testProfiles().defaultRevision["go"], "templateDigest": testProfiles().byName["go"].Digest, "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(7), "grantGeneration": int64(3), "fenceState": fenceHealthy, "references": []any{}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "ownerHash": "owner", "clientHash": hashText("client"), "epoch": int64(7), "grantGeneration": int64(3), "expiresAt": now.Add(time.Minute).Format(time.RFC3339Nano)}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
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
	s := NewStore(client, "ns", profiles, nil)
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

type recordingTransport struct {
	mu      sync.Mutex
	routes  []ExecutorRoute
	entered chan struct{}
	gate    chan struct{}
}

func (e *recordingTransport) Execute(
	ctx context.Context,
	route ExecutorRoute,
	req executionenv.ExecutorRequest,
) (executionenv.ExecutorResponse, error) {
	e.mu.Lock()
	e.routes = append(e.routes, route)
	e.mu.Unlock()
	if e.entered != nil {
		e.entered <- struct{}{}
	}
	if e.gate != nil {
		select {
		case <-e.gate:
		case <-ctx.Done():
			return executionenv.ExecutorResponse{}, ctx.Err()
		}
	}
	if req.Operation == executionenv.OpCommandStart {
		return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{
			CommandID: req.CommandID,
			State:     executionenv.CommandSucceeded,
		}}, nil
	}
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{
		Data:    []byte("ok"),
		Version: "v1",
	}}, nil
}

func (e *recordingTransport) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.routes)
}

type terminalErrorExecutor struct{ called int }

func (e *terminalErrorExecutor) Execute(
	context.Context,
	ExecutorRoute,
	executionenv.ExecutorRequest,
) (executionenv.ExecutorResponse, error) {
	e.called++
	return executionenv.ExecutorResponse{}, &executionenv.Error{
		Code:    executionenv.CodeNotFound,
		Message: "path not found",
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

func TestToolCallsWriteNoEnvironmentStatus(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	exec := &recordingTransport{}
	s := NewStore(client, "ns", testProfiles(), exec)
	file, err := s.File(t.Context(), "client", "owner", executionenv.FileRequest{
		Context:   runFixtureContext(),
		Operation: executionenv.OpFileRead,
		Path:      "main.go",
	})
	if err != nil || string(file.Data) != "ok" {
		t.Fatalf("file=%v err=%v", file, err)
	}
	command, err := s.StartCommand(t.Context(), "client", "owner", executionenv.CommandStartRequest{
		Context: runFixtureContext(),
		Command: "true",
	})
	if err != nil || command.State != executionenv.CommandSucceeded {
		t.Fatalf("command=%v err=%v", command, err)
	}
	requireOnlyReads(t, client)
	want := ExecutorPin{PodName: "pod", PodUID: "pod-uid", Fingerprint: fixtureFingerprint}
	if exec.calls() != 2 || exec.routes[0].Pin != want || exec.routes[1].MaxCommandDuration <= 0 {
		t.Fatalf("routes=%+v", exec.routes)
	}
}

func TestConcurrentReadsOnOneEnvironment(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	exec := &recordingTransport{entered: make(chan struct{}, 2), gate: make(chan struct{})}
	s := NewStore(client, "ns", testProfiles(), exec)
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.File(t.Context(), "client", "owner", executionenv.FileRequest{
				Context:   runFixtureContext(),
				Operation: executionenv.OpFileRead,
				Path:      "main.go",
			})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-exec.entered:
		case err := <-done:
			t.Fatalf("read refused while another was in flight: %v", err)
		}
	}
	close(exec.gate)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	requireOnlyReads(t, client)
}

func TestControlledExecutorErrorLeavesEnvironmentHealthy(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	exec := &terminalErrorExecutor{}
	s := NewStore(client, "ns", testProfiles(), exec)
	_, err := s.File(t.Context(), "client", "owner", executionenv.FileRequest{
		Context:   runFixtureContext(),
		Operation: executionenv.OpFileRead,
		Path:      "missing",
	})
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeNotFound || exec.called != 1 {
		t.Fatalf("error=%v calls=%d", err, exec.called)
	}
	requireOnlyReads(t, client)
}

func TestToolCallRefusesUnadmittedCapabilityBeforeDispatch(t *testing.T) {
	env := runFixtureEnvironment(1, 1)
	filesOnly := []string{"filesystem"}
	_ = unstructured.SetNestedStringSlice(env.Object, filesOnly, "spec", admittedCapabilitiesField)
	exec := &recordingTransport{}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	s := NewStore(client, "ns", testProfiles(), exec)
	_, err := s.StartCommand(t.Context(), "client", "owner", executionenv.CommandStartRequest{
		Context: runFixtureContext(),
		Command: "true",
	})
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodePermissionDenied || exec.calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, exec.calls())
	}
}
