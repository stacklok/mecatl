package client

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuthReason is the closed, proto-free auth-recovery contract passed from the
// transport boundary to the TUI. It contains no token, principal, or server text.
type AuthReason string

const (
	// AuthNotEnrolled means no saved credential was supplied.
	AuthNotEnrolled AuthReason = "not_enrolled"
	// AuthSessionExpired means a stored session can no longer refresh.
	AuthSessionExpired AuthReason = "session_expired"
	// AuthCredentialUnusable means local credential storage cannot be used.
	AuthCredentialUnusable AuthReason = "credential_unusable" // #nosec G101 -- closed diagnostic label, not a credential.
	// AuthCredentialCleanup means rejected-credential cleanup needs a retry.
	AuthCredentialCleanup AuthReason = "credential_cleanup" // #nosec G101 -- closed diagnostic label, not a credential.
	// AuthRejected means the remote server rejected a supplied bearer.
	AuthRejected AuthReason = "rejected"
)

// AuthError retains an exact local authentication cause across the per-RPC
// credential boundary. Callers must classify it with AuthFailure; no error-text
// parsing is used for local credential failures.
type AuthError struct{ Reason AuthReason }

func (e *AuthError) Error() string { return "authentication unavailable: " + string(e.Reason) }

// AuthFailure returns a safe recovery reason. Composition-mapped local credential
// failures retain their closed AuthError reason. A server Unauthenticated is
// rejected when a bearer was supplied, and means not_enrolled for an anonymous
// dial; all other failures deliberately remain unclassified.
func AuthFailure(err error, bearerBacked bool) (AuthReason, bool) {
	if reason, ok := localAuthFailure(err); ok {
		return reason, true
	}
	if status.Code(err) == codes.Unauthenticated {
		if bearerBacked {
			return AuthRejected, true
		}
		return AuthNotEnrolled, true
	}
	return "", false
}

func localAuthFailure(err error) (AuthReason, bool) {
	var local *AuthError
	if !errors.As(err, &local) {
		return "", false
	}
	switch local.Reason {
	case AuthNotEnrolled, AuthSessionExpired, AuthCredentialUnusable, AuthCredentialCleanup, AuthRejected:
		return local.Reason, true
	default:
		return "", false
	}
}
