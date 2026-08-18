package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	defaultMigrationBatch = 25
	maxMigrationBatch     = 100
	maxMigrationErrors    = 256
)

// MigrationPlan is the content-free, authenticated projection of a read-only
// v1-to-v2 storage inspection.
type MigrationPlan struct {
	ID                string
	Available         bool
	UnavailableReason string
	V1Families        int64
	V2Families        int64
	InvalidFamilies   int64
	SkippedFamilies   int64
	CurrentBytes      int64
	ReclaimableBytes  int64
	TemporaryBytes    int64
}

// MigrationItemError is a bounded stable maintenance failure. ItemHandle is a
// non-reversible display handle, never a session id or backend path.
type MigrationItemError struct {
	ItemHandle string
	ReasonCode string
	Message    string
}

// MigrationJob is the caller-safe durable job projection.
type MigrationJob struct {
	ID               string
	State            string
	V1Families       int64
	V2Families       int64
	InvalidFamilies  int64
	SkippedFamilies  int64
	CurrentBytes     int64
	ReclaimableBytes int64
	TemporaryBytes   int64
	Processed        int64
	Migrated         int64
	Failed           int64
	Errors           []MigrationItemError
}

func migrationStore(store port.SessionStore) (port.SessionMigrationStore, bool) {
	backend, ok := store.(port.SessionMigrationStore)
	return backend, ok
}

func (s *Service) authorizeMigration(ctx context.Context) (port.SessionMigrationStore, string, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return nil, "", ErrManagementUnauthorized
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, "", ErrManagementUnauthorized
	}
	backend, ok := migrationStore(s.cfg.Store)
	if !ok {
		return nil, migrationPrincipalKey(principal), ErrMigrationUnsupported
	}
	return backend, migrationPrincipalKey(principal), nil
}

func migrationPrincipalKey(principal *session.Principal) string {
	sum := sha256.Sum256([]byte(principal.Issuer + "\x00" + principal.Subject))
	return hex.EncodeToString(sum[:16])
}

func newMigrationHandle() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// PlanSessionMigration performs no writes. The opaque plan handle binds the
// authenticated principal to the inspected storage generation; apply turns it
// into a separate durable job handle.
func (s *Service) PlanSessionMigration(ctx context.Context) (MigrationPlan, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) || session.PrincipalFromContext(ctx) == nil {
		return MigrationPlan{}, ErrManagementUnauthorized
	}
	backend, ok := migrationStore(s.cfg.Store)
	if !ok {
		return MigrationPlan{UnavailableReason: "backend_unsupported"}, nil
	}
	inspection, err := backend.InspectSessionMigration(ctx)
	if err != nil {
		return MigrationPlan{}, sanitizedMigrationBackendError()
	}
	principalKey := migrationPrincipalKey(session.PrincipalFromContext(ctx))
	job := port.SessionMigrationJob{
		ID:         principalKey + "." + inspection.Generation,
		V1Families: inspection.V1Families, V2Families: inspection.V2Families,
		InvalidFamilies: inspection.InvalidFamilies, SkippedFamilies: inspection.SkippedFamilies,
		CurrentBytes: inspection.CurrentBytes, ReclaimableBytes: inspection.ReclaimableBytes,
		TemporaryBytes: inspection.TemporaryBytes,
	}
	return migrationPlanProjection(job, inspection.Available, inspection.UnavailableReason), nil
}

func migrationPlanProjection(job port.SessionMigrationJob, available bool, unavailable string) MigrationPlan {
	return MigrationPlan{
		ID: job.ID, Available: available, UnavailableReason: unavailable,
		V1Families: job.V1Families, V2Families: job.V2Families, InvalidFamilies: job.InvalidFamilies,
		SkippedFamilies: job.SkippedFamilies, CurrentBytes: job.CurrentBytes,
		ReclaimableBytes: job.ReclaimableBytes, TemporaryBytes: job.TemporaryBytes,
	}
}

// ApplySessionMigration creates a durable caller-bound job and processes one bounded batch.
func (s *Service) ApplySessionMigration(ctx context.Context, planID string, batchSize int) (MigrationJob, error) {
	return s.driveSessionMigration(ctx, planID, batchSize, true)
}

// ResumeSessionMigration processes the next bounded batch of a durable job.
func (s *Service) ResumeSessionMigration(ctx context.Context, jobID string, batchSize int) (MigrationJob, error) {
	return s.driveSessionMigration(ctx, jobID, batchSize, false)
}

