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
	cleanupBackendFailure = "backend_failure"
)

// CleanupScope is the exact kind subset a manual plan covers. Empty means all
// durable kinds owned by the authenticated principal.
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

// PlanManualRetention and PlanAutomaticRetention intentionally call the same
// side-effect-free planner. They are separate names only to make parity explicit
// at their two composition call sites.
func PlanManualRetention(rows []port.SessionDiscoveryMeta, policy RetentionPolicy, owner *session.Principal, live, leased map[session.SessionID]bool, now time.Time) sessionretention.Result {
	return sessionretention.Plan(rows, retentionPlannerPolicy(policy), sessionretention.Scope{Owner: owner}, sessionretention.RuntimeProtection{Live: live, Leased: leased}, now)
}

// PlanAutomaticRetention invokes the same pure planner as manual cleanup.
func PlanAutomaticRetention(rows []port.SessionDiscoveryMeta, policy RetentionPolicy, owner *session.Principal, live, leased map[session.SessionID]bool, now time.Time) sessionretention.Result {
	return PlanManualRetention(rows, policy, owner, live, leased, now)
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

func (s *Service) authorizeCleanup(ctx context.Context) (*session.Principal, string, error) {
	if s.cfg.StorageManagementAuthorized == nil || !s.cfg.StorageManagementAuthorized(ctx) {
		return nil, "", ErrManagementUnauthorized
	}
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return nil, "", ErrManagementUnauthorized
	}
	return principal, cleanupPrincipalKey(principal), nil
}

func cleanupPrincipalKey(principal *session.Principal) string {
	if principal == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(principal.Issuer + "\x00" + principal.Subject))
	return hex.EncodeToString(sum[:16])
}

// PlanSessionCleanup performs no writes. Ownership is pushed into the pager so
// foreign rows never enter page formation, aggregate counts, or token material.
func (s *Service) PlanSessionCleanup(ctx context.Context, scope CleanupScope) (CleanupPlan, error) {
	principal, principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupPlan{}, err
	}
	pager, pageOK := s.cfg.Store.(port.SessionMetadataPager)
	_, pruneOK := s.cfg.Store.(port.ConditionalPrunableStore)
	if !pageOK || !pruneOK || !supportsCleanupDelete(s.cfg.Store) {
		return CleanupPlan{UnavailableReason: "backend_unsupported"}, nil
	}
	rows, err := cleanupMetadata(ctx, pager, principal)
	if err != nil {
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) || errors.Is(err, port.ErrPruneUnsupported) {
			return CleanupPlan{UnavailableReason: "backend_unsupported"}, nil
		}
		return CleanupPlan{}, ErrCleanupBackend
	}
	rows = filterCleanupScope(rows, scope)
	live, leased := s.cleanupRuntimeProtection(rows)
	result := PlanManualRetention(rows, s.cfg.RetentionPolicy, principal, live, leased, s.cfg.Now())
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
			Limit: 1<<31 - 1, Cursor: cursor, OwnershipEnforced: true, Owner: owner,
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

