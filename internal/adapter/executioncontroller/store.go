//nolint:revive // Private store methods implement adapter-only lifecycle interfaces.
package executioncontroller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// ExecutionEnvironmentGVR identifies the private execution-environment CRD.
var ExecutionEnvironmentGVR = schema.GroupVersionResource{Group: "execution.mecatl.dev", Version: "v1alpha1", Resource: "executionenvironments"}

const (
	maxReferences              = 64
	environmentNotFoundMessage = "environment not found"
	currentSchemaVersion       = int64(2)
	operationLeaseTTL          = 30 * time.Second
	transitioningMessage       = "environment is transitioning"
	operationIDField           = "operationID"
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
	kube      kubernetes.Interface
	namespace string
	holderID  string
	now       func() time.Time
	opTTL     time.Duration
}

// NewStore constructs a namespace-scoped execution state store.
func NewStore(client dynamic.Interface, namespace string, profiles *Profiles, executor ExecutorTransport) *Store {
	holder, err := randomID()
	if err != nil {
		holder = "unavailable"
	}
	return &Store{resources: client.Resource(ExecutionEnvironmentGVR).Namespace(namespace), profiles: profiles, executor: executor, namespace: namespace, holderID: holder, now: func() time.Time { return time.Now().UTC() }, opTTL: 30 * time.Second}
}

// WithKubeClient enables provider-owned executor and retained-volume lifecycle operations.
func (s *Store) WithKubeClient(kube kubernetes.Interface) *Store {
	s.kube = kube
	return s
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
	return s.EnsurePending(ctx, client, owner, binding, profile, fp, "legacy-"+fp)
}

func (s *Store) EnsurePending(ctx context.Context, client, owner, binding, profile, fp, operationID string) (Allocation, error) {
	return s.ensurePending(ctx, client, owner, executionenv.Owner{}, binding, profile, fp, operationID)
}

// EnsurePendingOwned persists the attested principal needed for owner-safe intent reconciliation.
func (s *Store) EnsurePendingOwned(ctx context.Context, client, expectedOwnerHash string, owner executionenv.Owner, binding, profile, fp, operationID string) (Allocation, error) {
	if owner.Issuer == "" || owner.Subject == "" || ownerHash(owner) != expectedOwnerHash {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "owner attestation is invalid"}
	}
	return s.ensurePending(ctx, client, expectedOwnerHash, owner, binding, profile, fp, operationID)
}

func (s *Store) ensurePending(ctx context.Context, client, owner string, attested executionenv.Owner, binding, profile, fp, operationID string) (Allocation, error) {
	p, ok := s.profiles.get(profile)
	if !ok {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "profile not found"}
	}
	name := allocationName(client, owner, binding)
	cur, err := s.resources.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if err := s.initializeStatus(ctx, cur, binding, operationID); err != nil {
			return Allocation{}, err
		}
		cur, err = s.resources.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Allocation{}, err
		}
		if !referenceOperationMatches(cur, binding, operationID) {
			return Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "reference operation conflicts with existing environment"}
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
	if s.kube != nil {
		if err := s.reserveProfileSlot(ctx, profile, name, p.Spec.MaxEnvironments); err != nil {
			return Allocation{}, err
		}
	}
	spec := map[string]any{"schemaVersion": currentSchemaVersion, "allocationID": name, "revision": revision, "ownerHash": owner, "clientHash": hashText(client), "bindingID": binding, "requestFingerprint": fp, "profile": profile, "profileDigest": p.Digest, "image": p.Spec.Image, "storageClass": p.Spec.StorageClass, "storageSize": p.Spec.StorageSize, "resources": map[string]any{"cpuRequest": p.Spec.CPURequest, "memoryRequest": p.Spec.MemoryRequest, "cpuLimit": p.Spec.CPULimit, "memoryLimit": p.Spec.MemoryLimit}, "desired": "Active"}
	if attested.Issuer != "" && attested.Subject != "" {
		spec["ownerIssuer"] = attested.Issuer
		spec["ownerSubject"] = attested.Subject
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": name, "finalizers": []any{environmentFinalizer}}, "spec": spec}}
	created, err := s.resources.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = s.resources.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		if s.kube != nil && definitiveCreateRejection(err) {
			if releaseErr := releaseProfileSlot(ctx, s.kube, s.namespace, profile, name); releaseErr != nil {
				return Allocation{}, errors.Join(fmt.Errorf("create execution environment: %w", err), fmt.Errorf("release profile allocation: %w", releaseErr))
			}
		}
		return Allocation{}, fmt.Errorf("create execution environment: %w", err)
	}
	if err := s.initializeStatus(ctx, created, binding, operationID); err != nil {
		return Allocation{}, err
	}
	created, err = s.resources.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Allocation{}, err
	}
	return allocationFrom(created, client, owner, binding, profile, fp)
}

