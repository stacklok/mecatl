package executioncontroller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// ExecutionEnvironmentGVR identifies the private execution-environment CRD.
var ExecutionEnvironmentGVR = schema.GroupVersionResource{Group: "execution.mecatl.dev", Version: "v1alpha1", Resource: "executionenvironments"}

const (
	maxReferences              = 64
	environmentNotFoundMessage = "environment not found"
)

// ExecutorTransport dispatches one request to a fixed workload helper.
type ExecutorTransport interface {
	Execute(context.Context, string, executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error)
}

// Store persists authorization and fencing state in ExecutionEnvironment status.
type Store struct {
	resources dynamic.ResourceInterface
	profiles  *Profiles
	executor  ExecutorTransport
}

// NewStore constructs a namespace-scoped execution state store.
func NewStore(client dynamic.Interface, namespace string, profiles *Profiles, executor ExecutorTransport) *Store {
	return &Store{resources: client.Resource(ExecutionEnvironmentGVR).Namespace(namespace), profiles: profiles, executor: executor}
}

// ValidateProfile returns one validated immutable profile.
func (s *Store) ValidateProfile(_ context.Context, name string) (Profile, error) {
	p, ok := s.profiles.get(name)
	if !ok {
		return Profile{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "profile not found"}
	}
	return Profile{Name: name, Digest: p.Digest, MaxFileBytes: p.Spec.MaxFileBytes, MaxCommandBytes: p.Spec.MaxCommandBytes, MaxCommandDuration: p.Spec.MaxCommandDuration, Capabilities: []string{"filesystem", "foreground-command"}}, nil
}

// Ensure idempotently allocates an environment for an immutable request fingerprint.
func (s *Store) Ensure(ctx context.Context, client, owner, binding, profile, fp string) (Allocation, error) {
	p, ok := s.profiles.get(profile)
	if !ok {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "profile not found"}
	}
	name := allocationName(client, owner, binding)
	cur, err := s.resources.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if err := s.initializeStatus(ctx, cur, binding); err != nil {
			return Allocation{}, err
		}
		cur, err = s.resources.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Allocation{}, err
		}
		return allocationFrom(cur, client, owner, binding, profile, fp)
	}
	if !apierrors.IsNotFound(err) {
		return Allocation{}, fmt.Errorf("get execution environment: %w", err)
	}
	revision, err := randomID()
	if err != nil {
		return Allocation{}, err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": name, "finalizers": []any{environmentFinalizer}}, "spec": map[string]any{"allocationID": name, "revision": revision, "ownerHash": owner, "clientHash": hashText(client), "bindingID": binding, "requestFingerprint": fp, "profile": profile, "profileDigest": p.Digest, "image": p.Spec.Image, "storageClass": p.Spec.StorageClass, "storageSize": p.Spec.StorageSize, "resources": map[string]any{"cpuRequest": p.Spec.CPURequest, "memoryRequest": p.Spec.MemoryRequest, "cpuLimit": p.Spec.CPULimit, "memoryLimit": p.Spec.MemoryLimit}, "desired": "Active"}}}
	created, err := s.resources.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = s.resources.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return Allocation{}, fmt.Errorf("create execution environment: %w", err)
	}
	if err := s.initializeStatus(ctx, created, binding); err != nil {
		return Allocation{}, err
	}
	created, err = s.resources.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Allocation{}, err
	}
	return allocationFrom(created, client, owner, binding, profile, fp)
}
func (s *Store) initializeStatus(ctx context.Context, env *unstructured.Unstructured, binding string) error {
	if intNested(env.Object, "status", "epoch") > 0 {
		return nil
	}
	return s.retryUpdateStatus(ctx, env.GetName(), func(o *unstructured.Unstructured) error {
		if intNested(o.Object, "status", "epoch") > 0 {
			return nil
		}
		if err := unstructured.SetNestedField(o.Object, int64(1), "status", "epoch"); err != nil {
			return err
		}
		if err := unstructured.SetNestedStringSlice(o.Object, []string{binding}, "status", "references"); err != nil {
			return err
		}
		return unstructured.SetNestedField(o.Object, fenceHealthy, "status", "fenceState")
	})
}
func allocationName(client, owner, binding string) string {
	sum := sha256.Sum256([]byte(client + "\x00" + owner + "\x00" + binding))
	return "exec-" + hex.EncodeToString(sum[:20])
}
func allocationFrom(o *unstructured.Unstructured, client, owner, binding, profile, fp string) (Allocation, error) {
	spec, _, _ := unstructured.NestedMap(o.Object, "spec")
	if text(spec, "ownerHash") != owner || text(spec, "clientHash") != hashText(client) || text(spec, "bindingID") != binding || text(spec, "profile") != profile || text(spec, "requestFingerprint") != fp {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "allocation identity conflicts with existing environment"}
	}
	return exactAllocation(o, client, owner, binding)
}
func exactAllocation(o *unstructured.Unstructured, client, owner, binding string) (Allocation, error) {
	spec, _, _ := unstructured.NestedMap(o.Object, "spec")
	if text(spec, "ownerHash") != owner || text(spec, "clientHash") != hashText(client) {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	revision := text(spec, "revision")
	if revision == "" {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment identity is incomplete"}
	}
	epoch, ok := epochValue(o)
	if !ok {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment epoch is invalid"}
	}
	ready := conditionTrue(o, "Ready") && textNested(o.Object, "status", "fenceState") == fenceHealthy
	return Allocation{Environment: executionenv.EnvironmentRef{ID: o.GetName(), Revision: revision}, Epoch: epoch, OwnerHash: owner, BindingID: binding, Client: client, Ready: ready}, nil
}

// Attach adds a binding reference only to the exact healthy environment.
func (s *Store) Attach(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) (Allocation, error) {
	var out Allocation
	err := s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		a, err := exactAllocation(o, client, owner, binding)
		if err != nil {
			return err
		}
		if a.Environment != ref || textNested(o.Object, "spec", "desired") != "Active" {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment revision unavailable"}
		}
		if !a.Ready {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "environment is not ready at requested epoch"}
		}
		refs, _, _ := unstructured.NestedStringSlice(o.Object, "status", "references")
		if !contains(refs, binding) {
			if len(refs) >= maxReferences {
				return &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "environment reference limit reached"}
			}
			refs = append(refs, binding)
			sort.Strings(refs)
			if err := unstructured.SetNestedStringSlice(o.Object, refs, "status", "references"); err != nil {
				return err
			}
		}
		out = a
		return nil
	})
	return out, err
}

