package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/sessionretention"
)

const (
	cleanupTokenVersion = "cleanup-plan-v1"
	maxCleanupJobs      = 128
	maxCleanupErrors    = 256

	cleanupStateCancelled = "cancelled"
	cleanupStateRunning   = "running"
	cleanupKind           = "cleanup"
	cleanupBackendFailure = "backend_failure"

	maintenanceReasonChanged = "changed"
	maintenanceReasonActive  = "active"
	maintenanceReasonLeased  = "leased"
)

// CleanupScope is the exact durable-kind subset a store-wide management plan covers.
type CleanupScope struct {
	Kinds []session.SessionKind
}

// CleanupCandidate is content-free dry-run metadata, ordered oldest-first.
type CleanupCandidate struct {
	ID             session.SessionID
	Kind           session.SessionKind
	State          session.State
	Reason         string
	ModifiedAt     time.Time
	EstimatedBytes int64
	metadata       port.SessionDiscoveryMeta
}

// CleanupCounts reports bounded aggregate classification without content.
type CleanupCounts struct {
	Total    int
	ByKind   map[string]int
	ByState  map[string]int
	ByReason map[string]int
}

// CleanupPlan is a read-only manual retention result.
type CleanupPlan struct {
	Token             string
	JobID             string
	Available         bool
	UnavailableReason string
	Generation        string
	PolicyVersion     string
	Eligible          []CleanupCandidate
	EligibleCounts    CleanupCounts
	Protected         CleanupCounts
	EstimatedBytes    int64
}

// CleanupItemError is a stable sanitized partial failure.
type CleanupItemError struct {
	ItemHandle string
	ReasonCode string
	Message    string
}

// CleanupJob is a caller-bound management projection.
type CleanupJob struct {
	ID        string
	State     string
	Processed int
	Deleted   int
	Skipped   int
	Stale     int
	Failed    int
	Errors    []CleanupItemError
}

type cleanupJobRecord struct {
	CleanupJob
	PrincipalKey string
	CreatedAt    time.Time
	cancelled    bool
}

type cleanupTokenPayload struct {
	Version        string   `json:"v"`
	PrincipalKey   string   `json:"p"`
	Generation     string   `json:"g"`
	ScopeKinds     []string `json:"s"`
	PolicyVersion  string   `json:"r"`
	EligibleCount  int      `json:"n"`
	EstimatedBytes int64    `json:"b"`
	JobID          string   `json:"j"`
	IssuedAt       int64    `json:"i"`
}

// PlanManualRetention runs the shared, side-effect-free retention planner
// (sessionretention.Plan) behind the manual cleanup API. internal/app's
// automatic sweep (childGC.sweep) calls sessionretention.Plan directly with
// the same policy/scope shape; see
// TestSessionStorageContinuity_Scenario5_AutomaticManualPlannerParity in
// internal/app/childgc_test.go for the cross-check between the two real call
// sites (AC5.5).
func PlanManualRetention(rows []port.SessionDiscoveryMeta, policy RetentionPolicy, owner *session.Principal, live, leased map[session.SessionID]bool, now time.Time) sessionretention.Result {
	return sessionretention.Plan(rows, retentionPlannerPolicy(policy), sessionretention.Scope{Owner: owner}, sessionretention.RuntimeProtection{Live: live, Leased: leased}, now)
}

func retentionPlannerPolicy(policy RetentionPolicy) sessionretention.Policy {
	return sessionretention.Policy{
		Version: policyVersion(policy), MainMaxAge: policy.MainMaxAge, MainMaxCount: policy.MainMaxCount,
		ChildMaxAge: policy.ChildMaxAge, ChildMaxCount: policy.ChildMaxCount,
		ScheduledMaxAge: policy.ScheduledMaxAge, ScheduledMaxCount: policy.ScheduledMaxCount,
	}
}

