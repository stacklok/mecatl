package auditsink

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// Source of every broker event: mecak8s calls the broker in process, so the
// origin is local.
var brokerSource = audit.EventSource{Type: audit.SourceTypeLocal, Value: "mecak8s"}

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
	raw := json.RawMessage(d.json)
	ev := audit.NewAuditEventWithID(h.AuditID, h.Type, brokerSource, string(h.Outcome), subjects, Component)
	// LoggedAt is when the broker made the record, so a re-send keeps it.
	ev.LoggedAt = h.At.UTC()
	ev.Metadata.Extra = map[string]any{
		ExtraProfile:     Profile,
		ExtraProvenance:  provenance,
		ExtraOccurredAt:  h.At.Format(time.RFC3339Nano),
		ExtraClock:       Component,
		ExtraReplica:     orUnavailable(origin.Replica),
		ExtraDeployment:  orUnavailable(origin.Deployment),
		ExtraTrustDomain: orUnavailable(origin.TrustDomain),
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
		for k := range d.target {
			if !slices.Contains(p.target, k) {
				return nil, fmt.Errorf("%w: %s target key %q", errProfile, h.Type, k)
			}
		}
		ev.WithTarget(d.target)
	}
	ev.WithData(&raw)
	return ev, nil
}

// subjectsOf returns rec's subjects with a value, and the provenance of
// every subject, an unavailable one included, checking each key against p.
func subjectsOf(rec ib.Record, p profile) (map[string]string, map[string]string, error) {
	subjects := map[string]string{}
	provenance := map[string]string{}
	for k, s := range rec.Subjects() {
		if !slices.Contains(p.subjects, k) {
			return nil, nil, fmt.Errorf("%w: %s subject key %q", errProfile, rec.Header().Type, k)
		}
		provenance[k] = string(s.Provenance)
		if s.Value != "" {
			subjects[k] = s.Value
		}
	}
	return subjects, provenance, nil
}

func orUnavailable(s string) string {
	if s == "" {
		return unavailable
	}
	return s
}
