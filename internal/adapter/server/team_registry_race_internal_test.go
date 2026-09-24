package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/team"
)

func TestCleanupBetweenTeamLookupAndClaimPreventsStartup(t *testing.T) {
	const id = "team-race"
	builds := 0
	tm := team.New("race")
	ts := &teamState{team: tm, phase: teamCreated}
	svc := &Service{
		cfg: Config{MemberEngine: func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
			builds++
			return agent.MemberBuild{}
		}},
		teams: map[string]*teamState{id: ts},
	}

	captured, err := svc.lookupTeam(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CleanupTeam(context.Background(), id); err != nil {
		t.Fatalf("CleanupTeam between lookup and claim: %v", err)
	}
	if _, err := svc.claimTeamStart(context.Background(), id, captured); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("claim after cleanup = %v, want ErrTeamNotFound", err)
	}
	if builds != 0 {
		t.Fatalf("member factory calls = %d, want 0", builds)
	}
	if _, ok := svc.teams[id]; ok {
		t.Fatal("cleanup did not remove team registry entry")
	}
}
