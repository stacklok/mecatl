package app

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestRedisWorkspaceSkipsLocalPathEscapePolicy(t *testing.T) {
	inner := permpolicy.NewPolicy(nil, nil)
	if got := escapePolicyForConfig(Config{RedisFilesystem: true}, nil, nil, inner); got != inner {
		t.Fatal("Redis workspace was wrapped in the local-filesystem escape policy")
	}
}

func TestRedisPlacementScopesWorkspaceByPrincipal(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider := &redisPlacementProvider{store: st}
	ctx := context.Background()
	alice := &session.Principal{Issuer: "https://issuer-a", Subject: "same", GrantType: session.GrantTypeUser}
	otherIssuer := &session.Principal{Issuer: "https://issuer-b", Subject: "same", GrantType: session.GrantTypeUser}

	bind := func(principal *session.Principal) server.PlacementBinding {
		binding, bindErr := provider.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: principal, Scope: "test", Operation: server.PlacementOperationCreate})
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		return binding
	}
	first := bind(alice)
	second := bind(alice)
	isolated := bind(otherIssuer)
	if first.Ref != second.Ref || first.Ref == isolated.Ref {
		t.Fatalf("principal refs: first=%+v second=%+v isolated=%+v", first.Ref, second.Ref, isolated.Ref)
	}
	if _, err := first.Environment.Workspace().CreateFile(ctx, "private.txt", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Environment.Workspace().Read(ctx, "private.txt"); err != nil || string(got) != "secret" {
		t.Fatalf("same-principal read = %q, %v", got, err)
	}
	if _, err := isolated.Environment.Workspace().Read(ctx, "private.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-principal read = %v, want not exist", err)
	}

	anonymousA, anonymousB := bind(nil), bind(nil)
	if anonymousA.Ref != anonymousB.Ref || anonymousA.Ref.ID != anonymousWorkspaceScope {
		t.Fatalf("anonymous refs = %+v, %+v", anonymousA.Ref, anonymousB.Ref)
	}
}

func TestRedisPlacementReattachRejectsDifferentPrincipal(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider := &redisPlacementProvider{store: st}
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: owner, Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: binding.Ref, Principal: &session.Principal{Issuer: "issuer", Subject: "attacker", GrantType: session.GrantTypeUser}, Scope: "test"})
	if !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("reattach error = %v, want hidden not found", err)
	}
}
