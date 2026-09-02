package server_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestRedisMetadataIndexAdoptionThroughAuthenticatedMaintenanceJob(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for i, id := range []session.SessionID{"legacy-a", "legacy-b", "legacy-c"} {
		sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
		if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		blob, err := sessnap.Marshal(sess)
		if err != nil {
			t.Fatal(err)
		}
		mr.HSet("mecatl:session:"+string(id), "blob", string(blob), "mtime", strconv.FormatInt(int64(i+1), 10))
	}
	mr.HSet("mecatl:session:corrupt-private-id", "blob", "OPENROUTER_API_KEY=do-not-project", "mtime", "4")

	store, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authorized := func(ctx context.Context) bool {
		principal := session.PrincipalFromContext(ctx)
		return principal != nil && principal.Subject == "alice"
	}
	svc := migrationService(t, store, authorized, nil)
	ctx := migrationContext("alice")

	if _, err := svc.PlanSessionMigration(migrationContext("mallory")); !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("unauthorized plan error = %v", err)
	}
	plan, err := svc.PlanSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Available || plan.V1Families != 3 || plan.InvalidFamilies != 1 {
		t.Fatalf("redis adoption plan = %+v", plan)
	}
	job, err := svc.ApplySessionMigration(ctx, plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" || job.Processed != 1 || job.Migrated != 1 {
		t.Fatalf("partial adoption = %+v", job)
	}
	if _, err := store.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10}); !errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
		t.Fatalf("partial index became visible: %v", err)
	}

	// Current Save and Delete race safely with the rebuild: Save publishes its row
	// atomically, while Delete removes a legacy candidate and advances generation.
	current := session.New("current-save", session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
	if err := current.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "legacy-b"); err != nil {
		t.Fatal(err)
	}

	// A replacement process reloads the durable caller-bound job and resumes it.
	store2, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	svc2 := migrationService(t, store2, authorized, nil)
	persisted, err := svc2.SessionMigrationJob(ctx, job.ID)
	if err != nil || persisted.Processed != job.Processed {
		t.Fatalf("reloaded job = %+v, %v", persisted, err)
	}
	job, err = svc2.ResumeSessionMigration(ctx, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "completed" || job.Failed != 1 {
		t.Fatalf("corrupt legacy row was not reported as completed-with-failures: %+v", job)
	}
	if _, err := store2.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10}); !errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
		t.Fatalf("corrupt coverage published ready: %v", err)
	}

	// Removing the corrupt legacy record is itself generation-tracked. A fresh
	// plan/job then re-verifies complete coverage and atomically publishes; the
	// completed-with-failures job remains immutable and honest.
	if err := store2.Delete(ctx, "corrupt-private-id"); err != nil {
		t.Fatal(err)
	}
	plan, err = svc2.PlanSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err = svc2.ApplySessionMigration(ctx, plan.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "completed" {
		t.Fatalf("completed adoption = %+v", job)
	}
	page, err := store2.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10, OwnershipEnforced: true, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 3 || len(page.Sessions) != 3 {
		t.Fatalf("post-adoption page = %+v", page)
	}
	deleted, err := store2.DeleteSessionIfUnchanged(ctx, page.Sessions[0])
	if err != nil || !deleted {
		t.Fatalf("post-adoption retention delete = %v, %v", deleted, err)
	}
	after, err := store2.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10, OwnershipEnforced: true, Owner: owner})
	if err != nil || after.TotalCount != 2 {
		t.Fatalf("post-retention page = %+v, %v", after, err)
	}
}

func TestRedisMetadataIndexAdoptionCancellationIsDurableAndMonotonic(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	for i, id := range []session.SessionID{"cancel-a", "cancel-b"} {
		sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		blob, err := sessnap.Marshal(sess)
		if err != nil {
			t.Fatal(err)
		}
		mr.HSet("mecatl:session:"+string(id), "blob", string(blob), "mtime", strconv.Itoa(i+1))
	}
	store, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authorized := func(context.Context) bool { return true }
	svc := migrationService(t, store, authorized, nil)
	ctx := migrationContext("alice")
	plan, err := svc.PlanSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.ApplySessionMigration(ctx, plan.ID, 1)
	if err != nil || job.State != "running" {
		t.Fatalf("partial job = %+v, %v", job, err)
	}
	cancelled, err := svc.CancelSessionMigration(ctx, job.ID)
	if err != nil || cancelled.State != "cancelled" || cancelled.Processed != 1 {
		t.Fatalf("cancelled job = %+v, %v", cancelled, err)
	}
	store2, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	svc2 := migrationService(t, store2, authorized, nil)
	persisted, err := svc2.SessionMigrationJob(ctx, job.ID)
	if err != nil || persisted.State != "cancelled" || persisted.Processed != 1 {
		t.Fatalf("persisted cancellation = %+v, %v", persisted, err)
	}
	if _, err := svc2.ResumeSessionMigration(ctx, job.ID, 10); !errors.Is(err, server.ErrMigrationConflict) {
		t.Fatalf("resume cancelled job error = %v", err)
	}
	if _, err := store2.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10}); !errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
		t.Fatalf("cancelled partial job exposed index: %v", err)
	}
}