// ReleaseReference removes one binding reference without deleting the environment.
func (s *Store) ReleaseReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		a, err := exactAllocation(o, client, owner, binding)
		if err != nil {
			return err
		}
		if a.Environment != ref {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, _, _ := unstructured.NestedStringSlice(o.Object, "status", "references")
		next := refs[:0]
		for _, v := range refs {
			if v != binding {
				next = append(next, v)
			}
		}
		return unstructured.SetNestedStringSlice(o.Object, next, "status", "references")
	})
}

// Retire requests controlled retirement of an unreferenced healthy environment.
func (s *Store) Retire(ctx context.Context, ref executionenv.EnvironmentRef, owner string) error {
	cur, err := s.resources.Get(ctx, ref.ID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	if err != nil {
		return err
	}
	if textNested(cur.Object, "spec", "ownerHash") != owner || textNested(cur.Object, "spec", "revision") != ref.Revision {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	refs, _, _ := unstructured.NestedStringSlice(cur.Object, "status", "references")
	if len(refs) != 0 || textNested(cur.Object, "status", "activeOperation", "id") != "" || textNested(cur.Object, "status", "fenceState") != fenceHealthy {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is referenced or not quiescent"}
	}
	if err := unstructured.SetNestedField(cur.Object, "Retiring", "spec", "desired"); err != nil {
		return err
	}
	_, err = s.resources.Update(ctx, cur, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment changed concurrently", Retryable: true}
	}
	return err
}

// File performs one claimed and fenced filesystem operation.
func (s *Store) File(ctx context.Context, client, owner string, q executionenv.FileRequest) (executionenv.FileResponse, error) {
	req := executionenv.ExecutorRequest{Operation: q.Operation, Path: q.Path, Destination: q.Destination, Pattern: q.Pattern, Data: q.Data, Version: q.Version, Limit: q.Limit}
	resp, err := s.execute(ctx, client, owner, q.Context, req)
	return resp.FileResponse, err
}

// StartCommand performs one claimed foreground command through terminal receipt.
func (s *Store) StartCommand(ctx context.Context, client, owner string, q executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	id, err := randomID()
	if err != nil {
		return executionenv.CommandStartResponse{}, err
	}
	resp, err := s.execute(ctx, client, owner, q.Context, executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, Command: q.Command, CommandID: id, TimeoutMillis: q.TimeoutMillis})
	if err != nil {
		return executionenv.CommandStartResponse{}, err
	}
	if resp.Command == nil {
		return executionenv.CommandStartResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "executor returned no terminal command receipt"}
	}
	return executionenv.CommandStartResponse{CommandID: id, State: resp.Command.State, Result: *resp.Command}, nil
}

// CommandStatus reports that foreground-only commands have no detached status API.
func (*Store) CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "foreground commands have no resumable status stream"}
}

