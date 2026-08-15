package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type pagingSkillsClient struct {
	mecatlv1.HarnessServiceClient
	skillCalls  map[string]int
	changeCalls map[string]int
}

func (f *pagingSkillsClient) ListLearnedSkills(_ context.Context, in *mecatlv1.ListLearnedSkillsRequest, _ ...grpc.CallOption) (*mecatlv1.ListLearnedSkillsResponse, error) {
	if f.skillCalls == nil {
		f.skillCalls = map[string]int{}
	}
	f.skillCalls[in.GetProject()]++
	if in.GetCursor() == "" {
		rows := make([]*mecatlv1.LearnedSkillVersion, 50)
		for i := range rows {
			rows[i] = &mecatlv1.LearnedSkillVersion{Id: in.GetProject() + "first"}
		}
		generation := uint64(4)
		if in.GetProject() != "" {
			generation = 5
		}
		return &mecatlv1.ListLearnedSkillsResponse{Skills: rows, NextCursor: "next", Project: in.GetProject(), Generation: generation}, nil
	}
	generation := uint64(4)
	if in.GetProject() != "" {
		generation = 5
	}
	return &mecatlv1.ListLearnedSkillsResponse{Skills: []*mecatlv1.LearnedSkillVersion{{Id: in.GetProject() + "last"}}, Project: in.GetProject(), Generation: generation}, nil
}

func (f *pagingSkillsClient) ListSkillChanges(_ context.Context, in *mecatlv1.ListSkillChangesRequest, _ ...grpc.CallOption) (*mecatlv1.ListSkillChangesResponse, error) {
	if f.changeCalls == nil {
		f.changeCalls = map[string]int{}
	}
	f.changeCalls[in.GetProject()]++
	if in.GetCursor() == "" {
		return &mecatlv1.ListSkillChangesResponse{Changes: []*mecatlv1.SkillChangeReceipt{{Id: "first"}}, NextCursor: "next"}, nil
	}
	return &mecatlv1.ListSkillChangesResponse{Changes: []*mecatlv1.SkillChangeReceipt{{Id: "last"}}}, nil
}

func TestLearnedSkillsAndChangesExhaustGlobalAndProjectPages(t *testing.T) {
	fake := &pagingSkillsClient{}
	cl := newFakeClient(fake)
	skills, generations, err := cl.ListLearnedSkillsPage(context.Background(), "/project")
	if err != nil || len(skills) != 102 || generations[""] != 4 || generations["/project"] != 5 {
		t.Fatalf("skills=%d generations=%v err=%v", len(skills), generations, err)
	}
	changes, err := cl.ListSkillChanges(context.Background(), "/project")
	if err != nil || len(changes) != 4 {
		t.Fatalf("changes=%d err=%v", len(changes), err)
	}
	for _, partition := range []string{"", "/project"} {
		if fake.skillCalls[partition] != 2 || fake.changeCalls[partition] != 2 {
			t.Fatalf("partition %q calls skills=%d changes=%d", partition, fake.skillCalls[partition], fake.changeCalls[partition])
		}
	}
}

type rollbackSkillsClient struct {
	mecatlv1.HarnessServiceClient
	lastReq *mecatlv1.RollbackLearnedSkillRequest
}

func (f *rollbackSkillsClient) RollbackLearnedSkill(_ context.Context, in *mecatlv1.RollbackLearnedSkillRequest, _ ...grpc.CallOption) (*mecatlv1.MutateLearnedSkillResponse, error) {
	f.lastReq = in
	return &mecatlv1.MutateLearnedSkillResponse{
		Skill:             &mecatlv1.LearnedSkillVersion{Id: "skill-1", OwnerAgent: "agent", Version: "v1", Revision: "r3", State: "active"},
		Project:           "/project",
		Generation:        8,
		PublicationStatus: "published",
	}, nil
}