func definitiveCreateRejection(err error) bool {
	return apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsBadRequest(err) || apierrors.IsMethodNotSupported(err) || apierrors.IsRequestEntityTooLargeError(err)
}

const profileAllocationConfigMap = "mecatl-execution-profile-allocations"

func profileAllocationKey(profile string) string {
	keySum := sha256.Sum256([]byte(profile))
	return "profile-" + hex.EncodeToString(keySum[:16]) + ".json"
}

func (s *Store) reserveProfileSlot(ctx context.Context, profile, allocationID string, limit int) error { //nolint:gocyclo // Durable capacity CAS and CR reconciliation are intentionally one transaction loop.
	cms := s.kube.CoreV1().ConfigMaps(s.namespace)
	key := profileAllocationKey(profile)
	for range 8 {
		cm, err := cms.Get(ctx, profileAllocationConfigMap, metav1.GetOptions{})
		exists := true
		if apierrors.IsNotFound(err) {
			exists = false
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: s.namespace}, Data: map[string]string{}}
		} else if err != nil {
			return fmt.Errorf("read profile allocation authority: %w", err)
		}
		var slots []string
		if raw := cm.Data[key]; raw != "" {
			if err := executionenv.DecodeStrict([]byte(raw), &slots); err != nil || len(slots) > limit {
				return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "profile allocation authority is invalid"}
			}
		}
		if slices.Contains(slots, allocationID) {
			return nil
		}
		all, err := s.resources.List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list profile allocations: %w", err)
		}
		for i := range all.Items {
			item := &all.Items[i]
			deallocating := conditionTrue(item, "Retired") && conditionTrue(item, "ExecutorTerminated") && textNested(item.Object, "status", "lifecycleOperation", "type") == "DeleteRetiredEnvironment" && textNested(item.Object, "status", "lifecycleOperation", "phase") == "ReleasingSlot"
			if textNested(item.Object, "spec", "profile") == profile && !deallocating && !slices.Contains(slots, item.GetName()) {
				slots = append(slots, item.GetName())
			}
		}
		if len(slots) >= limit {
			return &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "profile environment limit reached"}
		}
		slots = append(slots, allocationID)
		slices.Sort(slots)
		raw, err := json.Marshal(slots)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = string(raw)
		if exists {
			_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		} else {
			_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		}
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("reserve profile allocation: %w", err)
		}
	}
	return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "profile allocation authority changed concurrently", Retryable: true}
}