//nolint:gocyclo // explicit durable-state orchestration keeps each failure checkpoint visible.
func (s *Service) driveSessionMigration(ctx context.Context, id string, batchSize int, apply bool) (MigrationJob, error) {
	backend, principalKey, err := s.authorizeMigration(ctx)
	if err != nil {
		return MigrationJob{}, err
	}
	var job port.SessionMigrationJob
	if apply {
		inspection, inspectErr := backend.InspectSessionMigration(ctx)
		if inspectErr != nil {
			return MigrationJob{}, sanitizedMigrationBackendError()
		}
		if id != principalKey+"."+inspection.Generation {
			return MigrationJob{}, ErrMigrationConflict
		}
		handle, handleErr := newMigrationHandle()
		if handleErr != nil {
			return MigrationJob{}, sanitizedMigrationBackendError()
		}
		now := time.Now().UTC()
		job = port.SessionMigrationJob{
			ID: handle, PrincipalKey: principalKey, State: port.SessionMigrationPlanned,
			Generation: inspection.Generation, CreatedAt: now, UpdatedAt: now,
			V1Families: inspection.V1Families, V2Families: inspection.V2Families,
			InvalidFamilies: inspection.InvalidFamilies, SkippedFamilies: inspection.SkippedFamilies,
			CurrentBytes: inspection.CurrentBytes, ReclaimableBytes: inspection.ReclaimableBytes,
			TemporaryBytes: inspection.TemporaryBytes, TerminalItems: make(map[string]bool),
		}
	} else {
		job, err = loadBoundMigrationJob(ctx, backend, id, principalKey)
		if err != nil {
			return MigrationJob{}, err
		}
	}
	if apply && job.State != port.SessionMigrationPlanned && job.State != port.SessionMigrationRunning {
		return MigrationJob{}, ErrMigrationConflict
	}
	if !apply && job.State == port.SessionMigrationPlanned {
		return MigrationJob{}, ErrMigrationConflict
	}
	if job.State == port.SessionMigrationCompleted {
		return migrationJobProjection(job), nil
	}
	job.State = port.SessionMigrationRunning
	job.UpdatedAt = time.Now().UTC()
	if err := backend.SaveSessionMigrationJob(ctx, job); err != nil {
		return MigrationJob{}, sanitizedMigrationBackendError()
	}
	inspection, err := backend.InspectSessionMigration(ctx)
	if err != nil {
		return MigrationJob{}, sanitizedMigrationBackendError()
	}
	limit := migrationBatchSize(batchSize)
	attempted := 0
	for _, family := range inspection.Families {
		if attempted >= limit || ctx.Err() != nil {
			break
		}
		if job.TerminalItems[family.Handle] {
			continue
		}
		latest, loadErr := backend.LoadSessionMigrationJob(ctx, id)
		if loadErr == nil && latest.State == port.SessionMigrationCancelled {
			job.State = port.SessionMigrationCancelled
			break
		}
		reason := s.migrateOneFamily(ctx, backend, family)
		attempted++
		job.Processed++
		job.TerminalItems[family.Handle] = true
		switch reason {
		case "":
			job.Migrated++
		case "changed", "active", "leased":
			job.SkippedFamilies++
		default:
			job.Failed++
			if len(job.Errors) < maxMigrationErrors {
				job.Errors = append(job.Errors, port.SessionMigrationError{
					ItemHandle: family.Handle, ReasonCode: reason, Message: migrationReasonMessage(reason),
				})
			}
		}
		job.UpdatedAt = time.Now().UTC()
		if err := backend.SaveSessionMigrationJob(context.WithoutCancel(ctx), job); err != nil {
			return MigrationJob{}, sanitizedMigrationBackendError()
		}
	}
	remaining := false
	for _, family := range inspection.Families {
		if !job.TerminalItems[family.Handle] {
			remaining = true
			break
		}
	}
	if !remaining && job.State != port.SessionMigrationCancelled {
		job.State = port.SessionMigrationCompleted
	}
	job.UpdatedAt = time.Now().UTC()
	if err := backend.SaveSessionMigrationJob(context.WithoutCancel(ctx), job); err != nil {
		return MigrationJob{}, sanitizedMigrationBackendError()
	}
	return migrationJobProjection(job), nil
}