func policyVersion(policy RetentionPolicy) string {
	if policy.Version != "" {
		return policy.Version
	}
	normalized := policy
	normalized.Version = ""
	encoded, _ := json.Marshal(normalized)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

func (s *Service) authorizeCleanup(ctx context.Context) (string, error) {
	key, err := s.storageManagementPrincipalKey(ctx)
	if err != nil {
		return "", err
	}
	if !s.maintenanceMutationAvailable() {
		return key, ErrMaintenanceExclusionUnavailable
	}
	return key, nil
}

// PlanSessionCleanup performs no writes. The explicit management gate runs
// before the store-wide pager, so tenants cannot form pages or aggregate counts.
func (s *Service) PlanSessionCleanup(ctx context.Context, scope CleanupScope) (CleanupPlan, error) {
	principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		if errors.Is(err, ErrMaintenanceExclusionUnavailable) {
			return CleanupPlan{UnavailableReason: "maintenance_exclusion_unavailable"}, nil
		}
		return CleanupPlan{}, err
	}
	pager, pageOK := s.cfg.Store.(port.SessionMetadataPager)
	pageOK = pageOK && port.SupportsSessionMetadataPaging(s.cfg.Store)
	_, pruneOK := s.cfg.Store.(port.ConditionalPrunableStore)
	if !pageOK || !pruneOK || !supportsCleanupDelete(s.cfg.Store) {
		return CleanupPlan{UnavailableReason: storageBackendUnsupported}, nil
	}
	rows, err := cleanupMetadata(ctx, pager, nil)
	if err != nil {
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) || errors.Is(err, port.ErrPruneUnsupported) {
			return CleanupPlan{UnavailableReason: storageBackendUnsupported}, nil
		}
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: "cleanup-plan", State: StorageMaintenanceFailed, Failure: "cleanup: storage metadata unavailable"})
		return CleanupPlan{}, ErrCleanupBackend
	}
	if s.cfg.OwnershipEnforced {
		owned := rows[:0]
		for _, row := range rows {
			if row.Owner != nil {
				owned = append(owned, row)
			}
		}
		rows = owned
	}
	rows = filterCleanupScope(rows, scope)
	live, leased, err := s.cleanupRuntimeProtection(ctx, rows)
	if err != nil {
		if errors.Is(err, ErrMaintenanceExclusionUnavailable) {
			return CleanupPlan{UnavailableReason: "maintenance_exclusion_unavailable"}, nil
		}
		if ctx.Err() != nil {
			return CleanupPlan{}, ctx.Err()
		}
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: "cleanup-plan", State: StorageMaintenanceFailed, Failure: "cleanup: lease status unavailable"})
		return CleanupPlan{}, ErrCleanupBackend
	}
	result := PlanManualRetention(rows, s.cfg.RetentionPolicy, nil, live, leased, s.cfg.Now())
	plan := cleanupPlanProjection(result)
	plan.Available = true
	plan.PolicyVersion = policyVersion(s.cfg.RetentionPolicy)
	plan.JobID, err = newCleanupID()
	if err != nil {
		return CleanupPlan{}, ErrCleanupBackend
	}
	payload := cleanupTokenPayload{
		Version: cleanupTokenVersion, PrincipalKey: principalKey, Generation: result.Generation,
		ScopeKinds: canonicalScope(scope), PolicyVersion: plan.PolicyVersion,
		EligibleCount: len(result.Eligible), EstimatedBytes: plan.EstimatedBytes, JobID: plan.JobID, IssuedAt: s.cfg.Now().UnixNano(),
	}
	plan.Token, err = s.signCleanupToken(payload)
	if err != nil {
		return CleanupPlan{}, ErrCleanupBackend
	}
	s.rememberCleanupJob(cleanupJobRecord{CleanupJob: CleanupJob{ID: plan.JobID, State: "planned"}, PrincipalKey: principalKey, CreatedAt: s.cfg.Now()})
	return plan, nil
}

func supportsCleanupDelete(store port.SessionStore) bool {
	if _, ok := store.(port.ConditionalPrunableStore); !ok {
		return false
	}
	if support, ok := store.(port.SessionDeleteSupport); ok {
		return support.SupportsSessionDelete()
	}
	_, ok := store.(port.PrunableStore)
	return ok
}

