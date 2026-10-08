package microvm

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func (d *Daemon) handleRepositoryRequest(ctx context.Context, request LifecycleRequest) (LifecycleResponse, bool) {
	if d.repositoryAttachments == nil {
		return LifecycleResponse{}, false
	}
	var response LifecycleResponse
	var err error
	switch {
	case request.Operation == LifecycleCreate && request.Provision != nil:
		response, err = d.repositoryCreate(ctx, request)
	case strings.HasPrefix(request.Binding.EnvironmentID, "logical-"):
		response, err = d.repositoryOperation(ctx, request)
	case request.Operation == LifecycleExec:
		response, err = LifecycleResponse{}, ErrEnvironmentUnavailable
	default:
		return LifecycleResponse{}, false
	}
	if err != nil {
		return lifecycleFailure(err), true
	}
	return response, true
}

func (d *Daemon) repositoryCreate(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	if d.repositoryProvisioner == nil || request.Provision == nil || request.Binding.Owner == "" || request.Binding.SessionID == "" || request.Provision.Owner != request.Binding.Owner || request.Provision.SessionID != request.Binding.SessionID {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	placement, err := d.repositoryProvisioner(ctx, *request.Provision)
	if err != nil {
		return LifecycleResponse{}, err
	}
	attachment, err := d.repositoryAttachments.Attach(ctx, placement.Request)
	if err != nil {
		return LifecycleResponse{}, err
	}
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil || generation != attachment.Logical.Repository.Generation {
		_ = attachment.Close()
		return LifecycleResponse{}, ErrRepositoryVMInconsistent
	}
	binding := control.Binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID, EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
	if err := d.repositoryAttachments.register(binding, attachment); err != nil {
		_ = attachment.Close()
		return LifecycleResponse{}, err
	}
	d.repositoryMu.Lock()
	d.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding, status: placement.Status}
	d.repositoryMu.Unlock()
	created := &LifecycleCreated{
		Ref: attachment.Logical.Ref, Generation: generation, HostWorktree: attachment.Logical.WorktreePath,
		GuestRoot: worktree.GuestWorkspace, Profile: placement.Status.Profile, GuestEgress: placement.Status.GuestEgress, HostEgress: placement.Status.HostEgress,
	}
	return LifecycleResponse{Binding: binding, Created: created}, nil
}

