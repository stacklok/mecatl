package server_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0280_ArtifactHandlesCannotReplayAsPlacementSelectors(t *testing.T) {
	t.Parallel()
	if reflect.TypeOf(agent.ArtifactHandle("")) == reflect.TypeOf(server.PlacementSelector{}) {
		t.Fatal("artifact handles and placement selectors share a type")
	}
	issuer, err := server.NewWorktreeSelectorIssuer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	artifact := agent.ArtifactHandle("artifact-parallel-call-0")
	_, err = issuer.Match(string(artifact), &session.Principal{Issuer: "test", Subject: "owner"}, "source", []server.Worktree{{Path: "/private", Branch: "main", Head: "abc"}})
	if !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("artifact replay as selector = %v, want hidden not-found", err)
	}
}