func cleanupMetadata(ctx context.Context, pager port.SessionMetadataPager, owner *session.Principal) ([]port.SessionDiscoveryMeta, error) {
	var rows []port.SessionDiscoveryMeta
	var cursor *port.SessionMetadataCursor
	for {
		page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
			Limit: 1<<31 - 1, Cursor: cursor, OwnershipEnforced: owner != nil, Owner: owner,
		})
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Sessions...)
		if page.NextCursor == nil {
			return rows, nil
		}
		if len(page.Sessions) == 0 || cursor != nil && page.NextCursor.Generation == cursor.Generation && page.NextCursor.Continuation == cursor.Continuation && page.NextCursor.ID == cursor.ID {
			return nil, ErrCleanupBackend
		}
		next := *page.NextCursor
		cursor = &next
	}
}

func filterCleanupScope(rows []port.SessionDiscoveryMeta, scope CleanupScope) []port.SessionDiscoveryMeta {
	if len(scope.Kinds) == 0 {
		return rows
	}
	allowed := make(map[session.SessionKind]bool, len(scope.Kinds))
	for _, kind := range scope.Kinds {
		allowed[kind] = true
	}
	out := rows[:0]
	for _, row := range rows {
		if allowed[row.Kind] {
			out = append(out, row)
		}
	}
	return out
}

func canonicalScope(scope CleanupScope) []string {
	out := make([]string, 0, len(scope.Kinds))
	seen := make(map[string]bool, len(scope.Kinds))
	for _, kind := range scope.Kinds {
		if !seen[string(kind)] {
			seen[string(kind)] = true
			out = append(out, string(kind))
		}
	}
	sort.Strings(out)
	return out
}

// cleanupRuntimeProtection takes an instant-in-time process/lease snapshot for
// planning. Lease probes are sequential and individually bounded; pagination and
// caller cancellation therefore bound total work without an N-goroutine fan-out.
// Apply never relies on this snapshot: it reacquires and revalidates every item.
func (s *Service) cleanupRuntimeProtection(ctx context.Context, rows []port.SessionDiscoveryMeta) (map[session.SessionID]bool, map[session.SessionID]bool, error) {
	live := make(map[session.SessionID]bool)
	leased := make(map[session.SessionID]bool)
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if s.IsLive(row.ID) {
			live[row.ID] = true
			continue
		}
		if !s.cleanupLeaseProbeCandidate(row) {
			continue
		}
		held, err := s.probeMaintenanceMutationLease(ctx, row.ID)
		if err != nil {
			return nil, nil, err
		}
		if held {
			leased[row.ID] = true
		}
	}
	return live, leased, nil
}

func (s *Service) cleanupLeaseProbeCandidate(row port.SessionDiscoveryMeta) bool {
	if session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil {
		return false
	}
	switch row.State {
	case session.StateIdle, session.StateCompleted, session.StateFailed, session.StateCancelled:
	default:
		return false
	}
	policy := s.cfg.RetentionPolicy
	switch row.Kind {
	case session.SessionKindMain:
		return policy.MainMaxAge > 0 || policy.MainMaxCount > 0
	case session.SessionKindSubagent, session.SessionKindParallelBranch, session.SessionKindTeamMember:
		return policy.ChildMaxAge > 0 || policy.ChildMaxCount > 0
	case session.SessionKindScheduled:
		return policy.ScheduledMaxAge > 0 || policy.ScheduledMaxCount > 0
	default:
		return false
	}
}

func cleanupPlanProjection(result sessionretention.Result) CleanupPlan {
	plan := CleanupPlan{
		Generation: result.Generation, EligibleCounts: CleanupCounts{ByKind: map[string]int{}, ByState: map[string]int{}, ByReason: map[string]int{}},
		Protected: cleanupCounts(result.Protected), Eligible: make([]CleanupCandidate, 0, len(result.Eligible)),
	}
	for _, item := range result.Eligible {
		plan.Eligible = append(plan.Eligible, CleanupCandidate{ID: item.ID, Kind: item.Kind, State: item.State, Reason: item.Reason, ModifiedAt: item.ModifiedAt, EstimatedBytes: item.EstimatedBytes, metadata: item.Metadata})
		plan.EligibleCounts.Total++
		plan.EligibleCounts.ByKind[string(item.Kind)]++
		plan.EligibleCounts.ByState[string(item.State)]++
		plan.EligibleCounts.ByReason[item.Reason]++
		plan.EstimatedBytes += item.EstimatedBytes
	}
	return plan
}