// CancelCommand refuses cancellation without an attached foreground control stream.
func (*Store) CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "no active foreground stream is attached; external fencing is required"}
}
func (s *Store) execute(ctx context.Context, client, owner string, rc executionenv.RequestContext, req executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) { //nolint:gocyclo // Atomic claim, dispatch, and fencing remain one auditable transaction.
	if s.executor == nil {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeNotReady, Message: "executor transport unavailable", Retryable: true}
	}
	opID, err := randomID()
	if err != nil {
		return executionenv.ExecutorResponse{}, err
	}
	var pod string
	var maxFileBytes, maxCommandBytes int64
	err = s.retryUpdateStatus(ctx, rc.Environment.ID, func(o *unstructured.Unstructured) error {
		profile, ok := s.profiles.get(textNested(o.Object, "spec", "profile"))
		if !ok || profile.Digest != textNested(o.Object, "spec", "profileDigest") {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "environment profile is unavailable"}
		}
		maxFileBytes, maxCommandBytes = profile.Spec.MaxFileBytes, profile.Spec.MaxCommandBytes
		if int64(len(req.Data)) > maxFileBytes || int64(len(req.Command)) > maxCommandBytes {
			return &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "operation exceeds profile bounds"}
		}
		if req.Operation == executionenv.OpCommandStart {
			maxMillis := profile.Spec.MaxCommandDuration.Milliseconds()
			if req.TimeoutMillis <= 0 {
				req.TimeoutMillis = maxMillis
			} else if req.TimeoutMillis > maxMillis {
				return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "command timeout exceeds profile bound"}
			}
		}
		refs, _, _ := unstructured.NestedStringSlice(o.Object, "status", "references")
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || !contains(refs, rc.BindingID) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		if textNested(o.Object, "spec", "revision") != rc.Environment.Revision || !epochMatches(o, rc.Epoch) || textNested(o.Object, "spec", "desired") != "Active" || !conditionTrue(o, "Ready") || textNested(o.Object, "status", "fenceState") != fenceHealthy {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "environment is not ready at requested epoch"}
		}
		if textNested(o.Object, "status", "activeOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment already has an active operation", Retryable: true}
		}
		pod = textNested(o.Object, "status", "pod", "name")
		if pod == "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "executor pod is unavailable", Retryable: true}
		}
		return unstructured.SetNestedMap(o.Object, map[string]any{"id": opID, "operation": string(req.Operation), "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}, "status", "activeOperation")
	})
	if err != nil {
		return executionenv.ExecutorResponse{}, err
	}
	current, err := s.resources.Get(ctx, rc.Environment.ID, metav1.GetOptions{})
	if err != nil || textNested(current.Object, "spec", "ownerHash") != owner || textNested(current.Object, "spec", "clientHash") != hashText(client) || textNested(current.Object, "spec", "revision") != rc.Environment.Revision || !epochMatches(current, rc.Epoch) || textNested(current.Object, "status", "activeOperation", "id") != opID {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation identity changed before executor dispatch"}
	}
	resp, dispatchErr := s.executor.Execute(ctx, pod, req)
	if dispatchErr == nil && (int64(len(resp.Data)) > maxFileBytes || resp.Command != nil && int64(len(resp.Command.Stdout)+len(resp.Command.Stderr)) > maxCommandBytes) {
		dispatchErr = &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "executor response exceeds profile bound"}
	}
	var controlledErr *executionenv.Error
	controlledTerminal := errors.As(dispatchErr, &controlledErr) && controlledErr.Code != executionenv.CodeFenceUnknown
	terminal := controlledTerminal || dispatchErr == nil && (req.Operation != executionenv.OpCommandStart || resp.Command != nil && resp.Command.TerminalReceipt != "")
	finishErr := s.retryUpdateStatus(context.WithoutCancel(ctx), rc.Environment.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "activeOperation", "id") != opID {
			return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation ownership changed before completion"}
		}
		if terminal {
			unstructured.RemoveNestedField(o.Object, "status", "activeOperation")
			return nil
		}
		unstructured.RemoveNestedField(o.Object, "status", "activeOperation")
		return unstructured.SetNestedField(o.Object, "FenceUnknown", "status", "fenceState")
	})
	if finishErr != nil {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "could not persist terminal operation state"}
	}
	if controlledTerminal {
		return executionenv.ExecutorResponse{}, controlledErr
	}
	if !terminal {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "executor termination is unconfirmed; environment fenced", Retryable: false}
	}
	return resp, nil
}
func (s *Store) retryUpdateStatus(ctx context.Context, name string, mutate func(*unstructured.Unstructured) error) error {
	for i := 0; i < 5; i++ {
		cur, err := s.resources.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		if err != nil {
			return err
		}
		if err := mutate(cur); err != nil {
			return err
		}
		if _, err = s.resources.UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err == nil {
			return nil
		} else if !apierrors.IsConflict(err) {
			return err
		}
	}
	return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment changed concurrently", Retryable: true}
}
func epochValue(o *unstructured.Unstructured) (uint64, bool) {
	epoch, found, err := unstructured.NestedInt64(o.Object, "status", "epoch")
	if err != nil || !found || epoch <= 0 {
		return 0, false
	}
	return uint64(epoch), true
}

func epochMatches(o *unstructured.Unstructured, expected uint64) bool {
	epoch, ok := epochValue(o)
	return ok && epoch == expected
}

func conditionTrue(o *unstructured.Unstructured, name string) bool {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if ok && text(m, "type") == name && text(m, "status") == "True" {
			return true
		}
	}
	return false
}
func text(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func textNested(m map[string]any, p ...string) string {
	v, _, _ := unstructured.NestedString(m, p...)
	return v
}
func intNested(m map[string]any, p ...string) int64 {
	v, _, _ := unstructured.NestedInt64(m, p...)
	return v
}
func hashText(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

var _ Backend = (*Store)(nil)
