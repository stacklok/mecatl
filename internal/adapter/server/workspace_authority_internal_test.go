package server

import (
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestValidateEnvironmentOverrideUsesAuthoritativeIdentity pins that the
// environment-override gate uses the same lexical root-identity rule as the other
// persisted-root gates, not a raw string compare. The Fileless case is the reason:
// there AuthoritativeWorkspace is "", so `ws.Root() != s.cfg.AuthoritativeWorkspace`
// ACCEPTS an empty-root override on a ProfileDefault session (both ""), where
// isAuthoritativeWorkspace fails closed. That path is unreachable via run entry
// (validatePersistedWorkspace rejects a ProfileDefault/Fileless session first), so
// this is a direct unit assertion of the defense-in-depth backstop.
func TestValidateEnvironmentOverrideUsesAuthoritativeIdentity(t *testing.T) {
	// profileForSession → ProfileDefault (non-empty workspace, non-remote ref), so
	// the no-FS early return does not fire and the root-identity check runs.
	defaultSession := func() *session.Session {
		return session.New("def", session.ModeDefault, "/some/root", session.Limits{}, time.Unix(0, 0))
	}
	emptyRootOverride := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS}, nofs.New(), nil)

	t.Run("fileless empty-root override fails closed", func(t *testing.T) {
		s := &Service{cfg: Config{WorkspaceAuthority: WorkspaceAuthorityFileless}}
		if err := s.validateEnvironmentOverride(defaultSession(), emptyRootOverride); !errors.Is(err, ErrFailedPrecondition) {
			t.Fatalf("validateEnvironmentOverride(fileless, empty-root) = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("server-assigned matching root is accepted", func(t *testing.T) {
		s := &Service{cfg: Config{WorkspaceAuthority: WorkspaceAuthorityServerAssigned, AuthoritativeWorkspace: "/dep/ws"}}
		match := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/dep/ws"}, memfs.NewWorkspace("/dep/ws"), nil)
		if err := s.validateEnvironmentOverride(defaultSession(), match); err != nil {
			t.Fatalf("validateEnvironmentOverride(server-assigned, matching root) = %v, want nil", err)
		}
	})

	t.Run("server-assigned off-root override fails closed", func(t *testing.T) {
		s := &Service{cfg: Config{WorkspaceAuthority: WorkspaceAuthorityServerAssigned, AuthoritativeWorkspace: "/dep/ws"}}
		off := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/other"}, memfs.NewWorkspace("/other"), nil)
		if err := s.validateEnvironmentOverride(defaultSession(), off); !errors.Is(err, ErrFailedPrecondition) {
			t.Fatalf("validateEnvironmentOverride(server-assigned, off-root) = %v, want ErrFailedPrecondition", err)
		}
	})
}

// TestClientSelectsRootFailsSafeForNewValues pins the reason the authority
// predicate is phrased around the single permissive value: a future authority
// constant (or an unknown value) must be treated as deployment-assigned — the
// gates enforce — rather than silently client-selectable. Only the explicit
// client-selected authority selects the root.
func TestClientSelectsRootFailsSafeForNewValues(t *testing.T) {
	if !WorkspaceAuthorityClientSelected.clientSelectsRoot() {
		t.Fatal("client-selected authority must let the caller select the root")
	}
	for _, a := range []WorkspaceAuthority{
		WorkspaceAuthorityServerAssigned,
		WorkspaceAuthorityFileless,
		WorkspaceAuthority(42), // a value added later, unhandled here
	} {
		if a.clientSelectsRoot() {
			t.Fatalf("authority %d must be deployment-assigned (fail safe), got client-selected", a)
		}
	}
}
