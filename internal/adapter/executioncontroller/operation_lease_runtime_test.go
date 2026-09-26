package executioncontroller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

type leaseBlockingExecutor struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newLeaseBlockingExecutor() *leaseBlockingExecutor {
	return &leaseBlockingExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
}

func (e *leaseBlockingExecutor) Execute(ctx context.Context, _ string, _ executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	close(e.started)
	select {
	case <-ctx.Done():
		close(e.cancelled)
	case <-e.release:
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Data: []byte("ok"), Version: "v1"}}, nil
	}
	<-e.release
	return executionenv.ExecutorResponse{}, ctx.Err()
}

func (e *leaseBlockingExecutor) unblock() { e.once.Do(func() { close(e.release) }) }

func TestActiveOperationCancelsWhenExecutionAuthorityIsLost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(*unstructured.Unstructured)
	}{
		{name: "run expiry", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "status", "activeRun", "expiresAt")
		}},
		{name: "operation expiry", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "status", "activeOperation", "expiresAt")
		}},
		{name: "grant revocation", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", "grantGeneration")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testActiveOperationCancellation(t, nil, tc.plant, nil)
		})
	}
}

func TestActiveOperationCancelsWhenLeaseRenewalPersistenceFails(t *testing.T) {
	var fail atomic.Bool
	testActiveOperationCancellation(t, func(client *dynamicfake.FakeDynamicClient) {
		client.PrependReactor("update", "executionenvironments", func(_ k8stesting.Action) (bool, runtime.Object, error) {
			if fail.CompareAndSwap(true, false) {
				return true, nil, apierrors.NewInternalError(errors.New("planted renewal write failure"))
			}
			return false, nil, nil
		})
	}, func(*unstructured.Unstructured) {}, func() { fail.Store(true) })
}

func testActiveOperationCancellation(t *testing.T, install func(*dynamicfake.FakeDynamicClient), plant func(*unstructured.Unstructured), afterPlant func()) {
	t.Helper()
	env := runFixtureEnvironment(1, 1)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	if install != nil {
		install(client)
	}
	exec := newLeaseBlockingExecutor()
	defer exec.unblock()
	store := NewStore(client, "ns", testProfiles(), exec)
	store.opTTL = 60 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := store.File(context.Background(), "client", "owner", executionenv.FileRequest{Context: operationRequestContext(), Operation: executionenv.OpFileRead, Path: "sentinel"})
		done <- err
	}()
	select {
	case <-exec.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	if err := store.retryUpdateStatus(t.Context(), "env", func(o *unstructured.Unstructured) error { plant(o); return nil }); err != nil {
		t.Fatal(err)
	}
	if afterPlant != nil {
		afterPlant()
	}
	select {
	case <-exec.cancelled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("active executor was not cancelled after execution authority was lost")
	}
	exec.unblock()
	var controlled *executionenv.Error
	select {
	case err := <-done:
		if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeFenceUnknown {
			t.Fatalf("operation error=%v, want FenceUnknown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operation did not finish after backend release")
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(got.Object, "status", "activeOperation", "id") == "" || textNested(got.Object, "status", "fenceState") != "FenceUnknown" {
		t.Fatalf("lost authority did not retain operation/fence uncertainty: %v", got.Object["status"])
	}
	_, err = store.File(t.Context(), "client", "owner", executionenv.FileRequest{Context: operationRequestContext(), Operation: executionenv.OpFileRead, Path: "takeover"})
	if err == nil {
		t.Fatal("new writer was admitted while prior operation remained uncertain")
	}
}

func TestActiveOperationCleanCompletionRechecksRunAuthority(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(*unstructured.Unstructured)
	}{
		{name: "run expiry", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "status", "activeRun", "expiresAt")
		}},
		{name: "operation expiry", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "status", "activeOperation", "expiresAt")
		}},
		{name: "missing operation expiry", plant: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "status", "activeOperation", "expiresAt")
		}},
		{name: "malformed operation expiry", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "not-a-time", "status", "activeOperation", "expiresAt")
		}},
		{name: "grant revocation", plant: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(2), "status", "grantGeneration")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := runFixtureEnvironment(1, 1)
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			exec := newLeaseBlockingExecutor()
			defer exec.unblock()
			store := NewStore(client, "ns", testProfiles(), exec)
			store.opTTL = time.Hour // No renewal tick can observe the planted loss.
			done := make(chan error, 1)
			go func() {
				_, err := store.File(context.Background(), "client", "owner", executionenv.FileRequest{Context: operationRequestContext(), Operation: executionenv.OpFileRead, Path: "sentinel"})
				done <- err
			}()
			select {
			case <-exec.started:
			case <-time.After(time.Second):
				t.Fatal("executor did not start")
			}
			if err := store.retryUpdateStatus(t.Context(), "env", func(o *unstructured.Unstructured) error { tc.plant(o); return nil }); err != nil {
				t.Fatal(err)
			}
			exec.unblock()
			select {
			case err := <-done:
				var controlled *executionenv.Error
				if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeFenceUnknown {
					t.Fatalf("operation error=%v, want FenceUnknown", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not finish")
			}
			got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if textNested(got.Object, "status", "activeOperation", "id") == "" || textNested(got.Object, "status", "fenceState") != "FenceUnknown" {
				t.Fatalf("lost authority was treated as a clean terminal: %v", got.Object["status"])
			}
			if _, err := store.File(t.Context(), "client", "owner", executionenv.FileRequest{Context: operationRequestContext(), Operation: executionenv.OpFileRead, Path: "takeover"}); err == nil {
				t.Fatal("takeover was admitted after completion authority was lost")
			}
		})
	}
}

func TestActiveOperationCleanCompletionClearsLeaseWithoutFencing(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runFixtureEnvironment(1, 1))
	exec := newLeaseBlockingExecutor()
	store := NewStore(client, "ns", testProfiles(), exec)
	store.opTTL = 60 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := store.File(context.Background(), "client", "owner", executionenv.FileRequest{Context: operationRequestContext(), Operation: executionenv.OpFileRead, Path: "sentinel"})
		done <- err
	}()
	<-exec.started
	exec.unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if id := textNested(got.Object, "status", "activeOperation", "id"); id != "" || textNested(got.Object, "status", "fenceState") != fenceHealthy {
		t.Fatalf("clean completion left operation uncertainty: %s %v", id, got.Object["status"])
	}
}

func operationRequestContext() executionenv.RequestContext {
	return executionenv.RequestContext{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 1, GrantGeneration: 1}
}
