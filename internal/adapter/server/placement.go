package server

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var (
	// ErrInvalidPlacementSelection reports a malformed selector or binding request.
	ErrInvalidPlacementSelection = errors.New("server: invalid placement selection")
	// ErrPlacementNotFound deliberately covers both absent and authorization-hidden IDs.
	ErrPlacementNotFound = errors.New("server: placement not found")
	// ErrPlacementUnavailable reports a known, authorized placement whose backend is unavailable.
	ErrPlacementUnavailable = errors.New("server: placement unavailable")
	// ErrPlacementChanged reports an inventory revision race during Bind.
	ErrPlacementChanged = errors.New("server: placement changed during bind")
	// ErrInvalidPlacementBinding reports unsafe or internally inconsistent provider output.
	ErrInvalidPlacementBinding = errors.New("server: invalid placement binding")
)

// PlacementScope is a trusted composition-owned authorization scope. It is not
// derived from a placement ID or filesystem path.
type PlacementScope string

// PlacementOperation identifies the server operation requesting a new binding.
type PlacementOperation string

const (
	// PlacementOperationCreate binds a placement for a new root session.
	PlacementOperationCreate PlacementOperation = "create"
	// PlacementOperationSuccessor binds or reauthorizes a placement while
	// creating a successor session.
	PlacementOperationSuccessor PlacementOperation = "successor"
)

// PlacementBindRequest contains the complete authorization context a provider
// needs to atomically authorize and resolve one placement record version.
type PlacementBindRequest struct {
	Selector  session.PlacementSelector
	Principal *session.Principal
	Scope     PlacementScope
	Operation PlacementOperation
}

// PlacementMetadata is the bounded, display-safe provider projection returned
// with a binding. It contains no roots, locators, credentials, or authority.
type PlacementMetadata struct {
	Name        string
	Description string
}

// PlacementBinding is the indivisible successful result of Bind.
type PlacementBinding struct {
	Environment tool.Environment
	Ref         session.EnvironmentRef
	Metadata    PlacementMetadata
}

// PlacementProvider owns placement inventory, authorization, and atomic new
// binding resolution.
type PlacementProvider interface {
	Bind(context.Context, PlacementBindRequest) (PlacementBinding, error)
}

// PlacementReattacher is the exact persisted-ref half of a placement provider.
// It is separate from PlacementProvider so legacy providers cannot accidentally
// receive a reattachment request through Bind and follow their current default.
type PlacementReattacher interface {
	Reattach(context.Context, PlacementReattachRequest) (PlacementBinding, error)
}

// PlacementReattachRequest carries the trusted authorization context and the
// exact durable identity to reattach. Ref must include Kind, ID, and Revision.
type PlacementReattachRequest struct {
	Ref       session.EnvironmentRef
	Principal *session.Principal
	Scope     PlacementScope
}

// PlacementBinder is the server-owned choke point around one deployment
// provider. It exposes no registry, inventory, signer, cache, or split
// authorization/resolution operation.
type PlacementBinder struct {
	provider PlacementProvider
}

// NewPlacementBinder constructs the binding choke point.
func NewPlacementBinder(provider PlacementProvider) (*PlacementBinder, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: PlacementProvider is required", ErrConfig)
	}
	return &PlacementBinder{provider: provider}, nil
}

func configuredPlacementBinder(ctx context.Context, cfg Config) (*PlacementBinder, error) {
	if err := validateWorkspaceAuthorityConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.PlacementProvider == nil {
		return nil, nil
	}
	if cfg.PlacementScope == "" {
		return nil, fmt.Errorf("%w: PlacementScope is required with PlacementProvider", ErrConfig)
	}
	binder, err := NewPlacementBinder(cfg.PlacementProvider)
	if err != nil {
		return nil, err
	}
	// NewService runs before a listener can serve. Binding the configured
	// default proves that its current record is authorized, available,
	// revision-stable, and capable of constructing a complete environment. The
	// result is deliberately not cached.
	if _, err := binder.Bind(ctx, PlacementBindRequest{
		Selector: session.DefaultPlacement(), Scope: cfg.PlacementScope,
		Operation: PlacementOperationCreate,
	}); err != nil {
		return nil, fmt.Errorf("server: validate default placement: %w", err)
	}
	return binder, nil
}

