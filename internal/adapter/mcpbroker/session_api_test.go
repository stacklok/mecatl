package mcpbroker

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionAPIOpenDeleteLifecycle(t *testing.T) {
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()

	server := miniredis.RunT(t)
	storage := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer storage.Close()
	api, err := NewSessionAPI(process, storage, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "broker"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "alice"})
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Ref == "" || !opened.ExpiresAt.After(time.Now()) || opened.Catalogue == nil || !opened.Catalogue.Valid() || len(opened.Catalogue.Tools()) != 0 {
		t.Fatalf("new session = %+v", opened)
	}

	reopened, err := api.OpenSession(owner, &opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Ref != opened.Ref || reopened.ExpiresAt != opened.ExpiresAt || reopened.Catalogue.Ref() != opened.Catalogue.Ref() || len(reopened.Catalogue.Tools()) != 0 {
		t.Fatalf("saved open changed snapshot: opened=%+v reopened=%+v", opened, reopened)
	}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "mallory"})
	if _, err := api.OpenSession(foreign, &opened.Ref); err == nil {
		t.Fatal("saved open accepted a different owner")
	}

	deleted, err := api.DeleteSession(owner, opened.Ref)
	if err != nil || deleted != c.Deleted {
		t.Fatalf("delete = %v, %v; want deleted", deleted, err)
	}
	deleted, err = api.DeleteSession(owner, opened.Ref)
	if err != nil || deleted != c.AlreadyAbsent {
		t.Fatalf("repeat delete = %v, %v; want already absent", deleted, err)
	}
	if _, err := api.OpenSession(owner, &opened.Ref); err == nil {
		t.Fatal("deleted session was reopened")
	}
}
