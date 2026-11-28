package auditsink

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/audit"

	ib "github.com/stacklok/mecatl/internal/identitybroker"
)

// The Metadata.Extra keys of the profile.
const (
	ExtraProfile     = "mecatl.profile"
	ExtraProvenance  = "mecatl.provenance"
	ExtraOccurredAt  = "mecatl.occurred_at"
	ExtraClock       = "mecatl.clock"
	ExtraOperationID = "mecatl.operation_id"
	ExtraAttemptID   = "mecatl.attempt_id"
	ExtraSupersedes  = "mecatl.supersedes"
	ExtraReplica     = "mecatl.replica"
	ExtraDeployment  = "mecatl.deployment"
	ExtraTrustDomain = "mecatl.trust_domain"
)

// unavailable is the value of an origin fact the sink was not given.
const unavailable = "unavailable"

// sourceInProcess is the Source value of every broker event: the broker is
// called in process, so no network origin exists. Who called is a subject,
// never the source.
const sourceInProcess = "in-process"

// sourceExtraSessionID is the Source extra key of the broker session, for
// joining records, as ToolHive's session_id.
const sourceExtraSessionID = "session_id"

// The Subjects keys each record type may carry.
var (
	userSubjects = []string{
		ib.SubjectOwnerIssuer, ib.SubjectOwnerID, ib.SubjectUserIssuer, ib.SubjectUserID,
		ib.SubjectUserClientID, ib.SubjectUserClientIssuer, ib.SubjectWorkloadIssuer, ib.SubjectWorkloadSubject,
	}
	agentSubjects    = append(slices.Clone(userSubjects), ib.SubjectAgentSPIFFEID, ib.SubjectAgentActorID, ib.SubjectAgentInstanceRef)
	runOwnerSubjects = append(slices.Clone(agentSubjects), ib.SubjectRunOwnerIssuer, ib.SubjectRunOwnerID)
)

// The outcome sets of audit.md's table: a lifecycle or decision event is a
// success, denied, or error; a close only ever succeeds.
var (
	decisionOutcomes = []ib.DecisionOutcome{ib.OutcomeSuccess, ib.OutcomeDenied, ib.OutcomeError}
	closeOutcomes    = []ib.DecisionOutcome{ib.OutcomeSuccess}
)

// profile is what the profile allows for one record type.
type profile struct {
	subjects []string
	outcomes []ib.DecisionOutcome
	// target keys; nil when the type has no target.
	target []string
	// version names the Data structure and its version.
	version string
}

// profiles are the profile of each record type, by Type.
var profiles = map[string]profile{
	ib.TypeSessionOpen:        {subjects: userSubjects, outcomes: decisionOutcomes, version: ib.TypeSessionOpen + "/1"},
	ib.TypeSessionClose:       {subjects: userSubjects, outcomes: closeOutcomes, version: ib.TypeSessionClose + "/1"},
	ib.TypeInstanceRegistered: {subjects: agentSubjects, outcomes: decisionOutcomes, version: ib.TypeInstanceRegistered + "/1"},
	ib.TypeDelegation:         {subjects: runOwnerSubjects, outcomes: decisionOutcomes, version: ib.TypeDelegation + "/1"},
	ib.TypeIDJAG:              {subjects: runOwnerSubjects, outcomes: decisionOutcomes, version: ib.TypeIDJAG + "/1"},
	ib.TypeToolDecision: {
		subjects: runOwnerSubjects, outcomes: decisionOutcomes, version: ib.TypeToolDecision + "/1",
		target: []string{audit.TargetKeyType, audit.TargetKeyName, audit.TargetKeyMethod, targetKeyServer},
	},
}

// targetKeyServer names the MCP server of a tool target.
const targetKeyServer = "server"

// errProfile is the error of a record that does not fit the profile.
var errProfile = errors.New("record does not fit the AuditEvent profile")