func (s *Service) cleanupRuntimeProtection(rows []port.SessionDiscoveryMeta) (map[session.SessionID]bool, map[session.SessionID]bool) {
	live := make(map[session.SessionID]bool)
	leased := make(map[session.SessionID]bool)
	s.mu.Lock()
	for _, row := range rows {
		if _, ok := s.runs[row.ID]; ok {
			live[row.ID] = true
		}
		if _, ok := s.heldLeases[row.ID]; ok {
			leased[row.ID] = true
		}
	}
	s.mu.Unlock()
	return live, leased
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
	principal, principalKey, err := s.authorizeCleanup(ctx)
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
	job := cleanupJobRecord{CleanupJob: CleanupJob{ID: payload.JobID, State: "running"}, PrincipalKey: principalKey, CreatedAt: s.cfg.Now()}
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
	for _, candidate := range plan.Eligible {
		if ctx.Err() != nil || s.cleanupCancelled(job.ID, principalKey) {
			job.State = cleanupStateCancelled
			break
		}
		job.Processed++
		reason := s.deleteCleanupCandidate(ctx, principal, candidate)
		recordCleanupOutcome(&job, candidate, reason)
		job.State = "running"
		s.rememberCleanupJob(job)
	}
	if s.cleanupCancelled(job.ID, principalKey) {
		job.State = cleanupStateCancelled
	} else if job.State == "running" {
		job.State = "completed"
	}
	s.rememberCleanupJob(job)
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
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func cleanupItemHandle(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func (s *Service) deleteCleanupCandidate(ctx context.Context, principal *session.Principal, candidate CleanupCandidate) string {
	unlock := s.runEntryMu.lock(candidate.ID)
	defer unlock()
	if s.IsLive(candidate.ID) {
		return maintenanceReasonActive
	}
	release, err := s.acquireMutationLease(ctx, candidate.ID)
	if err != nil {
		if errors.Is(err, ErrSessionLeasedElsewhere) {
			return maintenanceReasonLeased
		}
		return cleanupBackendFailure
	}
	defer release()
	if s.IsLive(candidate.ID) {
		return maintenanceReasonActive
	}
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok {
		return cleanupBackendFailure
	}
	rows, err := cleanupMetadata(ctx, pager, principal)
	if err != nil {
		return cleanupBackendFailure
	}
	if !cleanupMetadataContains(rows, principal, candidate) {
		return maintenanceReasonChanged
	}
	loaded, err := s.cfg.Store.Load(ctx, candidate.ID)
	if err != nil || !cleanupSessionMatches(loaded, principal, candidate) {
		return maintenanceReasonChanged
	}
	pruner, ok := s.cfg.Store.(port.ConditionalPrunableStore)
	if !ok {
		return cleanupBackendFailure
	}
	deleted, err := pruner.DeleteSessionIfUnchanged(ctx, candidate.metadata)
	if err != nil {
		return cleanupBackendFailure
	}
	if !deleted {
		return maintenanceReasonChanged
	}
	return ""
}

func cleanupMetadataContains(rows []port.SessionDiscoveryMeta, principal *session.Principal, candidate CleanupCandidate) bool {
	for _, row := range rows {
		if row.ID == candidate.ID && row.Kind == candidate.Kind && row.State == candidate.State &&
			row.ModifiedAt.Equal(candidate.ModifiedAt) && principal.SameIdentity(row.Owner) {
			return true
		}
	}
	return false
}

func cleanupSessionMatches(loaded *session.Session, principal *session.Principal, candidate CleanupCandidate) bool {
	if loaded == nil || !sameCleanupOwner(principal, loaded.Owner) || loaded.Kind != candidate.Kind || loaded.State != candidate.State {
		return false
	}
	return loaded.State != session.StateRunning && loaded.State != session.StateAwaiting
}

func sameCleanupOwner(expected, actual *session.Principal) bool {
	return expected == nil && actual == nil || expected != nil && expected.SameIdentity(actual)
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
	_, principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.cleanupJobs[id]
	if !ok || job.PrincipalKey != principalKey {
		return CleanupJob{}, ErrManagementUnauthorized
	}
	job.cancelled = true
	if job.State == "running" || job.State == "planned" {
		job.State = cleanupStateCancelled
	}
	s.cleanupJobs[id] = job
	return job.CleanupJob, nil
}

// SessionCleanupJob returns one caller-bound sanitized maintenance projection.
func (s *Service) SessionCleanupJob(ctx context.Context, id string) (CleanupJob, error) {
	_, principalKey, err := s.authorizeCleanup(ctx)
	if err != nil {
		return CleanupJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.cleanupJobs[id]
	if !ok || job.PrincipalKey != principalKey {
		return CleanupJob{}, ErrManagementUnauthorized
	}
	out := job.CleanupJob
	out.Errors = append([]CleanupItemError(nil), job.Errors...)
	return out, nil
}