func cleanupCounts(counts sessionretention.Counts) CleanupCounts {
	out := CleanupCounts{Total: counts.Total, ByKind: make(map[string]int), ByState: make(map[string]int), ByReason: make(map[string]int)}
	for kind, count := range counts.ByKind {
		out.ByKind[string(kind)] = count
	}
	for state, count := range counts.ByState {
		out.ByState[string(state)] = count
	}
	for reason, count := range counts.ByReason {
		out.ByReason[reason] = count
	}
	return out
}

func (s *Service) signCleanupToken(payload cleanupTokenPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.cleanupTokenKey[:])
	_, _ = mac.Write(encoded)
	token := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	s.mu.Lock()
	if len(s.cleanupPlans) >= maxCleanupJobs {
		var oldestToken string
		var oldest int64
		for candidate, plan := range s.cleanupPlans {
			if oldestToken == "" || plan.IssuedAt < oldest {
				oldestToken, oldest = candidate, plan.IssuedAt
			}
		}
		delete(s.cleanupPlans, oldestToken)
	}
	s.cleanupPlans[token] = payload
	s.mu.Unlock()
	return token, nil
}

func (s *Service) verifyCleanupToken(token string) (cleanupTokenPayload, bool) {
	s.mu.Lock()
	payload, ok := s.cleanupPlans[token]
	s.mu.Unlock()
	return payload, ok && payload.Version == cleanupTokenVersion
}

// ApplySessionCleanup re-plans before mutation, then serializes each deletion by
// run-entry lock -> maintenance lease -> backend family lock (inside Delete).
func (s *Service) ApplySessionCleanup(ctx context.Context, token string) (CleanupJob, error) {
	principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupJob{}, err
	}
	payload, valid := s.verifyCleanupToken(token)
	if !valid || payload.PrincipalKey != principalKey {
		return CleanupJob{}, ErrManagementUnauthorized
	}
	scope := CleanupScope{}
	for _, kind := range payload.ScopeKinds {
		scope.Kinds = append(scope.Kinds, session.SessionKind(kind))
	}
	plan, err := s.PlanSessionCleanup(ctx, scope)
	if err != nil {
		return CleanupJob{}, err
	}
	if !plan.Available {
		return CleanupJob{}, ErrCleanupUnsupported
	}
	job := cleanupJobRecord{CleanupJob: CleanupJob{ID: payload.JobID, State: cleanupStateRunning}, PrincipalKey: principalKey, CreatedAt: s.cfg.Now()}
	if s.cleanupCancelled(job.ID, principalKey) {
		job.State = cleanupStateCancelled
		s.rememberCleanupJob(job)
		return job.CleanupJob, nil
	}
	if payload.Generation != plan.Generation || payload.PolicyVersion != plan.PolicyVersion || payload.EligibleCount != len(plan.Eligible) || payload.EstimatedBytes != plan.EstimatedBytes {
		job.State = "stale"
		job.Stale = payload.EligibleCount
		s.rememberCleanupJob(job)
		return job.CleanupJob, ErrCleanupPlanStale
	}
	s.rememberCleanupJob(job)
	s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: job.ID, State: StorageMaintenanceStarted})
	for _, candidate := range plan.Eligible {
		if ctx.Err() != nil || s.cleanupCancelled(job.ID, principalKey) {
			job.State = cleanupStateCancelled
			break
		}
		job.Processed++
		reason := cleanupDeletionReason(s.DeleteSessionForRetentionCandidate(ctx, candidate.metadata))
		recordCleanupOutcome(&job, candidate, reason)
		job.State = cleanupStateRunning
		s.rememberCleanupJob(job)
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: job.ID, State: StorageMaintenanceProgress})
	}
	if s.cleanupCancelled(job.ID, principalKey) {
		job.State = cleanupStateCancelled
	} else if job.State == cleanupStateRunning {
		job.State = "completed"
	}
	s.rememberCleanupJob(job)
	failure := ""
	if job.Failed > 0 {
		failure = "cleanup: one or more items failed"
	}
	state := StorageMaintenanceCompleted
	if job.State == cleanupStateCancelled {
		state = StorageMaintenanceCancelled
	}
	s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: job.ID, State: state, Failure: failure})
	return job.CleanupJob, nil
}

