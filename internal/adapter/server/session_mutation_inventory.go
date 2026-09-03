package server

import (
	"fmt"
	"sort"
	"strings"
)

// SessionMutationClass classifies application code that can touch a durable
// session family. It is deliberately separate from caller authorization:
// ownership is proved before coordination, while this inventory records the
// coordination required before storage mutation.
type SessionMutationClass uint8

const (
	// SessionMutationLeaseOwned acquires the session lease after runEntryMu and
	// keeps it through the backend call.
	SessionMutationLeaseOwned SessionMutationClass = iota
	// SessionMutationLeaseProven runs only for a registered run whose Service-owned
	// session lease is already held; lease loss invalidates that proof.
	SessionMutationLeaseProven
	// SessionMutationNewFamily is the creation-only exception: a caller-owned
	// identity is published with atomic create/reservation before any session can
	// have an existing lease owner. Every later mutation uses the lease classes.
	SessionMutationNewFamily
	// SessionMutationReadOnly reads session state without changing its family.
	SessionMutationReadOnly
	// SessionMutationComposition changes process-local composition state only.
	SessionMutationComposition
)

// SessionMutationEntry is one reviewed source-level inventory row.
type SessionMutationEntry struct {
	Class     SessionMutationClass
	Rationale string
}

func (e SessionMutationEntry) mutatesDurableFamily() bool {
	return e.Class == SessionMutationLeaseOwned || e.Class == SessionMutationLeaseProven || e.Class == SessionMutationNewFamily
}