func releaseProfileSlot(ctx context.Context, kube kubernetes.Interface, namespace, profile, allocationID string) error {
	cms := kube.CoreV1().ConfigMaps(namespace)
	key := profileAllocationKey(profile)
	for range 8 {
		cm, err := cms.Get(ctx, profileAllocationConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var slots []string
		if err := executionenv.DecodeStrict([]byte(cm.Data[key]), &slots); err != nil {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "profile allocation authority is invalid"}
		}
		index := slices.Index(slots, allocationID)
		if index < 0 {
			return nil
		}
		slots = append(slots[:index], slots[index+1:]...)
		raw, err := json.Marshal(slots)
		if err != nil {
			return err
		}
		cm.Data[key] = string(raw)
		if _, err = cms.Update(ctx, cm, metav1.UpdateOptions{}); err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return errors.New("profile allocation authority changed concurrently")
}

func (s *Store) initializeStatus(ctx context.Context, env *unstructured.Unstructured, binding, operationID string) error {
	if operationID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "operation identity is required"}
	}
	if intNested(env.Object, "status", "epoch") > 0 {
		return requireCurrentSchema(env)
	}
	return s.retryUpdateStatusRaw(ctx, env.GetName(), func(o *unstructured.Unstructured) error {
		if intNested(o.Object, "status", "epoch") > 0 {
			return requireCurrentSchema(o)
		}
		if intNested(o.Object, "spec", "schemaVersion") != currentSchemaVersion {
			return incompatibleSchemaError()
		}
		if err := unstructured.SetNestedField(o.Object, currentSchemaVersion, "status", "schemaVersion"); err != nil {
			return err
		}
		if err := unstructured.SetNestedField(o.Object, int64(1), "status", "epoch"); err != nil {
			return err
		}
		if err := unstructured.SetNestedField(o.Object, int64(1), "status", "grantGeneration"); err != nil {
			return err
		}
		ref := map[string]any{"bindingID": binding, "state": string(executionenv.ReferencePendingCreate), operationIDField: operationID, "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}
		if err := unstructured.SetNestedSlice(o.Object, []any{ref}, "status", "references"); err != nil {
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
	generation := intNested(o.Object, "status", "grantGeneration")
	if generation <= 0 {
		return Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "grant generation is invalid"}
	}
	ready := conditionTrue(o, "Ready") && textNested(o.Object, "status", "fenceState") == fenceHealthy
	return Allocation{Environment: executionenv.EnvironmentRef{ID: o.GetName(), Revision: revision}, Epoch: epoch, GrantGeneration: uint64(generation), OwnerHash: owner, BindingID: binding, Client: client, Ready: ready}, nil
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
		refs, refsErr := referenceRecords(o)
		if refsErr != nil {
			return refsErr
		}
		if !attachedReference(refs, binding) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		out = a
		return nil
	})
	return out, err
}

