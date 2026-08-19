package port

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// SessionMigrationState is the durable lifecycle of a semantics-preserving
// session snapshot migration job.
type SessionMigrationState string

const (
	// SessionMigrationPlanned is a read-only plan not yet applied.
	SessionMigrationPlanned SessionMigrationState = "planned"
	// SessionMigrationRunning may process another bounded batch.
	SessionMigrationRunning SessionMigrationState = "running"
	// SessionMigrationCancelled retains committed work and stops future items.
	SessionMigrationCancelled SessionMigrationState = "cancelled"
	// SessionMigrationCompleted has converged all eligible families.
	SessionMigrationCompleted SessionMigrationState = "completed"
)

// SessionMigrationFamily is an adapter-private candidate projected through the
// optional migration capability. ID is never exposed on a management response.
type SessionMigrationFamily struct {
	ID          session.SessionID   `json:"-"`
	Handle      string              `json:"handle"`
	Fingerprint string              `json:"fingerprint"`
	OwnerKey    string              `json:"owner_key"`
	Kind        session.SessionKind `json:"kind"`
	State       session.State       `json:"state"`
	Bytes       int64               `json:"bytes"`
}

// SessionMigrationInspection is a read-only physical inventory. Families is
// consumed only by the server orchestrator and is not a wire projection.
type SessionMigrationInspection struct {
	Available         bool
	UnavailableReason string
	Generation        string
	V1Families        int64
	V2Families        int64
	InvalidFamilies   int64
	SkippedFamilies   int64
	CurrentBytes      int64
	ReclaimableBytes  int64
	TemporaryBytes    int64
	Families          []SessionMigrationFamily
}

// SessionMigrationError is one bounded, content-free terminal item result.
type SessionMigrationError struct {
	ItemHandle string `json:"item_handle"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

// SessionMigrationJob is durable resumable progress. PrincipalKey is a one-way
// binding and must never be projected to clients.
type SessionMigrationJob struct {
	ID               string                  `json:"id"`
	PrincipalKey     string                  `json:"principal_key"`
	State            SessionMigrationState   `json:"state"`
	Generation       string                  `json:"generation"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	V1Families       int64                   `json:"v1_families"`
	V2Families       int64                   `json:"v2_families"`
	InvalidFamilies  int64                   `json:"invalid_families"`
	SkippedFamilies  int64                   `json:"skipped_families"`
	CurrentBytes     int64                   `json:"current_bytes"`
	ReclaimableBytes int64                   `json:"reclaimable_bytes"`
	TemporaryBytes   int64                   `json:"temporary_bytes"`
	Processed        int64                   `json:"processed"`
	Migrated         int64                   `json:"migrated"`
	Failed           int64                   `json:"failed"`
	Errors           []SessionMigrationError `json:"errors,omitempty"`
	TerminalItems    map[string]bool         `json:"terminal_items,omitempty"`
}

// SessionMigrationStore is an optional physical-maintenance capability. The
// engine loop never consumes it; authenticated server composition does. A
// mutating load-to-checkpoint sequence must hold AcquireSessionMigrationJob for
// the job's opaque ID. Implementations must provide stable cross-process
// exclusion, bind the exact acquisition to the returned context, and reject
// ownership checks, mutations, and checkpoints made without that acquisition.
// The returned release function relinquishes it and must be called exactly once.
type SessionMigrationStore interface {
	InspectSessionMigration(context.Context) (SessionMigrationInspection, error)
	MigrateSessionFamily(context.Context, SessionMigrationFamily) (string, error)
	AcquireSessionMigrationJob(context.Context, string) (acquired context.Context, release func() error, err error)
	CheckSessionMigrationJobOwnership(context.Context) error
	SaveSessionMigrationJob(context.Context, SessionMigrationJob) error
	LoadSessionMigrationJob(context.Context, string) (SessionMigrationJob, error)
}