// Event returns rec as an AuditEvent in the profile, recorded at origin, or
// an error when rec breaks it: an unknown type, a missing AuditID or time,
// an outcome outside its type's set, or a subject or target key its type
// does not allow.
func Event(rec ib.Record, origin Origin) (*audit.AuditEvent, error) {
	h := rec.Header()
	p, ok := profiles[h.Type]
	if !ok {
		return nil, fmt.Errorf("%w: unknown type %q", errProfile, h.Type)
	}
	if h.AuditID == "" || h.At.IsZero() {
		return nil, fmt.Errorf("%w: %s record without an AuditID or time", errProfile, h.Type)
	}
	if !slices.Contains(p.outcomes, h.Outcome) {
		return nil, fmt.Errorf("%w: outcome %q is not one of %s's", errProfile, h.Outcome, h.Type)
	}
	subjects, provenance, err := subjectsOf(rec, p)
	if err != nil {
		return nil, err
	}
	d, err := dataOf(rec, p.version)
	if err != nil {
		return nil, err
	}
	if err := checkKeys(h.Type, "target", maps.Keys(d.target), p.target); err != nil {
		return nil, err
	}
	raw := json.RawMessage(d.json)
	source := audit.EventSource{Type: audit.SourceTypeLocal, Value: sourceInProcess}
	if d.sessionID != "" {
		source.Extra = map[string]any{sourceExtraSessionID: d.sessionID}
	}
	ev := audit.NewAuditEventWithID(h.AuditID, h.Type, source, string(h.Outcome), subjects, Component)
	// LoggedAt is when the broker made the record, so a re-send keeps it.
	ev.LoggedAt = h.At.UTC()
	ev.Metadata.Extra = map[string]any{
		ExtraProfile:     Profile,
		ExtraProvenance:  provenance,
		ExtraOccurredAt:  h.At.Format(time.RFC3339Nano),
		ExtraClock:       Component,
		ExtraReplica:     cmp.Or(origin.Replica, unavailable),
		ExtraDeployment:  cmp.Or(origin.Deployment, unavailable),
		ExtraTrustDomain: cmp.Or(origin.TrustDomain, unavailable),
	}
	if h.Supersedes != "" {
		ev.Metadata.Extra[ExtraSupersedes] = h.Supersedes
	}
	if d.operationID != "" {
		ev.Metadata.Extra[ExtraOperationID] = d.operationID
	}
	if d.attemptID != "" {
		ev.Metadata.Extra[ExtraAttemptID] = d.attemptID
	}
	if d.target != nil {
		ev.WithTarget(d.target)
	}
	ev.WithData(&raw)
	return ev, nil
}

// subjectsOf returns rec's subjects with a value, and the provenance of
// every subject, an unavailable one included, checking each key against p.
// A user_id the broker did not verify, directly or from a retained record,
// is refused: an unverified identity never appears as user_id.
func subjectsOf(rec ib.Record, p profile) (map[string]string, map[string]string, error) {
	typ := rec.Header().Type
	all := rec.Subjects()
	if err := checkKeys(typ, "subject", maps.Keys(all), p.subjects); err != nil {
		return nil, nil, err
	}
	subjects := map[string]string{}
	provenance := map[string]string{}
	for k, s := range all {
		provenance[k] = string(s.Provenance)
		if s.Value != "" {
			subjects[k] = s.Value
		}
	}
	if user, ok := subjects[ib.SubjectUserID]; ok && user != "" && !verifiedUser(all[ib.SubjectUserID].Provenance) {
		return nil, nil, fmt.Errorf("%w: %s user_id marked %q", errProfile, typ, all[ib.SubjectUserID].Provenance)
	}
	return subjects, provenance, nil
}

// verifiedUser reports whether p marks a user the broker verified, itself
// or in the retained record it names.
func verifiedUser(p ib.SubjectProvenance) bool {
	return p == ib.ProvenanceVerified || strings.HasPrefix(string(p), "record:")
}

// checkKeys refuses a key of typ's kind (subject or target) that allowed
// does not hold.
func checkKeys(typ, kind string, keys iter.Seq[string], allowed []string) error {
	for k := range keys {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("%w: %s %s key %q", errProfile, typ, kind, k)
		}
	}
	return nil
}