func migrationBatchSize(size int) int {
	if size <= 0 {
		return defaultMigrationBatch
	}
	if size > maxMigrationBatch {
		return maxMigrationBatch
	}
	return size
}

func (s *Service) migrateOneFamily(ctx context.Context, backend port.SessionMigrationStore, family port.SessionMigrationFamily) string {
	unlock := s.runEntryMu.lock(family.ID)
	defer unlock()
	if s.IsLive(family.ID) {
		return "active"
	}
	release, err := s.acquireMutationLease(ctx, family.ID)
	if err != nil {
		if errors.Is(err, ErrSessionLeasedElsewhere) {
			return "leased"
		}
		return "backend_failure"
	}
	defer release()
	if s.IsLive(family.ID) {
		return "active"
	}
	sess, err := s.cfg.Store.Load(ctx, family.ID)
	if err != nil {
		return "changed"
	}
	if sess.State == session.StateRunning || sess.State == session.StateAwaiting || sess.Kind != family.Kind ||
		sess.State != family.State || migrationSessionOwnerKey(sess.Owner) != family.OwnerKey {
		return "changed"
	}
	reason, err := backend.MigrateSessionFamily(ctx, family)
	if err != nil {
		return "backend_failure"
	}
	return reason
}

func migrationSessionOwnerKey(owner *session.Principal) string {
	if owner == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	return hex.EncodeToString(sum[:16])
}

// CancelSessionMigration stops future items without rolling back committed families.
func (s *Service) CancelSessionMigration(ctx context.Context, id string) (MigrationJob, error) {
	backend, principalKey, err := s.authorizeMigration(ctx)
	if err != nil {
		return MigrationJob{}, err
	}
	job, err := loadBoundMigrationJob(ctx, backend, id, principalKey)
	if err != nil {
		return MigrationJob{}, err
	}
	if job.State != port.SessionMigrationCompleted {
		job.State = port.SessionMigrationCancelled
		job.UpdatedAt = time.Now().UTC()
		if err := backend.SaveSessionMigrationJob(ctx, job); err != nil {
			return MigrationJob{}, sanitizedMigrationBackendError()
		}
	}
	return migrationJobProjection(job), nil
}

// SessionMigrationJob returns a caller-bound sanitized durable job projection.
func (s *Service) SessionMigrationJob(ctx context.Context, id string) (MigrationJob, error) {
	backend, principalKey, err := s.authorizeMigration(ctx)
	if err != nil {
		return MigrationJob{}, err
	}
	job, err := loadBoundMigrationJob(ctx, backend, id, principalKey)
	if err != nil {
		return MigrationJob{}, err
	}
	return migrationJobProjection(job), nil
}

func loadBoundMigrationJob(ctx context.Context, backend port.SessionMigrationStore, id, principalKey string) (port.SessionMigrationJob, error) {
	job, err := backend.LoadSessionMigrationJob(ctx, id)
	if err != nil || job.PrincipalKey != principalKey {
		return port.SessionMigrationJob{}, ErrManagementUnauthorized
	}
	return job, nil
}

func migrationJobProjection(job port.SessionMigrationJob) MigrationJob {
	out := MigrationJob{
		ID: job.ID, State: string(job.State), V1Families: job.V1Families, V2Families: job.V2Families,
		InvalidFamilies: job.InvalidFamilies, SkippedFamilies: job.SkippedFamilies,
		CurrentBytes: job.CurrentBytes, ReclaimableBytes: job.ReclaimableBytes, TemporaryBytes: job.TemporaryBytes,
		Processed: job.Processed, Migrated: job.Migrated, Failed: job.Failed,
		Errors: make([]MigrationItemError, 0, len(job.Errors)),
	}
	for _, item := range job.Errors {
		out.Errors = append(out.Errors, MigrationItemError{ItemHandle: item.ItemHandle, ReasonCode: item.ReasonCode, Message: item.Message})
	}
	return out
}

func migrationReasonMessage(reason string) string {
	switch reason {
	case "invalid_snapshot":
		return "snapshot is invalid and was left unchanged"
	case "insufficient_space":
		return "temporary space is insufficient; the source remains authoritative"
	case "verification_failed":
		return "replacement verification failed; the source remains authoritative"
	default:
		return "storage maintenance could not process this item"
	}
}

func sanitizedMigrationBackendError() error {
	return ErrMigrationBackend
}