func configuredWorktreeSelectors(cfg Config) (*WorktreeSelectorIssuer, error) {
	if cfg.PlacementSelectorKey == ([worktreeSelectorKeySize]byte{}) {
		return nil, nil
	}
	issuer, err := NewWorktreeSelectorIssuer(cfg.PlacementSelectorKey[:])
	if err != nil {
		return nil, fmt.Errorf("server: initialize worktree selector issuer: %w", err)
	}
	return issuer, nil
}

func (s *Service) bindPlacementForCreate(ctx context.Context, workspace string, profile SessionProfile, owner *session.Principal) (string, *PlacementBinding, error) {
	useBinder := s.placementBinder != nil
	if !useBinder {
		return workspace, nil, nil
	}
	selector := session.DefaultPlacement()
	if profile == ProfileNoFS {
		selector = session.NoFSPlacement()
	}
	binding, err := s.placementBinder.Bind(ctx, PlacementBindRequest{
		Selector: selector, Principal: owner, Scope: s.cfg.PlacementScope,
		Operation: PlacementOperationCreate,
	})
	if err != nil {
		return "", nil, err
	}
	boundRoot := binding.Environment.Workspace().Root()
	if profile == ProfileNoFS && boundRoot != "" || profile != ProfileNoFS && boundRoot == "" {
		return "", nil, ErrInvalidPlacementBinding
	}
	return boundRoot, &binding, nil
}

func (s *Service) persistPlacedCreatedSession(ctx context.Context, sess *session.Session, owner *session.Principal, request *createRequest, placement *PlacementBinding) (*session.Session, error) {
	if placement != nil {
		sess.EnvironmentRef = placement.Ref
	} else {
		stampDefaultEnvironmentRef(sess)
	}
	persisted, err := s.persistCreatedSession(ctx, sess, owner, request)
	if err == nil && persisted == sess && placement != nil {
		s.mu.Lock()
		s.sessionEnvironments[sess.ID] = placement.Environment
		s.mu.Unlock()
	}
	return persisted, err
}

func (s *Service) resolveSchedulePlacement(ctx context.Context, ref session.EnvironmentRef, profile SessionProfile) (session.EnvironmentRef, string, SessionProfile, error) {
	if s.placementBinder == nil {
		if profile == ProfileNoFS {
			return session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, "legacy-local", profile, nil
		}
		if !ref.Valid() && s.cfg.DefaultWorkspace != "" {
			ref = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: s.cfg.DefaultWorkspace, Revision: "in-tree-v1"}
		}
		if !ref.Valid() {
			return session.EnvironmentRef{}, "", profile, fmt.Errorf("%w: exact schedule placement is required", ErrFailedPrecondition)
		}
		return ref, "legacy-local", profile, nil
	}
	if profile == ProfileNoFS {
		binding, err := s.BindPlacement(ctx, session.NoFSPlacement(), PlacementOperationCreate)
		if err != nil {
			return session.EnvironmentRef{}, "", profile, err
		}
		return binding.Ref, string(s.cfg.PlacementScope), ProfileNoFS, nil
	}
	if profile != ProfileDefault {
		return session.EnvironmentRef{}, "", profile, fmt.Errorf("%w: unknown schedule profile %q", ErrInvalidArgument, profile)
	}
	var binding PlacementBinding
	var err error
	if ref.Valid() {
		binding, err = s.ReattachPlacement(ctx, ref)
	} else {
		binding, err = s.BindPlacement(ctx, session.DefaultPlacement(), PlacementOperationCreate)
	}
	if err != nil {
		return session.EnvironmentRef{}, "", profile, err
	}
	return binding.Ref, string(s.cfg.PlacementScope), ProfileDefault, nil
}

func (s *Service) reattachLegacySchedulePlacement(ref session.EnvironmentRef) (PlacementBinding, error) {
	if !ref.Valid() {
		return PlacementBinding{}, ErrInvalidPlacementSelection
	}
	if ref.Kind == session.EnvKindNoFS {
		env := tool.MustEnvironment(ref, nofs.New(), nil)
		return PlacementBinding{Environment: env, Ref: ref}, nil
	}
	if ref.Kind != session.EnvKindLocal || s.cfg.Workspaces == nil {
		return PlacementBinding{}, ErrPlacementUnavailable
	}
	ws := s.cfg.Workspaces(ref.ID)
	var runner tool.CommandRunner
	if ref.ID == s.cfg.DefaultWorkspace {
		runner = s.cfg.CommandRunner
	} else if s.cfg.CommandRunnerFactory != nil {
		runner = s.cfg.CommandRunnerFactory(ref.ID)
	}
	env, err := tool.NewEnvironment(ref, ws, runner)
	if err != nil {
		return PlacementBinding{}, err
	}
	return PlacementBinding{Environment: env, Ref: ref}, nil
}

