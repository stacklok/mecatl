package session_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestModelResponseProviderCallIDsSubmitLoadAndSettlement(t *testing.T) {
	h, s, p := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	mustBatch(t, b.ReplaceModelBinding(run.SessionID(), session.ModelBinding{Route: "r", API: "a", Model: "m"}))
	submitBatch(t, s, b)
	providerIDs := []string{" exact provider/尾 \n", "duplicate", "duplicate", ""}
	reasons := []session.ToolNotInvokedReason{session.ToolNotInvokedUserDenied, session.ToolNotInvokedUnavailable, session.ToolNotInvokedInfrastructureFailed, session.ToolNotInvokedPolicyRejected}
	var requests []session.ModelRequestID
	var responses []int
	var calls []session.RequestedCallID
	for range 2 {
		request := addInteractionRequest(t, s, run, nil, nil)
		requests = append(requests, request)
		b = beginBatch(t, s)
		// Invalid UTF-8 is rejected, but missing/duplicate provider IDs and malformed
		// names/arguments are observations retained independently of kernel IDs.
		if err := b.AddModelResponse(request, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: "\xff"}}}}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid provider ID: %v", err)
		}
		response := session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: richContent()}}}}
		for _, id := range providerIDs {
			response.Items = append(response.Items, session.ResponseItem{Call: &session.RequestedCall{ProviderCallID: id, Name: "", Arguments: []byte{0xff, '{', 0}}})
		}
		mustBatch(t, b.AddModelResponse(request, response))
		response.Items[1].Call.ProviderCallID = "changed"
		clear(response.Items[1].Call.Arguments)
		submitBatch(t, s, b)
		responses = append(responses, s.Snapshot().RecordCount()-1)
		accepted, _ := s.Snapshot().RecordAt(responses[len(responses)-1]).ModelResponse()
		b = beginBatch(t, s)
		for i, reason := range reasons {
			call := accepted.CallAt(i).ID()
			calls = append(calls, call)
			mustBatch(t, b.AddToolDisposition(call, session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: reason}, Diagnostic: new("private diagnostic")}))
			if err := b.AddToolDisposition(call, session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: reason}}); !errors.Is(err, session.ErrTransition) {
				t.Fatalf("duplicate kernel settlement: %v", err)
			}
		}
		submitBatch(t, s, b)
	}
	if calls[0] == calls[1] || calls[1] == calls[2] || calls[0] == calls[4] {
		t.Fatal("provider IDs replaced kernel occurrence identities")
	}
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		t.Run(seam, func(t *testing.T) {
			snapshot := candidate.Snapshot()
			for r, position := range responses {
				response, ok := snapshot.RecordAt(position).ModelResponse()
				if !ok || response.RequestID() != requests[r] || response.CallCount() != len(providerIDs) {
					t.Fatal("response scope/call count")
				}
				for i, id := range providerIDs {
					call := response.CallAt(i)
					data, ok := response.Items().At(i + 1).Call()
					if !ok || data.ProviderCallID() != id || call.ProviderCallID() != id || call.ID() != calls[r*4+i] || call.ID().RequestID() != requests[r] || call.Name() != "" || !bytes.Equal(call.Arguments().Copy(), []byte{0xff, '{', 0}) {
						t.Fatal("provider/kernel call identity or opaque payload changed")
					}
					clear(call.Arguments().Copy())
					if n := testing.AllocsPerRun(100, func() {
						if call.ProviderCallID() != id || data.ProviderCallID() != id {
							panic("provider ID")
						}
					}); n != 0 {
						t.Fatalf("provider ID getter allocated %g times", n)
					}
					joined, ok := snapshot.RunAt(0).ToolCalls().ByID(call.ID())
					if !ok || joined.RequestedCall().ProviderCallID() != id {
						t.Fatal("tool join changed original provider ID")
					}
					disposition, ok := joined.Disposition()
					notInvoked, not := disposition.NotInvoked()
					settlement, settled := joined.Settlement()
					if !ok || !not || notInvoked.Reason() != reasons[i] || !settled || settlement.Category() != session.ToolSettlementNotInvoked {
						t.Fatal("new reason changed kernel settlement")
					}
				}
			}
		})
	}
	if (session.RequestedCallView{}).ProviderCallID() != "" || (session.RequestedCallDataView{}).ProviderCallID() != "" {
		t.Fatal("zero provider ID view")
	}
}
