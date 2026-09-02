package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const inTreeEnvironmentRevision = "in-tree-v1"

var (
	// ErrInvalidPlacementSelection reports a malformed selector or binding request.
	ErrInvalidPlacementSelection = errors.New("server: invalid placement selection")
	// ErrPlacementNotFound deliberately covers absent and authorization-hidden selectors.
	ErrPlacementNotFound = errors.New("server: placement selector not found")
	// ErrPlacementStale reports an expired selector without disclosing hidden choices.
	ErrPlacementStale = errors.New("server: placement selector is stale")
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

// PlacementSelector is the private server/provider binding protocol. It never
// crosses the engine or public transport boundary.
type PlacementSelector struct {
	Kind      PlacementSelectorKind
	ID        string
	Source    session.SessionID
	SourceRef session.EnvironmentRef
}

// PlacementSelectorKind is the closed private binding vocabulary.
type PlacementSelectorKind string

// Private placement selector kinds.
const (
	PlacementSelectorDefault  PlacementSelectorKind = "default"
	PlacementSelectorNoFS     PlacementSelectorKind = "no-fs"
	PlacementSelectorID       PlacementSelectorKind = "id"
	PlacementSelectorWorktree PlacementSelectorKind = "worktree"
)

// DefaultPlacement selects the provider-owned deployment default.
func DefaultPlacement() PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorDefault}
}

// NoFSPlacement selects explicit filesystem attenuation.
func NoFSPlacement() PlacementSelector { return PlacementSelector{Kind: PlacementSelectorNoFS} }

// SelectPlacementID carries a private in-process provider hint.
func SelectPlacementID(id string) PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorID, ID: id}
}

// SelectWorktree carries a source-scoped ephemeral selector to the provider.
func SelectWorktree(source session.SessionID, ref session.EnvironmentRef, token string) PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorWorktree, ID: token, Source: source, SourceRef: ref}
}

// IsDefault reports whether the deployment default was selected.
func (s PlacementSelector) IsDefault() bool { return s.Kind == PlacementSelectorDefault }

// IsNoFS reports whether filesystem attenuation was selected.
func (s PlacementSelector) IsNoFS() bool { return s.Kind == PlacementSelectorNoFS }

// IsID reports whether a private in-process hint was selected.
func (s PlacementSelector) IsID() bool { return s.Kind == PlacementSelectorID }

// IsWorktree reports whether a source-scoped worktree token was selected.
func (s PlacementSelector) IsWorktree() bool { return s.Kind == PlacementSelectorWorktree }

// Valid reports whether the selector has exactly one valid protocol shape.
func (s PlacementSelector) Valid() bool {
	switch s.Kind {
	case PlacementSelectorDefault, PlacementSelectorNoFS:
		return s.ID == "" && s.Source == "" && !s.SourceRef.Valid()
	case PlacementSelectorID:
		return s.ID != "" && s.Source == "" && !s.SourceRef.Valid()
	case PlacementSelectorWorktree:
		return s.ID != "" && s.Source != "" && s.SourceRef.Valid()
	default:
		return false
	}
}

// PlacementBindRequest contains the complete authorization context a provider
// needs to atomically authorize and resolve one placement record version.
type PlacementBindRequest struct {
	Selector  PlacementSelector
	Principal *session.Principal
	Scope     PlacementScope
	Operation PlacementOperation
}

// PlacementMetadata is the bounded, display-safe provider projection returned
// with a binding. It contains no roots, locators, credentials, or authority.
type PlacementMetadata struct {
	Kind     string
	Label    string
	Branch   string
	Revision string
	// Name and Description are retained only for source compatibility with
	// private providers; canonical projection uses Label/Branch/Revision.
	Name        string
	Description string
}

// PlacementBinding is the indivisible successful result of Bind.
type PlacementBinding struct {
	Environment tool.Environment
	Ref         session.EnvironmentRef
	Metadata    PlacementMetadata
	// Close releases provisional provider resources. It is called after creation
	// because ordinary bindings are reattached fresh at run entry.
	Close func() error
}

// PlacementProvider owns placement inventory, authorization, and atomic new
// binding resolution.
type PlacementProvider interface {
	Bind(context.Context, PlacementBindRequest) (PlacementBinding, error)
}

// PrivatePlacementHintBinder marks trusted in-process providers that accept an
// opaque private placement hint. Public transports never provide one.
type PrivatePlacementHintBinder interface {
	AcceptsPrivatePlacementHints()
}

// PlacementDiscoveryRequest scopes alternate-worktree discovery to an owned
// source and its exact current placement.
type PlacementDiscoveryRequest struct {
	Source    session.SessionID
	SourceRef session.EnvironmentRef
	Principal *session.Principal
	Scope     PlacementScope
}

// PlacementDiscoverer is the optional provider-owned discovery half. The same
// provider that issues a selector must atomically consume it in Bind.
type PlacementDiscoverer interface {
	ListWorktrees(context.Context, PlacementDiscoveryRequest) ([]ScopedWorktree, error)
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

type placementProviderError struct {
	public error
	cause  error
}

func (e *placementProviderError) Error() string { return e.public.Error() }
func (e *placementProviderError) Unwrap() error { return e.public }

func sanitizePlacementProviderError(err error) error {
	if err == nil {
		return nil
	}
	public := ErrPlacementUnavailable
	for _, candidate := range []error{
		ErrInvalidPlacementSelection, ErrPlacementNotFound, ErrPlacementStale,
		ErrPlacementUnavailable, ErrPlacementChanged, ErrInvalidPlacementBinding,
	} {
		if errors.Is(err, candidate) {
			public = candidate
			break
		}
	}
	return &placementProviderError{public: public, cause: err}
}

func (s *Service) logPlacementProviderError(ctx context.Context, operation string, err error) {
	var providerErr *placementProviderError
	if s == nil || s.cfg.Diagnostics == nil || !errors.As(err, &providerErr) {
		return
	}
	words := strings.Fields(session.ToValidUTF8(providerErr.cause.Error()))
	for i, word := range words {
		if strings.ContainsAny(word, `/\\`) {
			words[i] = "[redacted]"
		}
	}
	detail := strings.Join(words, " ")
	if utf8.RuneCountInString(detail) > maxPlacementDetailRunes {
		detail = string([]rune(detail)[:maxPlacementDetailRunes])
	}
	s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "placement provider operation failed", "operation", operation, "cause", detail)
}

