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
		return &mecatlv1.ListLearnedSkillsResponse{Skills: rows, NextCursor: "next", Project: in.GetProject(), Generation: 9}, nil
	}
	return &mecatlv1.ListLearnedSkillsResponse{Skills: []*mecatlv1.LearnedSkillVersion{{Id: in.GetProject() + "last"}}, Project: in.GetProject(), Generation: 9}, nil
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
	skills, generation, err := cl.ListLearnedSkillsPage(context.Background(), "/project")
	if err != nil || len(skills) != 102 || generation != 9 {
		t.Fatalf("skills=%d generation=%d err=%v", len(skills), generation, err)
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
