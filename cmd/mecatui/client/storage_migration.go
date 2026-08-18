package client

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// SessionMigrator is the injectable, capability-gated physical optimization seam.
type SessionMigrator interface {
	PlanSessionMigration(context.Context) (SessionMigrationPlan, error)
	ApplySessionMigration(context.Context, string, int32) (SessionMigrationJob, error)
	ResumeSessionMigration(context.Context, string, int32) (SessionMigrationJob, error)
	CancelSessionMigration(context.Context, string) (SessionMigrationJob, error)
	GetSessionMigrationJob(context.Context, string) (SessionMigrationJob, error)
}

// SessionMigrationPlan is the proto-free read-only optimization estimate.
type SessionMigrationPlan struct {
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

// SessionMigrationItemError is a stable, sanitized item failure.
type SessionMigrationItemError struct {
	ItemHandle string
	ReasonCode string
	Message    string
}

// SessionMigrationJob is resumable bounded progress for a physical migration.
type SessionMigrationJob struct {
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
	Errors           []SessionMigrationItemError
}

// PlanSessionMigration fetches a read-only, caller-bound physical migration estimate.
func (c *Client) PlanSessionMigration(ctx context.Context) (SessionMigrationPlan, error) {
	resp, err := c.svc.PlanSessionMigration(ctx, &mecatlv1.PlanSessionMigrationRequest{})
	if err != nil {
		return SessionMigrationPlan{}, err
	}
	return migrationPlanFromProto(resp), nil
}

// ApplySessionMigration starts a durable job and processes its first bounded batch.
func (c *Client) ApplySessionMigration(ctx context.Context, planID string, batchSize int32) (SessionMigrationJob, error) {
	resp, err := c.svc.ApplySessionMigration(ctx, &mecatlv1.ApplySessionMigrationRequest{PlanId: planID, BatchSize: batchSize})
	if err != nil {
		return SessionMigrationJob{}, err
	}
	return migrationJobFromProto(resp), nil
}

// ResumeSessionMigration processes another bounded batch of an existing job.
func (c *Client) ResumeSessionMigration(ctx context.Context, jobID string, batchSize int32) (SessionMigrationJob, error) {
	resp, err := c.svc.ResumeSessionMigration(ctx, &mecatlv1.ResumeSessionMigrationRequest{JobId: jobID, BatchSize: batchSize})
	if err != nil {
		return SessionMigrationJob{}, err
	}
	return migrationJobFromProto(resp), nil
}

// CancelSessionMigration prevents future items while retaining committed families.
func (c *Client) CancelSessionMigration(ctx context.Context, jobID string) (SessionMigrationJob, error) {
	resp, err := c.svc.CancelSessionMigration(ctx, &mecatlv1.CancelSessionMigrationRequest{JobId: jobID})
	if err != nil {
		return SessionMigrationJob{}, err
	}
	return migrationJobFromProto(resp), nil
}

// GetSessionMigrationJob fetches sanitized caller-bound durable progress.
func (c *Client) GetSessionMigrationJob(ctx context.Context, jobID string) (SessionMigrationJob, error) {
	resp, err := c.svc.GetSessionMigrationJob(ctx, &mecatlv1.GetSessionMigrationJobRequest{JobId: jobID})
	if err != nil {
		return SessionMigrationJob{}, err
	}
	return migrationJobFromProto(resp), nil
}

func migrationPlanFromProto(p *mecatlv1.SessionMigrationPlan) SessionMigrationPlan {
	if p == nil {
		return SessionMigrationPlan{}
	}
	return SessionMigrationPlan{
		ID: p.GetPlanId(), Available: p.GetAvailable(), UnavailableReason: p.GetUnavailableReason(),
		V1Families: p.GetV1Families(), V2Families: p.GetV2Families(), InvalidFamilies: p.GetInvalidFamilies(),
		SkippedFamilies: p.GetSkippedFamilies(), CurrentBytes: p.GetCurrentBytes(),
		ReclaimableBytes: p.GetReclaimableBytes(), TemporaryBytes: p.GetTemporaryBytes(),
	}
}

func migrationJobFromProto(p *mecatlv1.SessionMigrationJob) SessionMigrationJob {
	if p == nil {
		return SessionMigrationJob{}
	}
	out := SessionMigrationJob{
		ID: p.GetJobId(), State: p.GetState(), V1Families: p.GetV1Families(), V2Families: p.GetV2Families(),
		InvalidFamilies: p.GetInvalidFamilies(), SkippedFamilies: p.GetSkippedFamilies(),
		CurrentBytes: p.GetCurrentBytes(), ReclaimableBytes: p.GetReclaimableBytes(), TemporaryBytes: p.GetTemporaryBytes(),
		Processed: p.GetProcessed(), Migrated: p.GetMigrated(), Failed: p.GetFailed(),
		Errors: make([]SessionMigrationItemError, 0, len(p.GetErrors())),
	}
	for _, item := range p.GetErrors() {
		out.Errors = append(out.Errors, SessionMigrationItemError{ItemHandle: item.GetItemHandle(), ReasonCode: item.GetReasonCode(), Message: item.GetMessage()})
	}
	return out
}