// NewPlacementBinder constructs the binding choke point.
func NewPlacementBinder(provider PlacementProvider) (*PlacementBinder, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: PlacementProvider is required", ErrConfig)
	}
	return &PlacementBinder{provider: provider}, nil
}

func configuredPlacementBinder(ctx context.Context, cfg Config) (*PlacementBinder, error) {
	if cfg.PlacementProvider == nil {
		return nil, fmt.Errorf("%w: PlacementProvider is required", ErrConfig)
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
	validation, err := binder.Bind(ctx, PlacementBindRequest{
		Selector: DefaultPlacement(), Scope: cfg.PlacementScope,
		Operation: PlacementOperationCreate,
	})
	if err != nil {
		return nil, fmt.Errorf("server: validate default placement: %w", err)
	}
	if validation.Close != nil {
		_ = validation.Close()
	}
	return binder, nil
}

func (s *Service) bindPlacementForCreate(ctx context.Context, workspace string, profile SessionProfile, owner *session.Principal) (string, *PlacementBinding, error) {
	selector := DefaultPlacement()
	if workspace != "" {
		if _, ok := s.cfg.PlacementProvider.(PrivatePlacementHintBinder); ok {
			selector = SelectPlacementID(workspace)
		}
	}
	if profile == ProfileNoFS {
		selector = NoFSPlacement()
	}
	binding, err := s.placementBinder.Bind(ctx, PlacementBindRequest{
		Selector: selector, Principal: owner, Scope: s.cfg.PlacementScope,
		Operation: PlacementOperationCreate,
	})
	if err != nil {
		return "", nil, err
	}
	boundRoot := binding.Environment.Workspace().Root()
	if profile == ProfileNoFS && boundRoot != "" {
		return "", nil, ErrInvalidPlacementBinding
	}
	return boundRoot, &binding, nil
}

func (s *Service) persistPlacedCreatedSession(ctx context.Context, sess *session.Session, owner *session.Principal, request *createRequest, placement *PlacementBinding) (*session.Session, error) {
	if placement != nil {
		sess.EnvironmentRef = placement.Ref
		sess.Placement = canonicalPlacementMetadata(*placement)
	}
	if !sess.EnvironmentRef.Valid() {
		return nil, fmt.Errorf("%w: placement did not provide an exact environment ref", ErrInvalidPlacementBinding)
	}
	return s.persistCreatedSession(ctx, sess, owner, request)
}

func (s *Service) resolveSchedulePlacement(ctx context.Context, ref session.EnvironmentRef, profile SessionProfile) (session.EnvironmentRef, string, SessionProfile, error) {
	if profile == ProfileNoFS {
		binding, err := s.BindPlacement(ctx, NoFSPlacement(), PlacementOperationCreate)
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
		binding, err = s.BindPlacement(ctx, DefaultPlacement(), PlacementOperationCreate)
	}
	if err != nil {
		return session.EnvironmentRef{}, "", profile, err
	}
	return binding.Ref, string(s.cfg.PlacementScope), ProfileDefault, nil
}

func (s *Service) privateWorkspace(ctx context.Context, sess *session.Session) (string, error) {
	s.mu.Lock()
	env, ok := s.sessionEnvironments[sess.ID]
	s.mu.Unlock()
	if ok && env.Ref() == sess.EnvironmentRef && env.Workspace() != nil {
		return env.Workspace().Root(), nil
	}
	binding, err := s.ReattachPlacement(ctx, sess.EnvironmentRef)
	if err != nil {
		return "", err
	}
	return binding.Environment.Workspace().Root(), nil
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
		return PlacementBinding{}, sanitizePlacementProviderError(err)
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
		return PlacementBinding{}, sanitizePlacementProviderError(err)
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
	return nil
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

func canonicalPlacementMetadata(binding PlacementBinding) session.PlacementMetadata {
	label := binding.Metadata.Label
	if label == "" {
		label = binding.Metadata.Name
	}
	return session.PlacementMetadata{
		Kind:     sanitizePlacementDisplay(string(binding.Ref.Kind), maxPlacementKindRunes),
		Label:    sanitizePlacementDisplay(label, maxPlacementNameRunes),
		Branch:   sanitizePlacementDisplay(binding.Metadata.Branch, maxPlacementNameRunes),
		Revision: sanitizePlacementDisplay(binding.Metadata.Revision, maxPlacementIdentityRunes),
	}
}

func sanitizePlacementDisplay(value string, maxRunes int) string {
	value = session.ToValidUTF8(value)
	var b strings.Builder
	for _, r := range value {
		if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
			b.WriteRune(r)
		}
	}
	value = strings.TrimSpace(b.String())
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) ||
		(len(value) > 2 && value[1] == ':' && (value[2] == '/' || value[2] == '\\')) {
		return ""
	}
	return value
}
