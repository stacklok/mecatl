// Package mcpbroker defines the consumer-facing boundary to an MCP broker.
// Implementations may be in-process or remote; storage and transport details do
// not cross this boundary.
package mcpbroker

import (
	"context"
	"errors"

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
	Binding() string
	// Tools returns independently owned wrappers bound to this attachment.
	Tools() []tool.Tool
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
