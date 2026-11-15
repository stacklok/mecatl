package as1servertest

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// HarnessActorValidator is the identity harness's AS-1 checking actor tokens
// with v, as the real AS-1 does: an expired credential is
// identityharness.ErrActorExpired, any other refusal
// identityharness.ErrActorDenied, and anything else an outage. It lives here,
// not in the harness, because the harness never imports the broker.
func HarnessActorValidator(v identitybroker.CredentialVerifier) identityharness.ActorValidator {
	return func(ctx context.Context, owner identityharness.Owner, actor string) (string, time.Time, error) {
		got, err := v.VerifyCredentialForOwner(ctx, identitybroker.VerifyCredentialForOwnerRequest{
			Owner: identitybroker.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, Credential: identitybroker.NewToken(actor),
		})
		var e *identitybroker.Error
		switch {
		case err == nil:
			return got.AgentID, got.ExpiresAt, nil
		case errors.As(err, &e) && e.Code == identitybroker.CodeUnauthenticated && e.Reason == identitybroker.ReasonSVIDExpired:
			return "", time.Time{}, identityharness.ErrActorExpired
		case errors.As(err, &e) && e.Code == identitybroker.CodePermissionDenied:
			return "", time.Time{}, identityharness.ErrActorDenied
		}
		return "", time.Time{}, err
	}
}
