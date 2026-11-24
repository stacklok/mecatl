package identitybroker_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

// TestAuthorizeCallChecksANamedRootsOwnCeiling pins check 7 for the main
// agent of a session started with a named definition (#2938): it registers
// as a root instance of that definition, with no parent to narrow it, and
// its calls are checked against the definition's own ceiling, never the
// built-in main agent's, which grants every gateway server.
func TestAuthorizeCallChecksANamedRootsOwnCeiling(t *testing.T) {
	f := ceilingFixture{perCallFixture: newPerCall(t)}
	named, err := f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: "hostS-named", Incarnation: "inc_nbcdefghijklmnopqrstuvwxya", Owner: testOwner, AccessToken: unsignedToken(t, testOwner),
	})
	if err != nil || !named.HoldsRefreshToken {
		t.Fatalf("OpenSession = %+v, %v; want a session holding a grant", named, err)
	}
	root, err := f.b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
		SessionID: named.ID, HostSessionID: named.HostSessionID, Owner: named.Owner, Incarnation: named.Incarnation,
		Definition: conformance.ReviewerDefinition,
	})
	if err != nil {
		t.Fatalf("RegisterAgent of the named root: %v", err)
	}
	f.s, f.reviewerCred = named, root.Credential
	const ceiling = "calendar; github except " + conformance.ReviewerExcludedTool

	f.wantAllowed(t, f.call(root.Credential, "R50", "", "op-named-read", "pull_request_read"), ceiling)
	f.wantCeilingDenied(t, f.call(root.Credential, "R50", "", "op-named-merge", conformance.ReviewerExcludedTool), ceiling)
}
