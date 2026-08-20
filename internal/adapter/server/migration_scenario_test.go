package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func migrationContext(subject string) context.Context {
	return session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: subject, GrantType: session.GrantTypeUser})
}

func migrationService(t *testing.T, store port.SessionStore, authorize func(context.Context) bool, lease port.SessionLease) *server.Service {
	return migrationServiceWithUpdate(t, store, authorize, lease, nil)
}

func migrationServiceWithUpdate(t *testing.T, store port.SessionStore, authorize func(context.Context) bool, lease port.SessionLease, update func(server.StorageMaintenanceEvent)) *server.Service {
	t.Helper()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		StorageManagementAuthorized:         authorize,
		LocalStorageMaintenanceSingleWriter: lease == nil,
		StorageMaintenanceUpdate:            update,
		SessionLease:                        lease, LeaseOwner: "migration-server", LeaseTTL: time.Minute, LeaseRenewInterval: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func migrationStoreFixture(t *testing.T) (*jsonlstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	return store, dir
}

type blockingMigrationStore struct {
	*jsonlstore.Store
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (st *blockingMigrationStore) MigrateSessionFamily(ctx context.Context, family port.SessionMigrationFamily) (string, error) {
	st.once.Do(func() {
		close(st.entered)
		select {
		case <-st.release:
		case <-ctx.Done():
		}
	})
	return st.Store.MigrateSessionFamily(ctx, family)
}

type signalingMigrationStore struct {
	*jsonlstore.Store
	locking chan struct{}
	once    sync.Once
}

func (st *signalingMigrationStore) AcquireSessionMigrationJob(ctx context.Context, id string) (context.Context, func() error, error) {
	st.once.Do(func() { close(st.locking) })
	return st.Store.AcquireSessionMigrationJob(ctx, id)
}

func reopenMigrationStore(t *testing.T, dir string) *jsonlstore.Store {
	t.Helper()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore.New(reopen): %v", err)
	}
	return store
}

func runningMigrationJob(t *testing.T) (string, server.MigrationJob) {
	t.Helper()
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for _, id := range []string{"race-a", "race-b", "race-c"} {
		writeV1Family(t, dir, newLegacySession(t, id, owner), nil, nil, time.Unix(1700000000, 0))
	}
	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatalf("PlanSessionMigration: %v", err)
	}
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatalf("ApplySessionMigration: %v", err)
	}
	return dir, job
}

func writeV1Family(t *testing.T, dir string, sess *session.Session, tools, events []byte, modified time.Time) string {
	t.Helper()
	payload, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatalf("sessnap.Marshal: %v", err)
	}
	path := filepath.Join(dir, string(sess.ID)+".session.jsonl")
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		t.Fatalf("write v1 snapshot: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("stamp v1 snapshot: %v", err)
	}
	if tools != nil {
		if err := os.WriteFile(filepath.Join(dir, string(sess.ID)+".tools.jsonl"), tools, 0o600); err != nil {
			t.Fatalf("write tools: %v", err)
		}
	}
	if events != nil {
		if err := os.WriteFile(filepath.Join(dir, string(sess.ID)+".events.jsonl"), events, 0o600); err != nil {
			t.Fatalf("write events: %v", err)
		}
	}
	return path
}

func newLegacySession(t *testing.T, id string, owner *session.Principal) *session.Session {
	t.Helper()
	sess := session.New(session.SessionID(id), session.ModePlan, "/workspace", session.Limits{MaxTurns: 7}, time.Unix(1700000000, 0).UTC())
	if err := sess.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	return sess
}

func treeSnapshot(t *testing.T, dir string) map[string]struct {
	Size int64
	Mode os.FileMode
	Mod  int64
} {
	t.Helper()
	out := make(map[string]struct {
		Size int64
		Mode os.FileMode
		Mod  int64
	})
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		out[rel] = struct {
			Size int64
			Mode os.FileMode
			Mod  int64
		}{info.Size(), info.Mode(), info.ModTime().UnixNano()}
		return nil
	}); err != nil {
		t.Fatalf("walk store: %v", err)
	}
	return out
}

