// Package mcpbroker defines the consumer-facing boundary to an MCP broker.
// Implementations may be in-process or remote; storage and transport details do
// not cross this boundary.
package mcpbroker

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var (
	// ErrStateUnavailable means the logical broker session or its transaction
	// state cannot be recovered. Callers should resolve a parked authorization
	// deterministically rather than silently creating a replacement transaction.
	ErrStateUnavailable = errors.New("mcp broker state unavailable")
	// ErrAttachmentClosed means an operation used a locally closed attachment.
	ErrAttachmentClosed = errors.New("mcp broker attachment closed")
	// ErrAuthorizationNotFound means the exact authorization reference is unknown
	// to the attached logical session.
	ErrAuthorizationNotFound = errors.New("mcp broker authorization not found")
	// ErrInvalidWorkspaceCatalogue means a broker catalogue could not be frozen
	// into a safe, internally consistent snapshot.
	ErrInvalidWorkspaceCatalogue = errors.New("mcp broker invalid workspace catalogue")
)

// AttachOutcome is the closed result vocabulary for AttachSession.
type AttachOutcome string

const (
	// AttachCreated means the logical broker session was created.
	AttachCreated AttachOutcome = "created"
	// AttachReattached means an attachment was opened to existing logical state.
	AttachReattached AttachOutcome = "reattached"
)

// CloseOutcome is the closed, idempotent result vocabulary for Attachment.Close.
type CloseOutcome string

const (
	// CloseClosed means this call released the local attachment.
	CloseClosed CloseOutcome = "closed"
	// CloseAlreadyClosed means the attachment had already been released.
	CloseAlreadyClosed CloseOutcome = "already_closed"
)

// DeleteOutcome is the closed, idempotent result vocabulary for DeleteSession.
type DeleteOutcome string

const (
	// DeleteDeleted means logical broker state existed and was deleted.
	DeleteDeleted DeleteOutcome = "deleted"
	// DeleteNotFound means no logical broker state existed; the requested end
	// state is already satisfied.
	DeleteNotFound DeleteOutcome = "not_found"
)

// CancelOutcome is the closed, idempotent result vocabulary for cancellation.
type CancelOutcome string

const (
	// CancelCancelled means this call cancelled the pending authorization.
	CancelCancelled CancelOutcome = "cancelled"
	// CancelAlreadyCancelled means that exact authorization was already cancelled.
	CancelAlreadyCancelled CancelOutcome = "already_cancelled"
	// CancelAlreadyResolved means that exact authorization had another terminal
	// status. AuthorizationStatus reports which one.
	CancelAlreadyResolved CancelOutcome = "already_resolved"
)

// WorkspaceEnrollmentStatus is the closed broker-authored state of one
// pre-prompt workspace enrollment.
type WorkspaceEnrollmentStatus string

// Workspace enrollment statuses.
const (
	WorkspaceEnrollmentPending   WorkspaceEnrollmentStatus = "pending"
	WorkspaceEnrollmentConnected WorkspaceEnrollmentStatus = "connected"
	WorkspaceEnrollmentDenied    WorkspaceEnrollmentStatus = "denied"
	WorkspaceEnrollmentCancelled WorkspaceEnrollmentStatus = "cancelled"
	WorkspaceEnrollmentExpired   WorkspaceEnrollmentStatus = "expired"
	WorkspaceEnrollmentFailed    WorkspaceEnrollmentStatus = "failed"
)

// Valid reports whether s is a defined workspace-enrollment status.
func (s WorkspaceEnrollmentStatus) Valid() bool {
	switch s {
	case WorkspaceEnrollmentPending, WorkspaceEnrollmentConnected,
		WorkspaceEnrollmentDenied, WorkspaceEnrollmentCancelled,
		WorkspaceEnrollmentExpired, WorkspaceEnrollmentFailed:
		return true
	default:
		return false
	}
}

// WorkspaceEnrollmentRef is the durable, presentation-safe correlation for one
// broker-owned enrollment. It contains no URL, credential, backend selection, or
// discovered definition.
type WorkspaceEnrollmentRef struct {
	ID               session.WorkspaceEnrollmentID
	RequiredServices uint32
	ExpiresAt        time.Time
}

// Valid reports whether r is a complete enrollment reference.
func (r WorkspaceEnrollmentRef) Valid() bool {
	return r.ID.Valid() && r.RequiredServices > 0 && !r.ExpiresAt.IsZero()
}

// WorkspaceEnrollmentPresentation is the ephemeral browser presentation for an
// enrollment. URL deliberately exists only on this non-durable value.
type WorkspaceEnrollmentPresentation struct {
	Ref WorkspaceEnrollmentRef
	URL string
}

// Valid reports whether p has a valid reference and an absolute HTTP(S) URL.
func (p WorkspaceEnrollmentPresentation) Valid() bool {
	parsed, err := url.ParseRequestURI(p.URL)
	return p.Ref.Valid() && err == nil && parsed.Host != "" &&
		(parsed.Scheme == "https" || parsed.Scheme == "http")
}