func TestRollbackLearnedSkillCmdCarriesSourceAndTarget(t *testing.T) {
	fake := &rollbackSkillsClient{}
	cl := newFakeClient(fake)
	source := LearnedSkill{Project: "/project", ID: "skill-1", OwnerAgent: "agent", Version: "v2", Revision: "r2", Supersedes: "v1"}

	msg, ok := RollbackLearnedSkillCmd(context.Background(), cl, source, 17)().(LearnedSkillMsg)
	if !ok {
		t.Fatal("rollback command returned an unexpected message type")
	}
	if fake.lastReq == nil || fake.lastReq.GetProject() != source.Project || fake.lastReq.GetId() != source.ID || fake.lastReq.GetOwnerAgent() != source.OwnerAgent || fake.lastReq.GetTargetVersion() != "v1" || fake.lastReq.GetExpectedRevision() != "r2" {
		t.Fatalf("rollback request = %#v", fake.lastReq)
	}
	if msg.Err != nil || msg.Action != "rollback" || msg.RequestID != 17 || msg.SelectedSkillID != "skill-1" || msg.SelectedOwnerAgent != "agent" || msg.SelectedVersion != "v2" || msg.ExpectedRevision != "r2" || msg.TargetVersion != "v1" {
		t.Fatalf("rollback message correlation = %#v", msg)
	}
	if msg.Skill == nil || msg.Skill.Version != "v1" || msg.Skill.Revision != "r3" || msg.Generation != 8 || msg.PublicationStatus != "published" {
		t.Fatalf("rollback result = %#v", msg)
	}
}

// fakeSkillsClient is a scripted HarnessServiceClient for the ListSkills wrapper
// tests. It embeds the interface and overrides only the one RPC under test, so
// the proto→plain mapping runs offline.
type fakeSkillsClient struct {
	mecatlv1.HarnessServiceClient

	resp *mecatlv1.ListSkillsResponse
	err  error

	lastReq *mecatlv1.ListSkillsRequest
}

func (f *fakeSkillsClient) ListSkills(_ context.Context, in *mecatlv1.ListSkillsRequest, _ ...grpc.CallOption) (*mecatlv1.ListSkillsResponse, error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestListSkillsMapping(t *testing.T) {
	fake := &fakeSkillsClient{resp: &mecatlv1.ListSkillsResponse{Skills: []*mecatlv1.SkillInfo{
		{Name: "code-review", Description: "review a diff"},
		{Name: "deep-research", Description: "fan-out web research"},
	}}}
	cl := newFakeClient(fake)

	skills, err := cl.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if fake.lastReq == nil {
		t.Fatal("request not sent")
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}
	if skills[0].Name != "code-review" || skills[0].Description != "review a diff" {
		t.Fatalf("skills[0] = %+v", skills[0])
	}
	if skills[1].Name != "deep-research" {
		t.Fatalf("skills[1] = %+v", skills[1])
	}
}

func TestListSkillsMappingNilSafe(t *testing.T) {
	fake := &fakeSkillsClient{resp: &mecatlv1.ListSkillsResponse{}}
	cl := newFakeClient(fake)

	skills, err := cl.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("skills = %d, want 0 (empty response)", len(skills))
	}
}

func TestListSkillsCmdSuccess(t *testing.T) {
	fake := &fakeSkillsClient{resp: &mecatlv1.ListSkillsResponse{Skills: []*mecatlv1.SkillInfo{
		{Name: "code-review", Description: "review a diff"},
	}}}
	cl := newFakeClient(fake)

	msg := ListSkillsCmd(context.Background(), cl)()
	sm, ok := msg.(SkillsMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SkillsMsg", msg)
	}
	if sm.Err != nil {
		t.Fatalf("unexpected err: %v", sm.Err)
	}
	if len(sm.Skills) != 1 || sm.Skills[0].Name != "code-review" {
		t.Fatalf("skills = %+v", sm.Skills)
	}
}

func TestListSkillsCmdError(t *testing.T) {
	fake := &fakeSkillsClient{err: errors.New("boom")}
	cl := newFakeClient(fake)

	msg := ListSkillsCmd(context.Background(), cl)()
	sm, ok := msg.(SkillsMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SkillsMsg", msg)
	}
	if sm.Err == nil {
		t.Fatalf("expected an error in SkillsMsg")
	}
}
