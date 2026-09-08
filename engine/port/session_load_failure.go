package port

import (
	"errors"
	"fmt"
)

// SessionLoadFailureClass is the bounded operator-facing classification of a
// non-not-found SessionStore.Load failure.
type SessionLoadFailureClass uint8

const (
	// SessionLoadFailureUnknown is the fail-safe class for unrecognized failures.
	SessionLoadFailureUnknown SessionLoadFailureClass = iota
	// SessionLoadFailureStore identifies retrieval or transport failures.
	SessionLoadFailureStore
	// SessionLoadFailureSnapshot identifies snapshot decode or validation failures.
	SessionLoadFailureSnapshot
)

// Valid reports whether c is one of the closed load-failure classes.
func (c SessionLoadFailureClass) Valid() bool {
	return c <= SessionLoadFailureSnapshot
}

// String returns the bounded metric/log label for c. Unknown values fail closed
// to the unknown label.
func (c SessionLoadFailureClass) String() string {
	switch c {
	case SessionLoadFailureStore:
		return "store"
	case SessionLoadFailureSnapshot:
		return "snapshot"
	default:
		return "unknown"
	}
}

// ErrSessionLoadFailure identifies a classified non-not-found Load failure.
var ErrSessionLoadFailure = errors.New("port: session load failure")

// SessionLoadFailureError carries a closed failure class while preserving the
// adapter's original cause for trusted in-process error inspection.
type SessionLoadFailureError struct {
	class SessionLoadFailureClass
	cause error
}

// NewSessionLoadFailure wraps cause with class. Genuine not-found errors pass
// through unchanged so ordinary absence remains silent to observability callers.
func NewSessionLoadFailure(class SessionLoadFailureClass, cause error) error {
	if cause == nil || errors.Is(cause, ErrSessionNotFound) {
		return cause
	}
	if !class.Valid() {
		class = SessionLoadFailureUnknown
	}
	return &SessionLoadFailureError{class: class, cause: cause}
}

// Error implements error.
func (e *SessionLoadFailureError) Error() string {
	return fmt.Sprintf("%s (%s): %v", ErrSessionLoadFailure, e.class, e.cause)
}

// Unwrap preserves errors.Is/errors.As traversal to the underlying cause.
func (e *SessionLoadFailureError) Unwrap() error { return e.cause }

// Is makes every classified failure match ErrSessionLoadFailure.
func (*SessionLoadFailureError) Is(target error) bool { return target == ErrSessionLoadFailure }

// Class returns the bounded load-failure class.
func (e *SessionLoadFailureError) Class() SessionLoadFailureClass { return e.class }

// ClassifySessionLoadFailure returns the typed class carried by err. It never
// infers a class from error text; unrecognized and not-found errors are unknown.
func ClassifySessionLoadFailure(err error) SessionLoadFailureClass {
	var classified *SessionLoadFailureError
	if errors.As(err, &classified) {
		return classified.Class()
	}
	return SessionLoadFailureUnknown
}
