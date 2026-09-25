package server

import (
	"context"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// Artifact is bounded, server-computed metadata for a session-owned object.
// Neither a storage key nor object URL crosses this boundary.
type Artifact struct {
	ID       string
	Name     string
	MIMEType string
	Size     int64
	SHA256   string
}

// ArtifactLifecycle is the host-owned storage seam used by the API relay
// and session composition. Implementations enforce session scoping on every
// lookup; the relay additionally enforces caller ownership before invocation.
type ArtifactLifecycle interface {
	Stage(context.Context, session.SessionID, string, string, io.Reader) (Artifact, error)
	Resolve(context.Context, session.SessionID, string) (Artifact, error)
	Open(context.Context, session.SessionID, string) (Artifact, io.ReadCloser, error)
	CommitPrompt(context.Context, session.SessionID, []string) error
	CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error)
	DiscardUnpublished(context.Context, session.SessionID) error
	Reconcile(context.Context) error
}