// sessionMutationInventory names every function in the server package that the
// source guard observes calling a durable-session mutation seam. Helpers are
// included as well as exported boundaries so adding an indirect write path
// cannot evade review. Read-only and composition rows make those exemptions
// explicit rather than treating every Service method as an implicit non-mutator.
var sessionMutationInventory = map[string]SessionMutationEntry{
	"persistNewSession":                  {SessionMutationNewFamily, "atomic create publishes a fresh identity before any existing owner can hold its lease"},
	"persistCreatedSession":              {SessionMutationNewFamily, "delegates creation-only atomic publication to persistNewSession"},
	"createSession":                      {SessionMutationNewFamily, "binds ownership and reserves the fresh id before atomic publication"},
	"createPerSessionEngine":             {SessionMutationNewFamily, "builds process-local state then atomically publishes the fresh caller-owned family"},
	"persistPlacedCreatedSession":        {SessionMutationNewFamily, "validates exact server-owned placement before delegating atomic fresh-family publication"},
	"createPlacedSuccessor":              {SessionMutationLeaseOwned, "holds source run-entry and mutation lease while binding placement and publishing a fresh successor family"},
	"CompactSession":                     {SessionMutationLeaseOwned, "holds run-entry and the session mutation lease across snapshot save and archive appends"},
	"RenameSession":                      {SessionMutationLeaseOwned, "holds run-entry and the session mutation lease through title snapshot save"},
	"DeleteSession":                      {SessionMutationLeaseOwned, "holds run-entry and the session mutation lease through family deletion"},
	"DeleteSessionForRetentionCandidate": {SessionMutationLeaseOwned, "holds run-entry and the mandatory maintenance lease through conditional family deletion"},
	"DeleteSessionForRetention":          {SessionMutationLeaseOwned, "holds run-entry and the session mutation lease through retention deletion"},
	"SetMode":                            {SessionMutationLeaseOwned, "authorizes first then holds run-entry and the session mutation lease through mode save"},
	"repairTerminalState":                {SessionMutationLeaseProven, "called only inside a run-entry transaction after its session lease is acquired"},
	"reopenLoadedSession":                {SessionMutationLeaseProven, "all callers hold the target session mutation lease before terminal recovery"},
	"prepareFailedStepRetry":             {SessionMutationLeaseProven, "retry entry already holds the durable session lease before preparation save"},
	"startRunContent":                    {SessionMutationLeaseOwned, "run-entry owns runEntryMu then acquires the durable session lease before repair and drive"},
	"RetryFailedRun":                     {SessionMutationLeaseOwned, "retry entry owns runEntryMu then acquires the same durable session lease"},
	"GracefulDrain":                      {SessionMutationLeaseProven, "shutdown persists only joined runs while their previously acquired session lease remains valid"},
	"Persist":                            {SessionMutationLeaseProven, "relay persistence is admitted only while the registered run's held lease remains valid"},
	"saveSession":                        {SessionMutationLeaseProven, "single pre-backend snapshot-save gate revalidates the current process-local mutation capability"},
	"repairRunningSession":               {SessionMutationLeaseProven, "run-entry crash repair executes only after acquisition and persists through saveSession"},
	"deleteSessionFamily":                {SessionMutationLeaseProven, "single pre-backend family-delete gate revalidates the exact held session lease"},
	"appendEvent":                        {SessionMutationLeaseProven, "relay event append is admitted only while the registered run or management mutation holds the lease"},
	"engine/agent/dispatch.go:ToolCall":  {SessionMutationLeaseProven, "tool-call recording occurs only inside a Service-admitted run that already owns the session lease; engine remains lease-unaware"},
	"adapter:family-derivatives":         {SessionMutationLeaseProven, "snapshot save, delete, event append, and tool-call record adapters update metadata indexes and sidecars inside the same backend call"},
	"SettleIfStale":                      {SessionMutationLeaseOwned, "stale repair owns run-entry and the session mutation lease before abandonment save"},
	"migrateOneFamily":                   {SessionMutationLeaseOwned, "migration owns run-entry and a mandatory maintenance mutation lease through family rewrite"},
	"driveSessionMigration":              {SessionMutationLeaseOwned, "job checkpoints are job-lease-owned and each family rewrite delegates to migrateOneFamily"},
	"CancelSessionMigration":             {SessionMutationLeaseOwned, "migration job cancellation is protected by the backend job-scoped acquisition"},
	"ApplySessionCleanup":                {SessionMutationLeaseOwned, "each family deletion delegates to the run-entry and maintenance-lease retention path"},
	"deleteAbandonedMembers":             {SessionMutationLeaseProven, "team creation acquires every member lease before publication and keeps it through abandoned-family cleanup"},
	"repairRunningAtRunEntry":            {SessionMutationLeaseProven, "run-entry has acquired the session lease before crash-orphan repair save"},
	"GetSession":                         {SessionMutationReadOnly, "loads and authorizes an authoritative snapshot without changing the durable family"},
	"GetTranscript":                      {SessionMutationReadOnly, "projects an authorized snapshot without changing the durable family"},
	"ListSessions":                       {SessionMutationReadOnly, "reads derivative metadata without changing a session family"},
	"SetModels":                          {SessionMutationComposition, "changes only process-wide composition state and touches no durable session family"},
	"SetModelsRefresher":                 {SessionMutationComposition, "installs a process-wide callback and touches no durable session family"},
	"SetSessionEnvironment":              {SessionMutationComposition, "registers only a process-local environment override and touches no durable session family"},
	"CloseSession":                       {SessionMutationLeaseProven, "tears down process-local session ownership and releases its lease without changing durable session bytes"},
}

func validateSessionMutationNames(table map[string]SessionMutationEntry, boundaries []string) []error {
	seen := make(map[string]struct{}, len(boundaries))
	var errs []error
	for _, name := range boundaries {
		seen[name] = struct{}{}
		entry, ok := table[name]
		if !ok {
			errs = append(errs, fmt.Errorf("unclassified session mutator %q (ADR 0293)", name))
			continue
		}
		if strings.TrimSpace(entry.Rationale) == "" {
			errs = append(errs, fmt.Errorf("session mutation %q has no rationale", name))
		}
		if !entry.mutatesDurableFamily() {
			errs = append(errs, fmt.Errorf("discovered session mutator %q uses non-mutating class %d", name, entry.Class))
		}
	}
	// Only mutation rows are source-discovered. Explicit read-only/composition
	// rows and outer orchestration rows are audit documentation; direct mutation
	// calls inside a newly added function are what make the guard fail.
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}