func (d *Daemon) repositoryOperation(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) { //nolint:gocyclo // exact attachment routing remains auditable in one switch
	if err := claimValidate(request.Binding); err != nil {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	if request.Operation == LifecycleResolve {
		if err := d.repositoryAttachments.validateBinding(request.Binding, false); err != nil {
			return LifecycleResponse{}, err
		}
	}
	if request.Operation == LifecycleDelete || request.Operation == LifecycleChildDelete {
		result, err := d.repositoryAttachments.delete(ctx, request.Binding)
		if d.observer != nil {
			outcome := OutcomeSuccess
			if err != nil {
				outcome = OutcomeFailure
			}
			d.observer.CleanupFinished(outcome)
		}
		if err != nil {
			return LifecycleResponse{}, err
		}
		d.removeRepositoryBinding(request.Binding.Ref)
		payload, err := json.Marshal(result)
		return LifecycleResponse{Binding: request.Binding, Payload: payload}, err
	}
	attachment := d.repositoryAttachment(request.Binding)
	if request.Operation == LifecycleResolve && attachment == nil {
		if request.Provision == nil || request.Provision.SourceCheckout == "" || request.Provision.Owner != request.Binding.Owner || request.Provision.SessionID != request.Binding.SessionID {
			return LifecycleResponse{}, control.ErrBindingMismatch
		}
		reattached, err := d.repositoryAttachments.Reattach(ctx, LogicalEnvironmentRequest{Owner: request.Binding.Owner, Checkout: request.Provision.SourceCheckout}, session.EnvironmentRef{Kind: session.EnvironmentKind(Kind), ID: request.Binding.Ref})
		if err != nil {
			return LifecycleResponse{}, err
		}
		if err := d.repositoryAttachments.register(request.Binding, reattached); err != nil {
			_ = d.repositoryAttachments.Detach(reattached.Environment.Ref())
			return LifecycleResponse{}, err
		}
		d.repositoryMu.Lock()
		d.repositoryBindings[request.Binding.Ref] = repositoryDaemonBinding{binding: request.Binding}
		d.repositoryMu.Unlock()
		attachment = reattached
	}
	if attachment == nil {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}

	switch request.Operation {
	case LifecycleResolve, LifecycleInspect:
		return LifecycleResponse{Binding: request.Binding}, nil
	case LifecycleWorkspace:
		payload, err := proxyWorkspace(ctx, attachment.Environment.Workspace(), request.Payload)
		if err != nil {
			return LifecycleResponse{}, err
		}
		encoded, err := json.Marshal(payload)
		return LifecycleResponse{Binding: request.Binding, Payload: encoded}, err
	case LifecycleExec:
		return d.repositoryExec(ctx, request, attachment)
	case LifecycleDetach:
		if err := d.repositoryAttachments.detach(ctx, attachment.Environment.Ref()); err != nil {
			return LifecycleResponse{}, err
		}
		d.removeRepositoryBinding(request.Binding.Ref)
		return LifecycleResponse{Binding: request.Binding}, nil
	case LifecycleFork:
		return d.repositoryFork(ctx, request, attachment)
	case LifecycleMerge:
		return d.repositoryMerge(ctx, request, attachment)
	default:
		return LifecycleResponse{}, errLifecycleProtocol
	}
}

func (d *Daemon) repositoryFork(ctx context.Context, request LifecycleRequest, parent *RepositoryAttachment) (LifecycleResponse, error) {
	var payload ChildForkPayload
	if json.Unmarshal(request.Payload, &payload) != nil || payload.Label == "" || len(payload.Label) > 256 {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	child, _, _, err := d.repositoryAttachments.Fork(ctx, parent.Environment, payload.Label)
	if err != nil {
		return LifecycleResponse{}, err
	}
	childAttachment := d.repositoryAttachments.lookup(child.Ref())
	environmentID, generation, err := parseEnvironmentRef(childAttachment.Logical.Ref)
	if err != nil {
		_ = childAttachment.Close()
		return LifecycleResponse{}, err
	}
	binding := control.Binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID + ":" + payload.Label, EnvironmentID: environmentID, Ref: child.Ref().ID, Generation: generation}
	if err := d.repositoryAttachments.register(binding, childAttachment); err != nil {
		_ = childAttachment.Close()
		return LifecycleResponse{}, err
	}
	d.repositoryMu.Lock()
	d.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	d.repositoryMu.Unlock()
	return LifecycleResponse{Binding: binding}, nil
}

func (d *Daemon) repositoryMerge(ctx context.Context, request LifecycleRequest, parent *RepositoryAttachment) (LifecycleResponse, error) {
	var payload ChildMergePayload
	if json.Unmarshal(request.Payload, &payload) != nil {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	child := d.repositoryAttachment(payload.Child)
	if child == nil {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	if err := d.repositoryAttachments.Merge(ctx, child.Environment, parent.Environment); err != nil {
		return LifecycleResponse{}, err
	}
	return LifecycleResponse{Binding: request.Binding}, nil
}

func (d *Daemon) repositoryExec(ctx context.Context, request LifecycleRequest, attachment *RepositoryAttachment) (_ LifecycleResponse, retErr error) {
	defer func() { d.observeExec(retErr) }()
	var input struct {
		Command        string              `json:"command"`
		TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
	}
	if json.Unmarshal(request.Payload, &input) != nil || input.Command == "" {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	runner := attachment.Environment.CommandRunner()
	var result tool.CommandResult
	var err error
	if input.TemporaryScope == "" {
		result, err = runner.Run(ctx, input.Command)
	} else if scoped, ok := runner.(tool.CommandTemporaryScopeRunner); ok {
		result, err = scoped.RunWithTemporaryScope(ctx, input.Command, input.TemporaryScope)
	} else {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	if err != nil {
		return LifecycleResponse{}, err
	}
	payload, err := json.Marshal(struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}{result.Stdout, result.Stderr, result.ExitCode})
	return LifecycleResponse{Binding: request.Binding, Payload: payload}, err
}

func (d *Daemon) repositoryExecStream(ctx context.Context, request LifecycleRequest, send func(LifecycleExecStream) error) (_ LifecycleResponse, retErr error) {
	defer func() { d.observeExec(retErr) }()
	attachment := d.repositoryAttachment(request.Binding)
	if attachment == nil {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	var input struct {
		Command        string              `json:"command"`
		TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
	}
	if json.Unmarshal(request.Payload, &input) != nil || input.Command == "" {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	runner := attachment.Logical.Runner
	if runner == nil {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	exit, err := runner.RunFramesWithTemporaryScope(ctx, input.Command, input.TemporaryScope, func(frame guestexec.OutputFrame) error {
		return send(LifecycleExecStream{Channel: frame.Channel, Data: frame.Data})
	})
	if err != nil {
		return LifecycleResponse{}, err
	}
	payload, err := json.Marshal(struct {
		ExitCode int `json:"exit_code"`
	}{exit})
	return LifecycleResponse{Binding: request.Binding, Payload: payload}, err
}

func (d *Daemon) observeExec(err error) {
	if d == nil || d.observer == nil {
		return
	}
	outcome := OutcomeSuccess
	if err != nil {
		outcome = OutcomeFailure
	}
	d.observer.ExecFinished(outcome, "")
}

func (d *Daemon) repositoryAttachment(claim control.Binding) *RepositoryAttachment {
	if d == nil || d.repositoryAttachments == nil || claim.Ref == "" || !strings.HasPrefix(claim.EnvironmentID, "logical-") {
		return nil
	}
	d.repositoryMu.Lock()
	known, ok := d.repositoryBindings[claim.Ref]
	d.repositoryMu.Unlock()
	if !ok || known.binding != claim {
		return nil
	}
	return d.repositoryAttachments.lookup(session.EnvironmentRef{Kind: session.EnvironmentKind(Kind), ID: claim.Ref})
}

func (d *Daemon) removeRepositoryBinding(ref string) {
	d.repositoryMu.Lock()
	delete(d.repositoryBindings, ref)
	d.repositoryMu.Unlock()
}
