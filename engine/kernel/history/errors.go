package history

import (
	"errors"
	"fmt"
)

// ErrInvalid marks a malformed public history value or request. Its wrapped
// error never includes caller-provided payload contents.
var ErrInvalid = errors.New("invalid history value")

// ErrCorrupt reports backend bytes or indexed evidence that cannot be verified.
// Decode failures include stored DTOs outside this instance's codec field/count
// limits, not just physical corruption. No authoritative verdict or partial page
// accompanies this error.
var ErrCorrupt = errors.New("corrupt history backend result")

// ErrNotFound reports that a history does not exist.
var ErrNotFound = errors.New("history not found")

// ErrPageTooSmall reports that the requested page byte budget cannot hold its
// first commit.
var ErrPageTooSmall = errors.New("history page too small")

// PageTooSmallError describes a page budget that cannot hold the first commit.
// RequiredBytes is a diagnostic size hint only; Read never raises the requested
// budget automatically. The hint is not permission to exceed configured caps.
type PageTooSmallError struct {
	// RequiredBytes is the first commit's encoded byte size at this observation.
	RequiredBytes int
}

// Error returns a stable error message without exposing commit payload values.
func (PageTooSmallError) Error() string { return ErrPageTooSmall.Error() }

// Unwrap marks PageTooSmallError as ErrPageTooSmall.
func (PageTooSmallError) Unwrap() error { return ErrPageTooSmall }

// invalid wraps a contract-validation reason without incorporating untrusted
// values into errors.
func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }
