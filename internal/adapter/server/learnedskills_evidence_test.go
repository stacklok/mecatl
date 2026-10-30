package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLearnedSkillAttachmentsRequireVerifiedCurrentEvidence(t *testing.T) {
	for _, kind := range []string{"v1", "missing-protocol", "forged-current", "current"} {
		t.Run(kind, func(t *testing.T) {
			call := session.NewToolCall(session.NewToolCallID(), "Read", nil)
			source := session.New("source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
			if err := source.SeedHistory([]session.Message{session.NewAssistantMessage("inspect source", []session.ToolCall{call}), session.NewToolMessage(session.NewToolResult(call.ID, "safe result"))}); err != nil {
				t.Fatal(err)
			}
			store := memstore.New()
			if err := store.Save(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			input := learning.NewInput(learning.NewSessionTrajectory(source, "", session.StopEndTurn, session.Usage{}), nil, nil, nil)
			ref, err := learning.MessageEvidenceRef(input, 0, call.ID)
			if err != nil {
				t.Fatal(err)
			}
			const sentinel = "provider-private-attachment-sentinel"
			switch kind {
			case "v1":
				ref.Protocol = "reflection-evidence/v1"
				ref.ToolCallID = sentinel
			case "missing-protocol":
				ref.Protocol = ""
				ref.ToolCallID = sentinel
			case "forged-current":
				ref.ToolCallID = sentinel
			}
			repository := memskill.New()
			partition := learning.SkillPartition{Principal: reflectionPrincipal(nil)}
			version, err := repository.CreateDraft(t.Context(), partition, "agent", learning.SkillBundle{Name: "inspect-source", Description: "Inspect source", Body: "Inspect before editing."}, learning.SkillProvenance{EvidenceRefs: []learning.EvidenceRef{ref}})
			if err != nil {
				t.Fatal(err)
			}
			version, err = repository.Reject(t.Context(), partition, "agent", version.ID, version.Version, version.Revision)
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{cfg: Config{Store: store, LearnedSkills: repository}}
			listed, err := svc.ListLearnedSkills(t.Context(), &mecatlv1.ListLearnedSkillsRequest{})
			if err != nil || len(listed.GetSkills()) != 1 {
				t.Fatalf("list: %+v %v", listed, err)
			}
			got, err := svc.GetLearnedSkill(t.Context(), &mecatlv1.GetLearnedSkillRequest{OwnerAgent: "agent", Id: string(version.ID), Version: string(version.Version)})
			if err != nil {
				t.Fatal(err)
			}
			for _, projected := range []*mecatlv1.LearnedSkillVersion{listed.Skills[0], got.Skill} {
				raw, err := json.Marshal(projected)
				if err != nil || strings.Contains(string(raw), sentinel) {
					t.Fatalf("unsafe attachment escaped: %s %v", raw, err)
				}
				if len(projected.Evidence) != 1 || len(projected.Receipts) == 0 {
					t.Fatalf("missing provenance: %+v", projected)
				}
				evidence := projected.Evidence[0]
				if kind == "current" {
					if !evidence.Available || evidence.AttachmentUnavailable || evidence.ToolCallId != string(call.ID) {
						t.Fatalf("current attachment lost: %+v", evidence)
					}
					for _, receipt := range projected.Receipts {
						if len(receipt.SourceToolCallIds) != 1 || receipt.SourceToolCallIds[0] != string(call.ID) {
							t.Fatalf("verified receipt attachment lost: %+v", receipt)
						}
					}
				} else {
					if evidence.Available || !evidence.AttachmentUnavailable || evidence.ToolCallId != "" || evidence.Availability == "" {
						t.Fatalf("unsupported attachment available: %+v", evidence)
					}
					for _, receipt := range projected.Receipts {
						if len(receipt.SourceToolCallIds) != 0 {
							t.Fatalf("unsafe receipt attachment: %+v", receipt)
						}
					}
				}
			}
		})
	}
}
