package client

import (
	"context"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// RetentionPolicy is the proto-free effective policy shown by the UI.
type RetentionPolicy struct {
	MainMaxAge, ChildMaxAge, ScheduledMaxAge       time.Duration
	MainMaxCount, ChildMaxCount, ScheduledMaxCount int
	SweepCadence                                   time.Duration
}

// StorageHealth is a proto-free aggregate management view. It deliberately
// contains no session identity, owner, path, or content fields.
type StorageHealth struct {
	Available                                                         bool
	UnavailableReason                                                 string
	CurrentBytes                                                      int64
	CurrentBytesAvailable                                             bool
	ReclaimableBytes                                                  int64
	ReclaimableBytesAvailable                                         bool
	SessionCount, FileCount                                           int64
	V1Count, V2Count                                                  int64
	MainCount, ChildCount, ScheduledCount, UnknownCount, CorruptCount int64
	Policy                                                            RetentionPolicy
	LastSweep, NextSweep                                              time.Time
	LastSweepAvailable, NextSweepAvailable                            bool
	ActiveJob, LastFailure                                            string
}

// StorageHealthFetcher is the injectable UI seam.
type StorageHealthFetcher interface {
	GetStorageHealth(context.Context) (StorageHealth, error)
}

// GetStorageHealth fetches the authenticated aggregate status.
func (c *Client) GetStorageHealth(ctx context.Context) (StorageHealth, error) {
	resp, err := c.svc.GetStorageHealth(ctx, &mecatlv1.GetStorageHealthRequest{})
	if err != nil {
		return StorageHealth{}, err
	}
	return storageHealthFromProto(resp), nil
}

func storageHealthFromProto(p *mecatlv1.GetStorageHealthResponse) StorageHealth {
	if p == nil {
		return StorageHealth{}
	}
	h := StorageHealth{
		Available: p.GetAvailable(), UnavailableReason: p.GetUnavailableReason(),
		CurrentBytes: p.GetCurrentBytes(), CurrentBytesAvailable: p.GetCurrentBytesAvailable(),
		ReclaimableBytes: p.GetReclaimableBytes(), ReclaimableBytesAvailable: p.GetReclaimableBytesAvailable(),
		SessionCount: p.GetSessionCount(), FileCount: p.GetFileCount(), V1Count: p.GetV1Count(), V2Count: p.GetV2Count(),
		MainCount: p.GetMainCount(), ChildCount: p.GetChildCount(), ScheduledCount: p.GetScheduledCount(), UnknownCount: p.GetUnknownCount(), CorruptCount: p.GetCorruptCount(),
		LastSweepAvailable: p.GetLastSweepAvailable(), NextSweepAvailable: p.GetNextSweepAvailable(),
		ActiveJob: p.GetActiveJob(), LastFailure: p.GetLastFailure(),
	}
	if policy := p.GetPolicy(); policy != nil {
		h.Policy = RetentionPolicy{
			MainMaxAge: time.Duration(policy.GetMainMaxAgeSeconds()) * time.Second, MainMaxCount: int(policy.GetMainMaxCount()),
			ChildMaxAge: time.Duration(policy.GetChildMaxAgeSeconds()) * time.Second, ChildMaxCount: int(policy.GetChildMaxCount()),
			ScheduledMaxAge: time.Duration(policy.GetScheduledMaxAgeSeconds()) * time.Second, ScheduledMaxCount: int(policy.GetScheduledMaxCount()),
			SweepCadence: time.Duration(policy.GetSweepCadenceSeconds()) * time.Second,
		}
	}
	if h.LastSweepAvailable {
		h.LastSweep = time.Unix(p.GetLastSweepUnix(), 0)
	}
	if h.NextSweepAvailable {
		h.NextSweep = time.Unix(p.GetNextSweepUnix(), 0)
	}
	return h
}
