package server_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

func TestCallerSeparation_Scenario4_SystemPrincipalIsNotUniversalBypass(t *testing.T) {
	svc, _, _, alice, _ := callerSeparationFixture(t)
	owned, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	system := syscaller.Context(context.Background(), syscaller.RootScheduler)
	if _, err := svc.GetSession(system, owned.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("scheduler system principal read caller-owned session: %v, want ErrNotFound", err)
	}
}
