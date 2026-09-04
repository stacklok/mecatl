package client

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type pagingReflectionClient struct {
	calls []string
}

func (c *pagingReflectionClient) ListLearningProposals(_ context.Context, _, cursor string, _ int, project string) (LearningProposalPage, error) {
	c.calls = append(c.calls, project+":"+cursor)
	return LearningProposalPage{NextCursor: project + "-next", Proposals: []LearningProposal{{ID: project + "-proposal"}}}, nil
}
func (*pagingReflectionClient) GetLearningProposal(context.Context, string, string) (LearningProposal, error) {
	return LearningProposal{}, nil
}
func (*pagingReflectionClient) DecideLearningProposal(context.Context, string, string, string, string, string) (LearningProposal, error) {
	return LearningProposal{}, nil
}
func (*pagingReflectionClient) UndoLearningPromotion(context.Context, string, string, string) (LearningProposal, error) {
	return LearningProposal{}, nil
}
func (*pagingReflectionClient) ReflectSession(context.Context, string) (ReflectionReceipt, error) {
	return ReflectionReceipt{}, nil
}

func TestADR_0298_MecatuiClientMapsClosedSafeAbstentionReason(t *testing.T) {
	got := mapReflectionReceipt(&mecatlv1.ReflectionReceipt{
		ReflectionId: "id\xff\x1b[31m",
		Disposition:  "abstained\x00selected",
		Abstained:    true,
		Reason:       "no_eligible_evidence\x1b",
		Message:      "attacker-controlled text\xff\nforged",
	})
	if got.Reason != "no_eligible_evidence" || got.Message != "No eligible evidence was available for reflection." || !got.Abstained {
		t.Fatalf("closed abstention mapping = %+v", got)
	}
	for name, value := range map[string]string{"id": got.ID, "disposition": got.Disposition, "reason": got.Reason, "message": got.Message} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\x1b\n\r") {
			t.Fatalf("%s is not output safe: %q", name, value)
		}
	}
}

func TestMapLearningProposalPreservesInformedReviewFields(t *testing.T) {
	got := mapLearningProposal(&mecatlv1.LearningProposal{
		Id: "p", Version: "v", Status: ProposalStatusStaged, Kind: "operator_fact",
		Key: "user/output", Value: "concise", Description: "preferred style",
		Evidence: []*mecatlv1.LearningEvidenceRef{{SessionId: "s", Locator: "event", Ordinal: 2, EventSeq: 7, ToolCallId: "call", Digest: "digest", Available: true, Availability: "available"}},
		Triggers: []string{"explicit_remember"},
	})
	if got.Key != "user/output" || got.Value != "concise" || got.Description != "preferred style" || len(got.Evidence) != 1 {
		t.Fatalf("proposal mapping = %+v", got)
	}
	evidence := got.Evidence[0]
	if evidence.SessionID != "s" || evidence.Locator != "event" || evidence.Ordinal != 2 || evidence.EventSeq != 7 || evidence.ToolCallID != "call" || evidence.Digest != "digest" || !evidence.Available {
		t.Fatalf("evidence mapping = %+v", evidence)
	}
}

func TestListReflectionsUsesIndependentScopeCursors(t *testing.T) {
	client := &pagingReflectionClient{}
	msg := ListReflectionsCmd(context.Background(), client, "", ReflectionCursors{Operator: "operator-cursor", Project: "project-cursor"}, "/project", 9)().(ReflectionsMsg)
	if msg.Err != nil || msg.Generation != 9 || msg.Page.OperatorNextCursor != "-next" || msg.Page.ProjectNextCursor != "/project-next" || len(msg.Page.Proposals) != 2 {
		t.Fatalf("message = %+v", msg)
	}
	if len(client.calls) != 2 || client.calls[0] != ":operator-cursor" || client.calls[1] != "/project:project-cursor" {
		t.Fatalf("calls = %v", client.calls)
	}
	if msg.Page.Proposals[1].Project != "/project" {
		t.Fatalf("project proposal lost partition = %+v", msg.Page.Proposals[1])
	}
}

func TestListReflectionsDoesNotRestartExhaustedScope(t *testing.T) {
	client := &pagingReflectionClient{}
	msg := ListReflectionsCmd(context.Background(), client, "", ReflectionCursors{OperatorDone: true, Project: "project-cursor"}, "/project", 10)().(ReflectionsMsg)
	if msg.Err != nil || len(client.calls) != 1 || client.calls[0] != "/project:project-cursor" || len(msg.Page.Proposals) != 1 {
		t.Fatalf("message=%+v calls=%v", msg, client.calls)
	}
}
