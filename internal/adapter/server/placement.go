package server

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

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

// PlacementProvider owns placement inventory, authorization, and resolution.
// Bind is deliberately its only operation: implementations must authorize and
// resolve one immutable record/revision atomically and fail on inventory drift.
type PlacementProvider interface {
	Bind(context.Context, PlacementBindRequest) (PlacementBinding, error)
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