func recordCleanupOutcome(job *cleanupJobRecord, candidate CleanupCandidate, reason string) {
	switch reason {
	case "":
		job.Deleted++
	case maintenanceReasonChanged:
		job.Skipped++
		job.Stale++
	case maintenanceReasonActive, maintenanceReasonLeased:
		job.Skipped++
	default:
		job.Failed++
		if len(job.Errors) < maxCleanupErrors {
			job.Errors = append(job.Errors, CleanupItemError{
				ItemHandle: cleanupItemHandle(candidate.ID), ReasonCode: cleanupBackendFailure,
				Message: "storage maintenance could not delete this item; retry is safe",
			})
		}
	}
}

func newCleanupID() (string, error) {
	return randomHexID(16)
}

// randomHexID returns a hex-encoded id from n cryptographically random bytes.
func randomHexID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cleanupItemHandle(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func cleanupDeletionReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errRetentionCandidateChanged):
		return maintenanceReasonChanged
	case errors.Is(err, errRetentionCandidateActive):
		return maintenanceReasonActive
	case errors.Is(err, ErrSessionLeasedElsewhere):
		return maintenanceReasonLeased
	default:
		return cleanupBackendFailure
	}
}

func (s *Service) rememberCleanupJob(job cleanupJobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.cleanupJobs[job.ID]; ok && existing.cancelled {
		job.cancelled = true
		job.State = cleanupStateCancelled
	}
	if len(s.cleanupJobs) >= maxCleanupJobs {
		var oldestID string
		var oldest time.Time
		for id, item := range s.cleanupJobs {
			if oldestID == "" || item.CreatedAt.Before(oldest) {
				oldestID, oldest = id, item.CreatedAt
			}
		}
		delete(s.cleanupJobs, oldestID)
	}
	s.cleanupJobs[job.ID] = job
}

func (s *Service) cleanupCancelled(id, principalKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.cleanupJobs[id]
	return ok && job.PrincipalKey == principalKey && job.cancelled
}

// CancelSessionCleanup stops future items; committed deletions are not rolled back.
func (s *Service) CancelSessionCleanup(ctx context.Context, id string) (CleanupJob, error) {
	principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupJob{}, err
	}
	s.mu.Lock()
	job, ok := s.cleanupJobs[id]
	if !ok || job.PrincipalKey != principalKey {
		s.mu.Unlock()
		return CleanupJob{}, ErrManagementUnauthorized
	}
	job.cancelled = true
	if job.State == cleanupStateRunning || job.State == "planned" {
		job.State = cleanupStateCancelled
	}
	s.cleanupJobs[id] = job
	s.mu.Unlock()
	s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: job.ID, State: StorageMaintenanceCancelled})
	return job.CleanupJob, nil
}

// SessionCleanupJob returns one caller-bound sanitized maintenance projection.
func (s *Service) SessionCleanupJob(ctx context.Context, id string) (CleanupJob, error) {
	principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupJob{}, err
	}
	s.mu.Lock()
	job, ok := s.cleanupJobs[id]
	if !ok || job.PrincipalKey != principalKey {
		s.mu.Unlock()
		return CleanupJob{}, ErrManagementUnauthorized
	}
	out := job.CleanupJob
	out.Errors = append([]CleanupItemError(nil), job.Errors...)
	s.mu.Unlock()
	if job.State == cleanupStateRunning {
		s.storageMaintenanceUpdate(StorageMaintenanceEvent{Kind: cleanupKind, Key: job.ID, State: StorageMaintenanceStarted})
	}
	return out, nil
}
