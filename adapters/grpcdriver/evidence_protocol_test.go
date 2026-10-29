package grpcdriver

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestMaterializedEvidenceSurvivesRepositoryDrivers(t *testing.T) {
	source := session.New("source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	if err := source.SeedHistory([]session.Message{session.NewUserMessage("omit older context"), session.NewUserMessage("remember focused verification"), session.NewUserMessage("remember stable evidence coordinates")}); err != nil {
		t.Fatal(err)
	}
	trajectory := learning.NewSessionTrajectory(source, "", session.StopEndTurn, session.Usage{})
	material, err := learning.MaterializeEvidence(t.Context(), learning.MaterializationRequest{Trajectory: trajectory, Explicit: true, Limits: learning.MaterializationLimits{MaxMessages: 2}})
	if err != nil || material.Disposition != learning.MaterializationSelected {
		t.Fatalf("materialize: %+v %v", material, err)
	}
	ref, err := learning.MessageEvidenceRef(material.Input, 1, "")
	if err != nil || ref.ManifestIndex == 0 || ref.OriginalMessage == nil || *ref.OriginalMessage != 2 {
		t.Fatalf("selected citation: %+v %v", ref, err)
	}
	verified, err := learning.RematerializeEvidence(trajectory, nil, material.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(material.Input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/evidence", Value: "stable coordinates", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterProposalRepositoryServiceServer(server, NewProposalRepositoryServer(memproposal.New()))
		driverv1.RegisterSkillRepositoryServiceServer(server, NewSkillRepositoryServer(memskill.New()))
	})
	proposals := NewProposalRepository(conn)
	partition := learning.ProposalPartition{Principal: "principal"}
	staged, err := proposals.StageBatch(t.Context(), partition, material.Manifest.Digest, []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposal, found, err := proposals.Get(t.Context(), partition, staged[0].ID)
	if err != nil || !found {
		t.Fatalf("remote proposal: %+v %v", proposal, err)
	}
	skills := NewSkillRepository(conn)
	skillPartition := learning.SkillPartition{Principal: partition.Principal}
	draft, err := skills.CreateDraft(t.Context(), skillPartition, "agent", learning.SkillBundle{Name: "inspect-evidence", Description: "Inspect evidence", Body: "Preserve evidence coordinates."}, learning.SkillProvenance{EvidenceRefs: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	skill, found, err := skills.Get(t.Context(), skillPartition, "agent", draft.ID, draft.Version)
	if err != nil || !found {
		t.Fatalf("remote skill: %+v %v", skill, err)
	}
	for _, got := range []learning.EvidenceRef{proposal.Candidate.Evidence[0], skill.Provenance.EvidenceRefs[0]} {
		if !reflect.DeepEqual(got, ref) {
			t.Fatalf("remote citation changed: %+v != %+v", got, ref)
		}
		if _, err := learning.CanonicalEvidenceCitation(verified, got); err != nil {
			t.Fatalf("remote citation no longer verifies: %v", err)
		}
		got.ManifestIndex = 0
		if _, err := learning.CanonicalEvidenceCitation(verified, got); err == nil {
			t.Fatal("tampered manifest index accepted")
		}
	}
}

func TestEvidenceManifestIndexBounds(t *testing.T) {
	const bound = learning.MaxInputMessages + learning.MaxInputEvents
	for _, index := range []int{-1, bound, 1 << 40} {
		ref := learning.EvidenceRef{Protocol: learning.ReflectionEvidenceV2, ManifestIndex: index}
		if proposalEvidenceToProto(ref).GetManifestIndex() < bound || skillEvidenceToProto(ref).GetManifestIndex() < bound {
			t.Fatalf("invalid outgoing index wrapped into range: %d", index)
		}
	}
	if proposalEvidenceFromProto(&driverv1.ProposalEvidence{Protocol: string(learning.ReflectionEvidenceV2), ManifestIndex: bound}).Protocol != "" || skillEvidenceFromProto(&driverv1.LearnedSkillEvidenceRef{Protocol: string(learning.ReflectionEvidenceV2), ManifestIndex: bound}).Protocol != "" {
		t.Fatal("out-of-range incoming index accepted")
	}
}

func TestEvidenceProtocolsAndCoordinatesRoundTrip(t *testing.T) {
	for _, protocol := range []learning.EvidenceProtocol{learning.ReflectionEvidenceV2, ""} {
		ref := learning.EvidenceRef{Protocol: protocol, SessionID: "source", Locator: learning.EvidenceMessage, Ordinal: 2, ManifestIndex: 3, OriginalMessage: new(0), EventSeq: new(int64(7)), ToolCallID: "canonical-call", Digest: strings.Repeat("a", 64), AggregateDigest: strings.Repeat("b", 64)}
		proposal := proposalEvidenceToProto(ref)
		raw, err := proto.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		var decodedProposal driverv1.ProposalEvidence
		if err := proto.Unmarshal(raw, &decodedProposal); err != nil {
			t.Fatal(err)
		}
		if got := proposalEvidenceFromProto(&decodedProposal); !reflect.DeepEqual(got, ref) {
			t.Fatalf("proposal evidence changed: %+v", got)
		}
		skill := skillEvidenceToProto(ref)
		raw, err = proto.Marshal(skill)
		if err != nil {
			t.Fatal(err)
		}
		var decodedSkill driverv1.LearnedSkillEvidenceRef
		if err := proto.Unmarshal(raw, &decodedSkill); err != nil {
			t.Fatal(err)
		}
		if got := skillEvidenceFromProto(&decodedSkill); !reflect.DeepEqual(got, ref) {
			t.Fatalf("skill evidence changed: %+v", got)
		}
	}
}
