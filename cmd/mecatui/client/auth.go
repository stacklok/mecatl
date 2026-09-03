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
	// AuthNotEnrolled means a credential-free RPC reached a server that requires
	// caller authentication. The server, not target classification, owns this
	// decision.
	AuthNotEnrolled AuthReason = "not_enrolled"
	// AuthAnonymousRejected means the server required caller authentication after
	// --anonymous deliberately bypassed both static and saved OIDC credentials.
	AuthAnonymousRejected AuthReason = "anonymous_rejected"
	// AuthSessionExpired means a stored session can no longer refresh.
	AuthSessionExpired AuthReason = "session_expired"
	// AuthCredentialUnusable means the saved encrypted record itself is unusable
	// (e.g. corrupt) but reauthentication replaces it and repairs the failure.
	AuthCredentialUnusable AuthReason = "credential_unusable" // #nosec G101 -- closed diagnostic label, not a credential.
	// AuthStorageUnavailable means a local dependency the login flow cannot
	// replace by itself -- the registry, keyring, encrypted store, or the saved
	// issuer/CA preflight -- is unavailable. Reauthentication will not fix this;
	// the user must retry after restoring the local dependency.
	AuthStorageUnavailable AuthReason = "storage_unavailable" // #nosec G101 -- closed diagnostic label, not a credential.
	// AuthCredentialCleanup means rejected-credential cleanup needs a retry.
	AuthCredentialCleanup AuthReason = "credential_cleanup" // #nosec G101 -- closed diagnostic label, not a credential.
	// AuthTargetChanged means the saved target changed (a concurrent logout,
	// or a newer enrollment for the same target) while an interactive
	// reauthentication was in progress. The completed sign-in was discarded
	// rather than risk resurrecting a logged-out target or clobbering the
	// newer enrollment; retrying reauthentication is safe.
	AuthTargetChanged AuthReason = "target_changed" // #nosec G101 -- closed diagnostic label, not a credential.
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
// rejected when a bearer was supplied, and means not_enrolled for a
// credential-free dial; all other failures deliberately remain unclassified.
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
	var reasoned interface{ AuthReason() AuthReason }
	if errors.As(err, &reasoned) {
		return knownAuthReason(reasoned.AuthReason())
	}
	var local *AuthError
	if !errors.As(err, &local) {
		return "", false
	}
	return knownAuthReason(local.Reason)
}

func knownAuthReason(reason AuthReason) (AuthReason, bool) {
	switch reason {
	case AuthNotEnrolled, AuthAnonymousRejected, AuthSessionExpired, AuthCredentialUnusable, AuthStorageUnavailable, AuthCredentialCleanup, AuthTargetChanged, AuthRejected:
		return reason, true
	default:
		return "", false
	}
}
