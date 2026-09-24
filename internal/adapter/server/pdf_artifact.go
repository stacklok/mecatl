package server

import (
	"context"
	"io"

	"github.com/stacklok/mecatl/engine/session"
)

// PDFArtifact is bounded, server-computed metadata for a session-owned PDF.
// Neither a storage key nor object URL crosses this boundary.
type PDFArtifact struct {
	ID     string
	Name   string
	Size   int64
	SHA256 string
}

// PDFArtifactLifecycle is the host-owned storage seam used by the API relay
// and session composition. Implementations enforce session scoping on every
// lookup; the relay additionally enforces caller ownership before invocation.
type PDFArtifactLifecycle interface {
	Stage(context.Context, session.SessionID, string, io.Reader) (PDFArtifact, error)
	Resolve(context.Context, session.SessionID, string) (PDFArtifact, error)
	Open(context.Context, session.SessionID, string) (PDFArtifact, io.ReadCloser, error)
	CommitPrompt(context.Context, session.SessionID, []string) error
	CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error)
	DiscardUnpublished(context.Context, session.SessionID) error
	Reconcile(context.Context) error
}
