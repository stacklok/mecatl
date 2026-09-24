package mcpbrokergrpc_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type offeringAttachment struct {
	*attachment
	offer bool
}

func (a *offeringAttachment) CredentialContinuity() bool { return a.offer }

type offeringBroker struct {
	*broker
	offer bool
}

func (b *offeringBroker) AttachSession(ctx context.Context, id session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	handle, outcome, err := b.broker.AttachSession(ctx, id)
	if err != nil {
		return nil, outcome, err
	}
	return &offeringAttachment{attachment: handle.(*attachment), offer: b.offer}, outcome, nil
}

// The broker's per-attachment continuity offer must reach the host exactly, so
// a broker without encrypted custody (or an older broker) keeps the legacy
// enrollment path instead of looking like a continuity failure.
func TestCredentialContinuityOfferCrossesTheWire(t *testing.T) {
	for _, offer := range []bool{true, false} {
		remote := newRemote(t, &offeringBroker{broker: newBroker(), offer: offer})
		handle, _, err := remote.AttachSession(context.Background(), "session-1")
		if err != nil {
			t.Fatal(err)
		}
		advertiser, ok := handle.(mcpbroker.CredentialContinuityAdvertiser)
		if !ok {
			t.Fatal("remote handle does not report the continuity offer")
		}
		if advertiser.CredentialContinuity() != offer {
			t.Fatalf("offer = %v, want %v", advertiser.CredentialContinuity(), offer)
		}
	}
}
