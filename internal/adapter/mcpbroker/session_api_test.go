package mcpbroker

import (
	"context"
	"sync/atomic"
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

func TestSessionAPIAnonymousEnrollmentAndDisconnect(t *testing.T) {
	var firstRequests, secondRequests atomic.Int32
	first := toolHiveDiscoveryServer(t, "first_tool", &firstRequests)
	second := toolHiveDiscoveryServer(t, "second_tool", &secondRequests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		Profiles: []ToolHiveProfile{
			{Name: "first", URL: first.URL, Auth: authNone},
			{Name: "second", URL: second.URL, Auth: authNone},
		},
		DeferAnonymousDiscovery: true,
	})
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
	if firstRequests.Load() != 0 || secondRequests.Load() != 0 || len(opened.Catalogue.Tools()) != 0 {
		t.Fatalf("OpenSession discovered tools: requests=(%d,%d), tools=%d", firstRequests.Load(), secondRequests.Load(), len(opened.Catalogue.Tools()))
	}
	begun, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || begun.Kind != c.EnrollmentCompletedKind || len(begun.Catalogue.Tools()) != 3 || find(begun.Catalogue, "CallMcpWithQuery") == nil {
		t.Fatalf("anonymous enrollment = %+v, %v", begun, err)
	}
	if firstRequests.Load() == 0 || secondRequests.Load() == 0 {
		t.Fatalf("enrollment skipped a required backend: requests=(%d,%d)", firstRequests.Load(), secondRequests.Load())
	}
	again, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || again.Kind != c.EnrollmentAlreadyConnected {
		t.Fatalf("repeat enrollment = %+v, %v", again, err)
	}
	connection := begun.Catalogue.Connection()
	result, err := api.DisconnectTools(owner, opened.Ref, connection)
	if err != nil || result != c.Disconnected {
		t.Fatalf("disconnect = %v, %v", result, err)
	}
	result, err = api.DisconnectTools(owner, opened.Ref, connection)
	if err != nil || result != c.AlreadyDisconnected {
		t.Fatalf("repeat disconnect = %v, %v", result, err)
	}
	reopened, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || len(reopened.Catalogue.Tools()) != 0 || reopened.Catalogue.Connection() != connection {
		t.Fatalf("reopen after disconnect = %+v, %v", reopened, err)
	}
	renewed, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || renewed.Kind != c.EnrollmentCompletedKind || renewed.Catalogue.Connection() == connection {
		t.Fatalf("new enrollment did not rotate connection: %+v, %v", renewed, err)
	}
	result, err = api.DisconnectTools(owner, opened.Ref, connection)
	if err != nil || result != c.ConnectionChanged {
		t.Fatalf("stale disconnect = %v, %v", result, err)
	}
	result, err = api.DisconnectTools(owner, opened.Ref, renewed.Catalogue.Connection())
	if err != nil || result != c.Disconnected {
		t.Fatalf("new disconnect = %v, %v", result, err)
	}
}

func TestSessionAPIStableLifetimeOutlivesNativeCustody(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	client := redis.NewClient(&redis.Options{MaxRetries: -1, Addr: f.mini.Addr()})
	defer client.Close()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	api, err := NewSessionAPI(f.process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := api.states[opened.Ref]
	guard := c.ContinuityGuard{SessionID: session.SessionID(opened.Ref), SessionIncarnation: st.record.Incarnation, OwnerPartition: st.record.Owner, WorkloadPartition: st.record.Workload, ProfileDigest: f.process.profileDigest, Providers: f.process.providers}
	staged, err := f.process.custody.Stage(owner, custodyRequest{Guard: custodyGuardFromContract(guard), AttemptDeadline: time.Now().Add(time.Minute)}, "verified-tsid")
	if err != nil {
		t.Fatal(err)
	}
	st.record.Custody = &c.StagedCredentialCustody{RecoveryReference: string(staged.Recovery), ExpiresAt: staged.ExpiresAt, ProfileDigest: guard.ProfileDigest, Providers: guard.Providers}
	st.record.Account = sessionAPIProofAccount(t, f)
	st.record.Connected = true
	st.record.Connection = apiRef()
	if err = saveAPIRecord(t, api, owner, st); err != nil {
		t.Fatal(err)
	}
	delete(api.states, opened.Ref)
	recovered, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || len(recovered.Catalogue.Tools()) != 2 || find(recovered.Catalogue, "CallMcpWithQuery") == nil {
		t.Fatalf("native custody recovery: %#v %v", recovered, err)
	}
	if !api.states[opened.Ref].record.Custody.ExpiresAt.Equal(staged.ExpiresAt) || !recovered.ExpiresAt.After(staged.ExpiresAt) {
		t.Fatal("recovery renewed custody or shortened session to custody")
	}
	later := staged.ExpiresAt.Add(time.Second)
	api.now = func() time.Time { return later }
	f.clock.Set(later)
	f.process.custody.clock = f.clock
	disconnected, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || disconnected.Ref != opened.Ref || len(disconnected.Catalogue.Tools()) != 0 {
		t.Fatalf("expired custody: %#v %v", disconnected, err)
	}
	started, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || started.Kind != c.EnrollmentStartedKind || !started.Started.Prompt.Valid() {
		t.Fatalf("explicit reenrollment: %#v %v", started, err)
	}
}

func TestSessionAPIAnonymousEnrollmentRequiresEveryBackend(t *testing.T) {
	var firstRequests, secondRequests atomic.Int32
	first := toolHiveDiscoveryServer(t, "first_tool", &firstRequests)
	second := toolHiveDiscoveryServer(t, "second_tool", &secondRequests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		Profiles: []ToolHiveProfile{
			{Name: "first", URL: first.URL, Auth: authNone},
			{Name: "second", URL: second.URL, Auth: authNone},
		},
		DeferAnonymousDiscovery: true,
	})
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
	second.Close()
	if outcome, err := api.BeginEnrollment(owner, opened.Ref); err == nil || outcome.Valid() {
		t.Fatalf("partial discovery was published: %+v, %v", outcome, err)
	}
	if firstRequests.Load() == 0 || secondRequests.Load() != 0 {
		t.Fatalf("unexpected discovery requests: first=%d second=%d", firstRequests.Load(), secondRequests.Load())
	}
	reopened, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || reopened.Catalogue.Ref() != opened.Catalogue.Ref() || len(reopened.Catalogue.Tools()) != 0 || reopened.Catalogue.Connection() != "" {
		t.Fatalf("failed enrollment changed catalogue: %+v, %v", reopened, err)
	}
}