// ReleaseReference removes one binding reference without deleting the environment.
func (s *Store) ReleaseReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		a, err := exactAllocation(o, client, owner, binding)
		if err != nil {
			return err
		}
		if a.Environment != ref {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, refsErr := referenceRecords(o)
		if refsErr != nil {
			return refsErr
		}
		for i := range refs {
			if refs[i].BindingID == binding {
				if refs[i].State != executionenv.ReferencePublished {
					return &executionenv.Error{Code: executionenv.CodeConflict, Message: "reference has a pending transaction"}
				}
				return setReferenceRecords(o, append(refs[:i], refs[i+1:]...))
			}
		}
		return nil
	})
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
	if rc.Epoch == 0 || rc.Epoch > math.MaxInt64 || rc.GrantGeneration == 0 || rc.GrantGeneration > math.MaxInt64 {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "invalid execution epoch or grant generation"}
	}
	epochStatus := int64(rc.Epoch) //nolint:gosec // bounded above by MaxInt64.
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
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
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
		refs, refsErr := referenceRecords(o)
		if refsErr != nil {
			return refsErr
		}
		claim, _, expiry, claimOK := activeRunFrom(o)
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || !publishedReference(refs, rc.BindingID) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		if !claimOK || !s.now().Before(expiry) || claim.BindingID != rc.BindingID || claim.RunID != rc.RunID || claim.ClaimID != rc.ClaimID || claim.Epoch != rc.Epoch || claim.GrantGeneration != rc.GrantGeneration || !generationMatches(o, rc.GrantGeneration) {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "run claim is not current"}
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
		now := s.now()
		return unstructured.SetNestedMap(o.Object, map[string]any{"id": opID, "operation": string(req.Operation), "startedAt": now.Format(time.RFC3339Nano), "claimID": rc.ClaimID, "runID": rc.RunID, "epoch": epochStatus, "holderID": s.holderID, "renewedAt": now.Format(time.RFC3339Nano), "expiresAt": now.Add(s.opTTL).Format(time.RFC3339Nano)}, "status", "activeOperation")
	})
	if err != nil {
		return executionenv.ExecutorResponse{}, err
	}
	authority := operationLeaseAuthority{
		environment: rc.Environment.ID, revision: rc.Environment.Revision, operationID: opID,
		bindingID: rc.BindingID, runID: rc.RunID, claimID: rc.ClaimID,
		client: client, owner: owner, epoch: rc.Epoch, grantGeneration: rc.GrantGeneration,
	}
	current, err := s.resources.Get(ctx, rc.Environment.ID, metav1.GetOptions{})
	if err != nil || !operationLeaseCurrent(current, authority, s.holderID, s.now()) || !s.operationAuthorityCurrent(current, authority) {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation identity changed before executor dispatch"}
	}
	opCtx, stopExecution := context.WithCancel(ctx)
	leaseDone := make(chan error, 1)
	go func() {
		leaseErr := s.renewOperationLease(opCtx, authority)
		if leaseErr != nil {
			stopExecution()
		}
		leaseDone <- leaseErr
	}()
	resp, dispatchErr := s.executor.Execute(opCtx, pod, req)
	stopExecution()
	leaseErr := <-leaseDone
	authorityLost := leaseErr != nil
	if authorityLost {
		fenceErr := &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation lease renewal failed"}
		if dispatchErr != nil {
			dispatchErr = errors.Join(fenceErr, dispatchErr)
		} else {
			dispatchErr = fenceErr
		}
	}
	if dispatchErr == nil && (int64(len(resp.Data)) > maxFileBytes || resp.Command != nil && int64(len(resp.Command.Stdout)+len(resp.Command.Stderr)) > maxCommandBytes) {
		dispatchErr = &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "executor response exceeds profile bound"}
	}
	var controlledErr *executionenv.Error
	controlledTerminal := !authorityLost && errors.As(dispatchErr, &controlledErr) && controlledErr.Code != executionenv.CodeFenceUnknown
	terminal := !authorityLost && (controlledTerminal || dispatchErr == nil && (req.Operation != executionenv.OpCommandStart || resp.Command != nil && resp.Command.TerminalReceipt != ""))
	completionAuthorityLost := false
	finishErr := s.retryUpdateStatus(context.WithoutCancel(ctx), rc.Environment.ID, func(o *unstructured.Unstructured) error {
		if !operationMatches(o, authority, s.holderID) {
			return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation ownership changed before completion"}
		}
		if terminal && operationLeaseCurrent(o, authority, s.holderID, s.now()) && s.operationAuthorityCurrent(o, authority) {
			unstructured.RemoveNestedField(o.Object, "status", "activeOperation")
			return nil
		}
		if terminal {
			completionAuthorityLost = true
		}
		return unstructured.SetNestedField(o.Object, "FenceUnknown", "status", "fenceState")
	})
	if finishErr != nil {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "could not persist terminal operation state"}
	}
	if completionAuthorityLost {
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "operation authority changed before completion"}
	}
	if controlledTerminal {
		return executionenv.ExecutorResponse{}, controlledErr
	}
	if !terminal {
		if authorityLost {
			return executionenv.ExecutorResponse{}, dispatchErr
		}
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "executor termination is unconfirmed; environment fenced", Retryable: false}
	}
	return resp, nil
}
func (s *Store) retryUpdateStatus(ctx context.Context, name string, mutate func(*unstructured.Unstructured) error) error {
	return s.retryUpdateStatusRaw(ctx, name, func(o *unstructured.Unstructured) error {
		if err := requireCurrentSchema(o); err != nil {
			return err
		}
		return mutate(o)
	})
}

func (s *Store) retryUpdateStatusRaw(ctx context.Context, name string, mutate func(*unstructured.Unstructured) error) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
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
		_, err = s.resources.UpdateStatus(ctx, cur, metav1.UpdateOptions{})
		return err
	})
	if apierrors.IsConflict(err) {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment changed concurrently", Retryable: true}
	}
	return err
}
func requireCurrentSchema(o *unstructured.Unstructured) error {
	if intNested(o.Object, "spec", "schemaVersion") != currentSchemaVersion || intNested(o.Object, "status", "schemaVersion") != currentSchemaVersion {
		return incompatibleSchemaError()
	}
	return nil
}

func incompatibleSchemaError() error {
	return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "environment schema is incompatible; explicit migration is required"}
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