// Bind validates the request, delegates exactly one atomic operation to the
// provider, then validates that the returned environment and exact ref agree.
func (b *PlacementBinder) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	if b == nil || b.provider == nil {
		return PlacementBinding{}, fmt.Errorf("%w: PlacementProvider is required", ErrConfig)
	}
	if !req.Selector.Valid() || req.Scope == "" || !validPlacementOperation(req.Operation) {
		return PlacementBinding{}, ErrInvalidPlacementSelection
	}

	binding, err := b.provider.Bind(ctx, clonePlacementRequest(req))
	if err != nil {
		return PlacementBinding{}, err
	}
	if err := validatePlacementBinding(binding); err != nil {
		return PlacementBinding{}, err
	}
	return binding, nil
}

// Reattach resolves only the exact persisted ref. Providers without the
// explicit reattachment capability fail closed; Bind is never used as fallback.
func (b *PlacementBinder) Reattach(ctx context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	if b == nil || b.provider == nil {
		return PlacementBinding{}, fmt.Errorf("%w: PlacementProvider is required", ErrConfig)
	}
	if !req.Ref.Valid() || req.Scope == "" {
		return PlacementBinding{}, ErrInvalidPlacementSelection
	}
	provider, ok := b.provider.(PlacementReattacher)
	if !ok {
		return PlacementBinding{}, fmt.Errorf("%w: PlacementProvider does not support exact reattachment", ErrPlacementUnavailable)
	}
	req.Principal = req.Principal.Clone()
	binding, err := provider.Reattach(ctx, req)
	if err != nil {
		return PlacementBinding{}, err
	}
	if binding.Ref != req.Ref {
		return PlacementBinding{}, ErrInvalidPlacementBinding
	}
	if err := validatePlacementBinding(binding); err != nil {
		return PlacementBinding{}, err
	}
	return binding, nil
}

func clonePlacementRequest(req PlacementBindRequest) PlacementBindRequest {
	req.Principal = req.Principal.Clone()
	return req
}

func validPlacementOperation(operation PlacementOperation) bool {
	return operation == PlacementOperationCreate || operation == PlacementOperationSuccessor
}

const (
	maxPlacementKindRunes     = 64
	maxPlacementIdentityRunes = 256
	maxPlacementNameRunes     = 128
	maxPlacementDetailRunes   = 512
)

type boundWorkspaceRunner interface {
	BoundWorkspaceRoot() string
}

func validatePlacementBinding(binding PlacementBinding) error {
	if !binding.Ref.Valid() ||
		!safePlacementText(string(binding.Ref.Kind), maxPlacementKindRunes) ||
		!safePlacementText(binding.Ref.ID, maxPlacementIdentityRunes) ||
		!safePlacementText(binding.Ref.Revision, maxPlacementIdentityRunes) {
		return ErrInvalidPlacementBinding
	}
	if binding.Environment.Workspace() == nil {
		return ErrInvalidPlacementBinding
	}
	if binding.Environment.Ref() != binding.Ref {
		return ErrInvalidPlacementBinding
	}
	if runner := binding.Environment.CommandRunner(); runner != nil {
		bound, ok := runner.(boundWorkspaceRunner)
		if !ok || bound.BoundWorkspaceRoot() != binding.Environment.Workspace().Root() {
			return ErrInvalidPlacementBinding
		}
	}
	if !safeOptionalPlacementText(binding.Metadata.Name, maxPlacementNameRunes) ||
		!safeOptionalPlacementText(binding.Metadata.Description, maxPlacementDetailRunes) {
		return ErrInvalidPlacementBinding
	}
	return nil
}

func safeOptionalPlacementText(value string, maxRunes int) bool {
	return value == "" || safePlacementText(value, maxRunes)
}

func safePlacementText(value string, maxRunes int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
