package client

import (
	"context"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// SessionCleaner is the injectable, capability-gated destructive cleanup seam.
type SessionCleaner interface {
	PlanSessionCleanup(context.Context, CleanupScope) (CleanupPlan, error)
	ApplySessionCleanup(context.Context, string) (CleanupJob, error)
	CancelSessionCleanup(context.Context, string) (CleanupJob, error)
	GetSessionCleanupJob(context.Context, string) (CleanupJob, error)
}

// CleanupScope is the exact durable-kind subset requested by a management client.
type CleanupScope struct{ Kinds []string }

// CleanupCandidate is one content-free cleanup candidate.
type CleanupCandidate struct {
	SessionID           string
	Kind, State, Reason string
	ModifiedAt          time.Time
	EstimatedBytes      int64
}

// CleanupCounts is a protected-row aggregate grouped by stable taxonomy.
type CleanupCounts struct {
	Total                     int
	ByKind, ByState, ByReason map[string]int
}

// CleanupPlan is the caller-bound dry-run projection.
type CleanupPlan struct {
	ConfirmationToken, PlannedJobID              string
	Available                                    bool
	UnavailableReason, Generation, PolicyVersion string
	Eligible                                     []CleanupCandidate
	EligibleCounts, Protected                    CleanupCounts
	EstimatedBytes                               int64
}

// CleanupItemError is one sanitized stable per-item failure.
type CleanupItemError struct{ ItemHandle, ReasonCode, Message string }

// CleanupJob is a bounded maintenance-job projection.
type CleanupJob struct {
	ID, State                                  string
	Processed, Deleted, Skipped, Stale, Failed int
	Errors                                     []CleanupItemError
}

// PlanSessionCleanup requests a read-only cleanup plan.
func (c *Client) PlanSessionCleanup(ctx context.Context, scope CleanupScope) (CleanupPlan, error) {
	resp, err := c.svc.PlanSessionCleanup(ctx, &mecatlv1.PlanSessionCleanupRequest{Kinds: append([]string(nil), scope.Kinds...)})
	if err != nil {
		return CleanupPlan{}, err
	}
	return cleanupPlanFromProto(resp), nil
}

// ApplySessionCleanup applies a caller-bound confirmation token.
func (c *Client) ApplySessionCleanup(ctx context.Context, token string) (CleanupJob, error) {
	resp, err := c.svc.ApplySessionCleanup(ctx, &mecatlv1.ApplySessionCleanupRequest{ConfirmationToken: token})
	if err != nil {
		return CleanupJob{}, err
	}
	return cleanupJobFromProto(resp), nil
}

// CancelSessionCleanup stops future items in a cleanup job.
func (c *Client) CancelSessionCleanup(ctx context.Context, id string) (CleanupJob, error) {
	resp, err := c.svc.CancelSessionCleanup(ctx, &mecatlv1.CancelSessionCleanupRequest{JobId: id})
	if err != nil {
		return CleanupJob{}, err
	}
	return cleanupJobFromProto(resp), nil
}

// GetSessionCleanupJob reads one caller-bound cleanup job.
func (c *Client) GetSessionCleanupJob(ctx context.Context, id string) (CleanupJob, error) {
	resp, err := c.svc.GetSessionCleanupJob(ctx, &mecatlv1.GetSessionCleanupJobRequest{JobId: id})
	if err != nil {
		return CleanupJob{}, err
	}
	return cleanupJobFromProto(resp), nil
}

func cleanupPlanFromProto(resp *mecatlv1.PlanSessionCleanupResponse) CleanupPlan {
	if resp == nil {
		return CleanupPlan{}
	}
	out := CleanupPlan{ConfirmationToken: resp.GetConfirmationToken(), PlannedJobID: resp.GetPlannedJobId(), Available: resp.GetAvailable(), UnavailableReason: resp.GetUnavailableReason(), Generation: resp.GetGeneration(), PolicyVersion: resp.GetPolicyVersion(), EstimatedBytes: resp.GetEstimatedBytes()}
	for _, item := range resp.GetEligible() {
		out.Eligible = append(out.Eligible, CleanupCandidate{SessionID: item.GetSessionId(), Kind: item.GetKind(), State: item.GetState(), Reason: item.GetReason(), ModifiedAt: time.Unix(item.GetModifiedAtUnix(), 0), EstimatedBytes: item.GetEstimatedBytes()})
	}
	if counts := resp.GetEligibleCounts(); counts != nil {
		out.EligibleCounts = CleanupCounts{Total: int(counts.GetTotal()), ByKind: intMap(counts.GetByKind()), ByState: intMap(counts.GetByState()), ByReason: intMap(counts.GetByReason())}
	}
	if counts := resp.GetProtected(); counts != nil {
		out.Protected = CleanupCounts{Total: int(counts.GetTotal()), ByKind: intMap(counts.GetByKind()), ByState: intMap(counts.GetByState()), ByReason: intMap(counts.GetByReason())}
	}
	return out
}

func cleanupJobFromProto(resp *mecatlv1.CleanupJob) CleanupJob {
	if resp == nil {
		return CleanupJob{}
	}
	out := CleanupJob{ID: resp.GetJobId(), State: resp.GetState(), Processed: int(resp.GetProcessed()), Deleted: int(resp.GetDeleted()), Skipped: int(resp.GetSkipped()), Stale: int(resp.GetStale()), Failed: int(resp.GetFailed())}
	for _, item := range resp.GetErrors() {
		out.Errors = append(out.Errors, CleanupItemError{ItemHandle: item.GetItemHandle(), ReasonCode: item.GetReasonCode(), Message: item.GetMessage()})
	}
	return out
}

func intMap(values map[string]int32) map[string]int {
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = int(value)
	}
	return out
}
