// Package sessionretention contains the side-effect-free retention selection policy.
package sessionretention

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	// ReasonAge identifies a candidate selected by its age limit.
	ReasonAge = "age"
	// ReasonCap identifies a candidate selected by its family count limit.
	ReasonCap = "cap"

	// ProtectedUnknown identifies missing or invalid durable taxonomy.
	ProtectedUnknown = "unknown_taxonomy"
	// ProtectedState identifies a running or awaiting durable state.
	ProtectedState = "active_state"
	// ProtectedLive identifies an in-process live session.
	ProtectedLive = "live"
	// ProtectedLeased identifies a session currently leased for mutation.
	ProtectedLeased = "leased"
	// ProtectedForeign identifies a row outside the caller-bound scope.
	ProtectedForeign = "foreign_owner"
)

// Policy is the effective retention policy consumed by both sweep and manual paths.
type Policy struct {
	Version           string
	MainMaxAge        time.Duration
	MainMaxCount      int
	ChildMaxAge       time.Duration
	ChildMaxCount     int
	ScheduledMaxAge   time.Duration
	ScheduledMaxCount int
}

// Scope restricts a plan to one authenticated owner when Owner is non-nil.
type Scope struct {
	Owner *session.Principal
}

// RuntimeProtection is a read-only snapshot of facts outside durable metadata.
type RuntimeProtection struct {
	Live   map[session.SessionID]bool
	Leased map[session.SessionID]bool
}

// Item is content-free retention metadata for one row.
type Item struct {
	ID             session.SessionID
	Kind           session.SessionKind
	State          session.State
	ModifiedAt     time.Time
	EstimatedBytes int64
	Reason         string
	OwnerKey       string
	Metadata       port.SessionDiscoveryMeta `json:"-"`
}

// Counts is a stable aggregate projection.
type Counts struct {
	Total    int
	ByKind   map[session.SessionKind]int
	ByState  map[session.State]int
	ByReason map[string]int
}

// Result is a deterministic plan. It contains no transcript or tool content.
type Result struct {
	Generation string
	Eligible   []Item
	Protected  Counts
}

// ContainsEligible reports whether id is selected for cleanup.
func (r Result) ContainsEligible(id session.SessionID) bool {
	return slices.ContainsFunc(r.Eligible, func(item Item) bool { return item.ID == id })
}

// Plan selects oldest-first candidates without performing I/O or mutation.
func Plan(rows []port.SessionDiscoveryMeta, policy Policy, scope Scope, protection RuntimeProtection, now time.Time) Result {
	result := Result{Protected: newCounts()}
	partitions := make(map[session.SessionKind][]Item)
	generationRows := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := Item{ID: row.ID, Kind: row.Kind, State: row.State, ModifiedAt: row.ModifiedAt, EstimatedBytes: row.EstimatedBytes, OwnerKey: ownerKey(row.Owner), Metadata: row}
		generationRows = append(generationRows, item)
		reason := protectedReason(row, scope, protection)
		if reason != "" {
			addCount(&result.Protected, item, reason)
			continue
		}
		partitions[row.Kind] = append(partitions[row.Kind], item)
	}
	result.Generation = generation(generationRows, policy, scope, protection)
	for kind, items := range partitions {
		sortItems(items)
		maxAge, maxCount := limits(kind, policy)
		cutoff := now.Add(-maxAge)
		survivors := make([]Item, 0, len(items))
		for _, item := range items {
			if maxAge > 0 && item.ModifiedAt.Before(cutoff) {
				item.Reason = ReasonAge
				result.Eligible = append(result.Eligible, item)
			} else {
				survivors = append(survivors, item)
			}
		}
		if maxCount > 0 && len(survivors) > maxCount {
			for i := 0; i < len(survivors)-maxCount; i++ {
				item := survivors[i]
				item.Reason = ReasonCap
				result.Eligible = append(result.Eligible, item)
			}
		}
	}
	sortItems(result.Eligible)
	return result
}

func protectedReason(row port.SessionDiscoveryMeta, scope Scope, protection RuntimeProtection) string {
	if scope.Owner != nil && !scope.Owner.SameIdentity(row.Owner) {
		return ProtectedForeign
	}
	if session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil || !retentionKind(row.Kind) {
		return ProtectedUnknown
	}
	switch row.State {
	case session.StateIdle, session.StateCompleted, session.StateFailed, session.StateCancelled:
	default:
		if row.State == session.StateRunning || row.State == session.StateAwaiting {
			return ProtectedState
		}
		return ProtectedUnknown
	}
	if protection.Live[row.ID] {
		return ProtectedLive
	}
	if protection.Leased[row.ID] {
		return ProtectedLeased
	}
	return ""
}

func retentionKind(kind session.SessionKind) bool {
	switch kind {
	case session.SessionKindMain, session.SessionKindScheduled, session.SessionKindSubagent,
		session.SessionKindParallelBranch, session.SessionKindTeamMember:
		return true
	default:
		return false
	}
}

func limits(kind session.SessionKind, policy Policy) (time.Duration, int) {
	switch kind {
	case session.SessionKindMain:
		return policy.MainMaxAge, policy.MainMaxCount
	case session.SessionKindScheduled:
		return policy.ScheduledMaxAge, policy.ScheduledMaxCount
	case session.SessionKindSubagent, session.SessionKindParallelBranch, session.SessionKindTeamMember:
		return policy.ChildMaxAge, policy.ChildMaxCount
	default:
		return 0, 0
	}
}

func sortItems(items []Item) {
	slices.SortStableFunc(items, func(a, b Item) int {
		return cmp.Or(a.ModifiedAt.Compare(b.ModifiedAt), cmp.Compare(a.ID, b.ID))
	})
}

func newCounts() Counts {
	return Counts{ByKind: make(map[session.SessionKind]int), ByState: make(map[session.State]int), ByReason: make(map[string]int)}
}

func addCount(counts *Counts, item Item, reason string) {
	counts.Total++
	counts.ByKind[item.Kind]++
	counts.ByState[item.State]++
	counts.ByReason[reason]++
}

func ownerKey(owner *session.Principal) string {
	if owner == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	return hex.EncodeToString(sum[:16])
}

func generation(rows []Item, policy Policy, scope Scope, protection RuntimeProtection) string {
	sortItems(rows)
	payload := struct {
		Rows       []Item
		Policy     Policy
		ScopeOwner string
		Live       []session.SessionID
		Leased     []session.SessionID
	}{Rows: rows, Policy: policy, ScopeOwner: ownerKey(scope.Owner)}
	for id, yes := range protection.Live {
		if yes {
			payload.Live = append(payload.Live, id)
		}
	}
	for id, yes := range protection.Leased {
		if yes {
			payload.Leased = append(payload.Leased, id)
		}
	}
	slices.Sort(payload.Live)
	slices.Sort(payload.Leased)
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