// WorkspaceCatalogue is an immutable snapshot of the complete frozen tool
// catalogue proven by one exact enrollment. Construct it with
// NewWorkspaceCatalogue; its accessors return defensive copies. The private
// marker seals construction to this package.
type WorkspaceCatalogue interface {
	Valid() bool
	Ref() WorkspaceEnrollmentRef
	Tools() []tool.Tool
	ToolNames() []string
	workspaceCatalogue()
}

type workspaceCatalogue struct {
	ref       WorkspaceEnrollmentRef
	tools     []tool.Tool
	toolNames []string
}

// NewWorkspaceCatalogue snapshots each tool's specification exactly once and
// returns executable wrappers whose Spec method serves that frozen value.
func NewWorkspaceCatalogue(ref WorkspaceEnrollmentRef, tools []tool.Tool) (WorkspaceCatalogue, error) {
	if !ref.Valid() {
		return nil, ErrInvalidWorkspaceCatalogue
	}
	frozen := make([]tool.Tool, 0)
	names := make([]string, 0)
	for _, candidate := range tools {
		if nilTool(candidate) {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		if _, planOnly := candidate.(tool.PlanOnly); planOnly {
			// PlanOnly is a composition-registered, model-visibility marker with
			// no MCP-protocol equivalent; it cannot legitimately arrive via
			// discovery. Refuse loudly rather than silently drop the marker.
			return nil, ErrInvalidWorkspaceCatalogue
		}
		spec := cloneToolSpec(candidate.Spec())
		if !session.ValidWorkspaceEnrollmentToolNames(append(names, spec.Name)) {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		names = append(names, spec.Name)
		_, serial := candidate.(tool.DispatchSerial)
		if requester, ok := candidate.(tool.AuthorizationRequester); ok {
			base := frozenAuthorizationTool{AuthorizationRequester: requester, spec: spec}
			if serial {
				frozen = append(frozen, &frozenAuthorizationSerialTool{frozenAuthorizationTool: base})
			} else {
				frozen = append(frozen, &base)
			}
		} else {
			base := frozenTool{Tool: candidate, spec: spec}
			if serial {
				frozen = append(frozen, &frozenSerialTool{frozenTool: base})
			} else {
				frozen = append(frozen, &base)
			}
		}
	}
	return &workspaceCatalogue{ref: ref, tools: frozen, toolNames: names}, nil
}

func (*workspaceCatalogue) workspaceCatalogue() {}

// Valid reports whether c was created by NewWorkspaceCatalogue.
func (*workspaceCatalogue) Valid() bool { return true }

// Ref returns the exact enrollment correlation proven by this catalogue.
func (c *workspaceCatalogue) Ref() WorkspaceEnrollmentRef { return c.ref }

// Tools returns a copied slice of executable tools bound to frozen specs.
func (c *workspaceCatalogue) Tools() []tool.Tool {
	return append([]tool.Tool(nil), c.tools...)
}

// ToolNames returns the copied exact authority tool-name set derived from the
// same frozen specs served by Tools.
func (c *workspaceCatalogue) ToolNames() []string {
	return append([]string(nil), c.toolNames...)
}

type frozenTool struct {
	tool.Tool
	spec tool.ToolSpec
}

func (t *frozenTool) Spec() tool.ToolSpec { return cloneToolSpec(t.spec) }

// Advertised forwards to the wrapped tool's own Disclosable projection when it
// has one, so the freeze boundary does not silently widen what the model sees
// (Catalog.AdvertisedSpecs falls back to Spec() for a non-Disclosable tool,
// which the frozen spec already serves correctly).
func (t *frozenTool) Advertised() tool.ToolSpec {
	if d, ok := t.Tool.(tool.Disclosable); ok {
		return d.Advertised()
	}
	return cloneToolSpec(t.spec)
}

type frozenAuthorizationTool struct {
	tool.AuthorizationRequester
	spec tool.ToolSpec
}

func (t *frozenAuthorizationTool) Spec() tool.ToolSpec { return cloneToolSpec(t.spec) }

func (t *frozenAuthorizationTool) Advertised() tool.ToolSpec {
	if d, ok := t.AuthorizationRequester.(tool.Disclosable); ok {
		return d.Advertised()
	}
	return cloneToolSpec(t.spec)
}

// frozenSerialTool/frozenAuthorizationSerialTool preserve the tool.DispatchSerial
// marker through the freeze boundary for a source that implements it. Wrapping
// unconditionally would serialize every broker tool; NewWorkspaceCatalogue
// selects the *Serial variant only when the source implements the marker.
type frozenSerialTool struct{ frozenTool }

func (*frozenSerialTool) DispatchSerialTool() {}

type frozenAuthorizationSerialTool struct{ frozenAuthorizationTool }

func (*frozenAuthorizationSerialTool) DispatchSerialTool() {}

func cloneToolSpec(spec tool.ToolSpec) tool.ToolSpec {
	spec.Schema = append([]byte(nil), spec.Schema...)
	return spec
}

func nilTool(candidate tool.Tool) bool {
	if candidate == nil {
		return true
	}
	value := reflect.ValueOf(candidate)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// WorkspaceEnrollmentResult is a broker-authored observation. A connected
// result proves one complete frozen catalogue; every other state forbids one.
type WorkspaceEnrollmentResult struct {
	Ref       WorkspaceEnrollmentRef
	Status    WorkspaceEnrollmentStatus
	Catalogue WorkspaceCatalogue
}

// Valid reports whether r is internally consistent and fail-closed.
func (r WorkspaceEnrollmentResult) Valid() bool {
	if !r.Ref.Valid() || !r.Status.Valid() {
		return false
	}
	if r.Status != WorkspaceEnrollmentConnected {
		return r.Catalogue == nil
	}
	return r.Catalogue != nil && r.Catalogue.Ref() == r.Ref && r.Catalogue.Valid()
}

// WorkspaceEnrollmentAttachment is the optional enrollment capability of an
// Attachment. Callers may begin, observe, cancel, or explicitly reset broker-
// owned state, but can never submit status, discovered definitions, authority,
// or success.
type WorkspaceEnrollmentAttachment interface {
	ResetWorkspaceEnrollment(context.Context) error
	BeginWorkspaceEnrollment(context.Context) (WorkspaceEnrollmentPresentation, error)
	ObserveWorkspaceEnrollment(context.Context, WorkspaceEnrollmentRef) (WorkspaceEnrollmentResult, error)
	CancelWorkspaceEnrollment(context.Context, WorkspaceEnrollmentRef) (WorkspaceEnrollmentResult, error)
}

// Service attaches consumers to broker state keyed by the stable mecatl session
// identity. AttachSession does not transfer ownership: multiple process-local
// attachments may refer to the same logical state. DeleteSession, unlike Close,
// durably and idempotently destroys that logical state.
type Service interface {
	AttachSession(context.Context, session.SessionID) (Attachment, AttachOutcome, error)
	// DeleteSession durably invalidates every attachment to the deleted logical
	// session incarnation. A later AttachSession with the same SessionID creates a
	// new incarnation; stale attachments must return ErrStateUnavailable. Any
	// generation or fencing mechanism used to enforce this remains implementation-private.
	DeleteSession(context.Context, session.SessionID) (DeleteOutcome, error)
}

// Attachment is a process-local handle to one logical broker session.
//
// Authorization operations take session.ExternalAuthorization so callers reuse
// the aggregate's existing value instead of a second broker DTO. Identity is the
// stable authorization ID plus opaque AuthorizationBinding; ExpiresAt is freshness
// metadata and must not participate in lookup equality. Implementations must not
// infer a transaction from only the session or authorization ID.
type Attachment interface {
	// Commit publishes a newly created logical session after its host session is
	// durable. It is idempotent. On a reattached handle it is a no-op. The
	// implementation keeps any creation token private so remote brokers can provide
	// the same transaction without exposing storage generations or CAS values.
	Commit(context.Context) error
	// Abort abandons this attachment's uncommitted creation and closes the local
	// handle. It may delete logical state only while that creation is still private;
	// once another attachment has observed the session, Abort must preserve that
	// peer and degrade to local close. It is idempotent.
	Abort(context.Context) error
	// Binding is the opaque identity of this exact logical-session incarnation.
	// It is persisted by the host and must match exactly on reattachment.
	Binding() session.ExternalBinding
	// Tools returns independently owned wrappers bound to this attachment.
	Tools() []tool.Tool
	// RefreshGrantedAuthorizationCatalogue atomically replaces static protected
	// declarations with authenticated metadata after the exact bundle grant. A
	// valid non-bundle grant returns the unchanged catalogue. The returned snapshot
	// is independently owned for the host's model-visible catalog.
	RefreshGrantedAuthorizationCatalogue(context.Context, session.ExternalAuthorization) ([]tool.Tool, error)
	// PresentAuthorization returns the live presentation URL for the exact
	// authorization. The URL is deliberately an ephemeral return value: it is not
	// part of ExternalAuthorization or any broker reference intended for storage.
	PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error)
	// AuthorizationStatus queries the exact authorization and remains available
	// after closing an old attachment and reattaching to the logical session.
	AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error)
	// CancelAuthorization precisely cancels the exact authorization reference.
	CancelAuthorization(context.Context, session.ExternalAuthorization) (CancelOutcome, error)
	// Close releases only this local attachment and is idempotent. It never
	// deletes logical broker state.
	Close(context.Context) (CloseOutcome, error)
}