func canonicalStem(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	var b strings.Builder
	for _, r := range string(id) {
		if b.Len() >= 40 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	prefix := b.String()
	if prefix == "" {
		prefix = "id"
	}
	return "sid-v1-" + prefix + "-" + hex.EncodeToString(sum[:16])
}

func TestSessionStorageContinuity_Scenario4_MigrationDryRunIsReadOnly(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	legacyA := writeV1Family(t, dir, newLegacySession(t, "legacy-a", owner), []byte("tool\n"), []byte("event\n"), time.Unix(1700000100, 0))
	latest, err := os.ReadFile(legacyA)
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if f, openErr := os.OpenFile(legacyA, os.O_APPEND|os.O_WRONLY, 0); openErr != nil {
			t.Fatal(openErr)
		} else {
			if _, writeErr := f.Write(latest); writeErr != nil {
				_ = f.Close()
				t.Fatal(writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
	}
	writeV1Family(t, dir, newLegacySession(t, "legacy-b", owner), nil, nil, time.Unix(1700000200, 0))
	if err := store.Save(context.Background(), newLegacySession(t, "current-v2", owner)); err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sid-v1", "corrupt.session.json"), []byte("{torn-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "torn.session.jsonl"), []byte("{secret-token: /private/path"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, dir)

	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatalf("PlanSessionMigration: %v", err)
	}
	if !plan.Available || plan.V1Families != 3 || plan.V2Families != 2 || plan.InvalidFamilies != 2 || plan.SkippedFamilies != 0 || plan.CurrentBytes <= 0 || plan.ReclaimableBytes <= 0 || plan.TemporaryBytes <= 0 {
		t.Fatalf("dry-run plan = %+v", plan)
	}
	if after := treeSnapshot(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry-run mutated storage:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestSessionStorageContinuity_Scenario4_ResumableMigrationJob(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for _, id := range []string{"batch-a", "batch-b", "batch-c"} {
		writeV1Family(t, dir, newLegacySession(t, id, owner), nil, nil, time.Unix(1700000000, 0))
	}
	ctx := migrationContext("alice")
	var lifecycle1 []server.StorageMaintenanceEvent
	svc1 := migrationServiceWithUpdate(t, store, func(context.Context) bool { return true }, nil, func(event server.StorageMaintenanceEvent) {
		lifecycle1 = append(lifecycle1, event)
	})
	plan, err := svc1.PlanSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc1.ApplySessionMigration(ctx, plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Processed != 1 || job.Migrated != 1 || job.State != "running" {
		t.Fatalf("first batch = %+v", job)
	}
	if len(lifecycle1) == 0 || lifecycle1[len(lifecycle1)-1].State != server.StorageMaintenanceProgress || !lifecycle1[len(lifecycle1)-1].Resumable {
		t.Fatalf("running migration lifecycle = %+v", lifecycle1)
	}
	// A fresh Service over the same store proves the job registry, progress, and
	// caller binding survive process replacement.
	var lifecycle2 []server.StorageMaintenanceEvent
	svc2 := migrationServiceWithUpdate(t, store, func(context.Context) bool { return true }, nil, func(event server.StorageMaintenanceEvent) {
		lifecycle2 = append(lifecycle2, event)
	})
	job, err = svc2.SessionMigrationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for job.State != "completed" {
		job, err = svc2.ResumeSessionMigration(ctx, job.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.Processed != 3 || job.Migrated != 3 || job.Failed != 0 {
		t.Fatalf("completed job = %+v", job)
	}
	if len(lifecycle2) == 0 || lifecycle2[len(lifecycle2)-1].State != server.StorageMaintenanceCompleted {
		t.Fatalf("reattached completion lifecycle = %+v", lifecycle2)
	}
	again, err := svc2.ResumeSessionMigration(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, job) {
		t.Fatalf("completed resume changed job: got %+v want %+v", again, job)
	}
}

func TestSessionStorageContinuity_MigrationConcurrentResumesRejectStaleCheckpoint(t *testing.T) {
	dir, initial := runningMigrationJob(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstStore := &blockingMigrationStore{Store: reopenMigrationStore(t, dir), entered: entered, release: release}
	locking := make(chan struct{})
	secondStore := &signalingMigrationStore{Store: reopenMigrationStore(t, dir), locking: locking}
	first := migrationService(t, firstStore, func(context.Context) bool { return true }, nil)
	second := migrationService(t, secondStore, func(context.Context) bool { return true }, nil)
	ctx := migrationContext("alice")

	type result struct {
		job server.MigrationJob
		err error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		job, err := first.ResumeSessionMigration(ctx, initial.ID, 1)
		firstResult <- result{job: job, err: err}
	}()
	<-entered
	go func() {
		job, err := second.ResumeSessionMigration(ctx, initial.ID, 1)
		secondResult <- result{job: job, err: err}
	}()
	<-locking
	close(release)

	winner := <-firstResult
	loser := <-secondResult
	if winner.err != nil || winner.job.Processed != initial.Processed+1 {
		t.Fatalf("winning resume = %+v, err=%v", winner.job, winner.err)
	}
	if !errors.Is(loser.err, server.ErrMigrationConflict) {
		t.Fatalf("stale concurrent resume error = %v, want ErrMigrationConflict", loser.err)
	}
	persisted, err := first.SessionMigrationJob(ctx, initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Processed != winner.job.Processed || persisted.Migrated != winner.job.Migrated || persisted.State != winner.job.State {
		t.Fatalf("persisted progress regressed: got %+v winner %+v", persisted, winner.job)
	}
}

func TestSessionStorageContinuity_MigrationCancelWinsAfterConcurrentResume(t *testing.T) {
	dir, initial := runningMigrationJob(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	resumeStore := &blockingMigrationStore{Store: reopenMigrationStore(t, dir), entered: entered, release: release}
	locking := make(chan struct{})
	cancelStore := &signalingMigrationStore{Store: reopenMigrationStore(t, dir), locking: locking}
	resumeService := migrationService(t, resumeStore, func(context.Context) bool { return true }, nil)
	cancelService := migrationService(t, cancelStore, func(context.Context) bool { return true }, nil)
	ctx := migrationContext("alice")

	resumeResult := make(chan error, 1)
	cancelResult := make(chan struct {
		job server.MigrationJob
		err error
	}, 1)
	go func() {
		_, err := resumeService.ResumeSessionMigration(ctx, initial.ID, 1)
		resumeResult <- err
	}()
	<-entered
	go func() {
		job, err := cancelService.CancelSessionMigration(ctx, initial.ID)
		cancelResult <- struct {
			job server.MigrationJob
			err error
		}{job: job, err: err}
	}()
	<-locking
	close(release)

	if err := <-resumeResult; err != nil {
		t.Fatalf("concurrent resume: %v", err)
	}
	cancelled := <-cancelResult
	if cancelled.err != nil {
		t.Fatalf("concurrent cancel: %v", cancelled.err)
	}
	if cancelled.job.State != "cancelled" || cancelled.job.Processed != initial.Processed+1 {
		t.Fatalf("cancelled checkpoint = %+v", cancelled.job)
	}
	if _, err := resumeService.ResumeSessionMigration(ctx, initial.ID, 1); !errors.Is(err, server.ErrMigrationConflict) {
		t.Fatalf("resume after cancellation error = %v, want ErrMigrationConflict", err)
	}
	persisted, err := cancelService.SessionMigrationJob(ctx, initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, cancelled.job) {
		t.Fatalf("cancel was overwritten: got %+v want %+v", persisted, cancelled.job)
	}
}

func TestSessionStorageContinuity_MigrationRejectsStalePlanGeneration(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	writeV1Family(t, dir, newLegacySession(t, "stale-a", owner), nil, nil, time.Unix(1700000000, 0))
	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	ctx := migrationContext("alice")
	plan, err := svc.PlanSessionMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeV1Family(t, dir, newLegacySession(t, "stale-b", owner), nil, nil, time.Unix(1700000001, 0))
	if _, err := svc.ApplySessionMigration(ctx, plan.ID, 1); !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("stale generation apply error = %v, want concealed rejection", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "sid-v1", "migration-jobs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("stale generation minted job files: %v", entries)
	}
}

func TestSessionStorageContinuity_Scenario4_PerFamilyCrashSafety(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	sess := newLegacySession(t, "crash-safe", owner)
	v1 := writeV1Family(t, dir, sess, nil, nil, time.Unix(1700000300, 0))
	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	plan, _ := svc.PlanSessionMigration(migrationContext("alice"))
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Migrated != 1 {
		t.Fatalf("job = %+v", job)
	}
	if _, err := os.Stat(v1); !os.IsNotExist(err) {
		t.Fatalf("v1 remains after verified promotion: %v", err)
	}
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load verified v2: %v", err)
	}
	if loaded.ID != sess.ID {
		t.Fatalf("loaded id = %q", loaded.ID)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "sid-v1", canonicalStem(sess.ID)+".session.json.tmp-v1-*"))
	if len(matches) > 1 {
		t.Fatalf("peak replacement temps = %d", len(matches))
	}
}

func TestSessionStorageContinuity_Scenario4_MigrationPreservesSemantics(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", Name: "Alice", GrantType: session.GrantTypeUser}
	sess := newLegacySession(t, "semantic", owner)
	sess.Profile, sess.ProviderID, sess.ModelID = "no-fs", "provider", "model"
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPrompt("full snapshot", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("answer", "reasoning", nil)); err != nil {
		t.Fatal(err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatal(err)
	}
	want, _ := sessnap.Marshal(sess)
	mtime := time.Unix(1600000000, 123456000)
	writeV1Family(t, dir, sess, []byte("tool-sidecar\n"), []byte("event-sidecar\n"), mtime)
	corruptPath := filepath.Join(dir, "torn-semantic.session.jsonl")
	corrupt := []byte("{\"owner\":\"OPENROUTER_API_KEY=must-not-disappear\"")
	if err := os.WriteFile(corruptPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.InvalidFamilies != 1 {
		t.Fatalf("corrupt family was not reported: %+v", plan)
	}
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Migrated != 1 {
		t.Fatalf("job = %+v", job)
	}
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := sessnap.Marshal(loaded)
	if string(got) != string(want) {
		t.Fatalf("snapshot semantics changed\n got %s\nwant %s", got, want)
	}
	stem := canonicalStem(sess.ID)
	for suffix, wantSidecar := range map[string]string{".tools.jsonl": "tool-sidecar\n", ".events.jsonl": "event-sidecar\n"} {
		data, readErr := os.ReadFile(filepath.Join(dir, "sid-v1", stem+suffix))
		if readErr != nil || string(data) != wantSidecar {
			t.Fatalf("sidecar %s = %q, %v", suffix, data, readErr)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "sid-v1", stem+".session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mtime) {
		t.Fatalf("logical mtime = %s, want %s", info.ModTime(), mtime)
	}
	if loaded.Kind != session.SessionKindUnknown || loaded.Owner == nil || loaded.Owner.Subject != "alice" {
		t.Fatalf("labels changed: kind=%q owner=%+v", loaded.Kind, loaded.Owner)
	}
	gotCorrupt, err := os.ReadFile(corruptPath)
	if err != nil || !reflect.DeepEqual(gotCorrupt, corrupt) {
		t.Fatalf("reported corrupt family was discarded or rewritten: got %q, err=%v", gotCorrupt, err)
	}
}

func TestSessionStorageContinuity_Scenario4_MigrationRevalidatesUnderLease(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	leased := newLegacySession(t, "a-leased", owner)
	leasedV1 := writeV1Family(t, dir, leased, nil, nil, time.Unix(1700000000, 0))
	changed := newLegacySession(t, "z-changed", owner)
	writeV1Family(t, dir, changed, nil, nil, time.Unix(1700000000, 0))
	lease := memlease.New(wallclock.Clock{}, time.Minute)
	if _, err := lease.Acquire(context.Background(), leased.ID, "peer-server"); err != nil {
		t.Fatal(err)
	}
	svc := migrationService(t, store, func(context.Context) bool { return true }, lease)
	plan, _ := svc.PlanSessionMigration(migrationContext("alice"))
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Migrated != 0 || job.SkippedFamilies != 1 || job.Failed != 0 || job.State != "running" {
		t.Fatalf("leased batch = %+v", job)
	}
	if _, err := os.Stat(leasedV1); err != nil {
		t.Fatalf("leased family was changed: %v", err)
	}

	// A newer v2 save lands between batches. Resume must revalidate under the
	// exclusions and skip the stale v1 candidate without overwriting or removing it.
	changed.SetTitle("newest write")
	if err := store.Save(context.Background(), changed); err != nil {
		t.Fatalf("concurrent Save: %v", err)
	}
	job, err = svc.ResumeSessionMigration(migrationContext("alice"), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Migrated != 0 || job.SkippedFamilies != 2 || job.Failed != 0 {
		t.Fatalf("revalidated job = %+v", job)
	}
	loaded, err := store.Load(context.Background(), changed.ID)
	if err != nil || loaded.Title != "newest write" {
		t.Fatalf("newest write lost: title=%q err=%v", loaded.Title, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sid-v1", canonicalStem(changed.ID)+".session.jsonl")); err != nil {
		t.Fatalf("changed v1 was discarded: %v", err)
	}
}

func TestSessionStorageContinuity_Scenario4_MigrationAuthorizationAndNoOracle(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	owner := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	writeV1Family(t, dir, newLegacySession(t, "private-family", owner), nil, nil, time.Unix(1700000000, 0))
	svc := migrationService(t, store, func(ctx context.Context) bool {
		p := session.PrincipalFromContext(ctx)
		return p != nil && p.Subject != "mallory"
	}, nil)
	alicePrincipal := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	grpcClient, closeClient := adoptionGRPCClient(t, svc, alicePrincipal)
	defer closeClient()
	wirePlan, err := grpcClient.PlanSessionMigration(context.Background(), &mecatlv1.PlanSessionMigrationRequest{})
	if err != nil || wirePlan.GetV1Families() != 1 {
		t.Fatalf("gRPC migration plan = %+v, %v", wirePlan, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/migrations/plan", nil).WithContext(migrationContext("alice"))
	rec := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "private-family") {
		t.Fatalf("HTTP migration plan status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := svc.PlanSessionMigration(migrationContext("mallory")); !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("unauthorized plan error = %v", err)
	}
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"apply foreign plan":  func() error { _, err := svc.ApplySessionMigration(migrationContext("bob"), plan.ID, 1); return err },
		"resume foreign job":  func() error { _, err := svc.ResumeSessionMigration(migrationContext("bob"), job.ID, 1); return err },
		"cancel foreign job":  func() error { _, err := svc.CancelSessionMigration(migrationContext("bob"), job.ID); return err },
		"inspect foreign job": func() error { _, err := svc.SessionMigrationJob(migrationContext("bob"), job.ID); return err },
		"inspect missing job": func() error {
			_, err := svc.SessionMigrationJob(migrationContext("bob"), "00000000000000000000000000000000")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			crossErr := call()
			if !errors.Is(crossErr, server.ErrManagementUnauthorized) || strings.Contains(crossErr.Error(), job.ID) {
				t.Fatalf("cross-caller request leaked oracle: %v", crossErr)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario4_MaintenanceErrorsAreSanitized(t *testing.T) {
	store, dir := migrationStoreFixture(t)
	secret := "OPENROUTER_API_KEY=super-secret"
	if err := os.WriteFile(filepath.Join(dir, "corrupt.session.jsonl"), []byte("/private/backend/path "+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := migrationService(t, store, func(context.Context) bool { return true }, nil)
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.InvalidFamilies != 1 {
		t.Fatalf("invalid count = %d", plan.InvalidFamilies)
	}
	projection := strings.ToLower(plan.ID + plan.UnavailableReason)
	for _, forbidden := range []string{"private", "openrouter", "super-secret", "corrupt.session", dir} {
		if strings.Contains(projection, strings.ToLower(forbidden)) {
			t.Fatalf("plan leaked %q: %+v", forbidden, plan)
		}
	}
	// Force a real adapter failure after planning: the existing canonical tool
	// sidecar makes legacy promotion fail closed. The raw backend error contains
	// physical names, but every durable and wire projection must use only the
	// closed reason code and stable message.
	valid := newLegacySession(t, "private-session-id", &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	writeV1Family(t, dir, valid, []byte(secret+" /private/backend/path\n"), nil, time.Unix(1700000000, 0))
	if err := os.MkdirAll(filepath.Join(dir, "sid-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sid-v1", canonicalStem(valid.ID)+".tools.jsonl"), []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.ApplySessionMigration(migrationContext("alice"), plan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Failed != 2 || len(job.Errors) != 1 || job.Errors[0].ReasonCode != "backend_failure" ||
		job.Errors[0].Message != "storage maintenance could not process this item" {
		t.Fatalf("sanitized item failure = %+v", job)
	}
	wire := strings.ToLower(job.Errors[0].ItemHandle + job.Errors[0].ReasonCode + job.Errors[0].Message)
	for _, forbidden := range []string{"private-session-id", "private/backend", "openrouter", "super-secret", dir} {
		if strings.Contains(wire, strings.ToLower(forbidden)) {
			t.Fatalf("job projection leaked %q: %+v", forbidden, job)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "sid-v1", "migration-jobs", job.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-session-id", secret, dir} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("durable job leaked %q: %s", forbidden, data)
		}
	}
}

func TestSessionStorageContinuity_Scenario4_UnsupportedBackendIsHonest(t *testing.T) {
	svc := migrationService(t, memstore.New(), func(context.Context) bool { return true }, nil)
	plan, err := svc.PlanSessionMigration(migrationContext("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Available || plan.UnavailableReason != "backend_unsupported" {
		t.Fatalf("unsupported plan = %+v", plan)
	}
	if _, err := svc.ApplySessionMigration(migrationContext("alice"), "plan", 1); !errors.Is(err, server.ErrMigrationUnsupported) {
		t.Fatalf("apply error = %v", err)
	}
}
