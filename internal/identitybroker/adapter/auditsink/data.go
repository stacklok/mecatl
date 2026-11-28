package auditsink

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/toolhive-core/audit"

	ib "github.com/stacklok/mecatl/internal/identitybroker"
)

// eventData is what a record contributes beyond its header and subjects.
type eventData struct {
	json        []byte
	target      map[string]string
	sessionID   string
	operationID string
	attemptID   string
}

// The Data structures, one per record type. Each names its version, and
// holds only references: credentials by (iss, jti), records by AuditID,
// never a credential, argument, or result value (audit.md, "Redaction").

// common is what every Data structure starts with: its version, and a
// refusal's code and reason.
type common struct {
	Version string `json:"version"`
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// credentialRef is a credential's (iss, jti); nil when there is none.
type credentialRef struct {
	Issuer string `json:"iss"`
	ID     string `json:"jti"`
}

func credRef(r ib.CredentialRef) *credentialRef {
	if r == (ib.CredentialRef{}) {
		return nil
	}
	return &credentialRef{Issuer: r.Issuer, ID: r.ID}
}

// definition is a definition reference.
type definition struct {
	Source   string `json:"source,omitempty"`
	Name     string `json:"name,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// callLineage is Data.lineage of a per-call event: the run, and references
// to the delegation event it runs under and to the acting instance's
// registration event (audit.md, "Creation and delegation").
type callLineage struct {
	Run                string `json:"run,omitempty"`
	Delegation         string `json:"delegation,omitempty"`
	DelegationRecord   string `json:"delegation_record,omitempty"`
	RegistrationRecord string `json:"registration_record,omitempty"`
}

type sessionOpenData struct {
	common
	SessionID      string           `json:"session_id,omitempty"`
	HostSessionID  string           `json:"host_session_id,omitempty"`
	Incarnation    string           `json:"incarnation,omitempty"`
	NewSession     bool             `json:"new_session"`
	SessionRecord  string           `json:"session_record,omitempty"`
	Kind           string           `json:"kind,omitempty"`
	TokenPresented bool             `json:"token_presented"`
	SourceJTI      string           `json:"source_jti,omitempty"`
	HardExpiry     *time.Time       `json:"hard_expiry,omitempty"`
	Replaced       *replacedGrantJS `json:"replaced,omitempty"`
}

type replacedGrantJS struct {
	OpenRecord string `json:"open_record"`
	SourceJTI  string `json:"source_jti,omitempty"`
}

type sessionCloseData struct {
	common
	SessionID     string `json:"session_id"`
	HostSessionID string `json:"host_session_id,omitempty"`
	ClosingReason string `json:"closing_reason,omitempty"`
	Cause         string `json:"cause"`
	SessionRecord string `json:"session_record,omitempty"`
}

// registrationLineage is a registration's place in the creation tree, as
// mecak8s reported it.
type registrationLineage struct {
	Provenance          string `json:"provenance"`
	SessionRecord       string `json:"session_record,omitempty"`
	HostSessionID       string `json:"host_session_id,omitempty"`
	Parent              string `json:"parent_spiffe_id,omitempty"`
	ParentHostSessionID string `json:"parent_host_session_id,omitempty"`
	ParentRecord        string `json:"parent_record,omitempty"`
	PreviousRecord      string `json:"previous_record,omitempty"`
}

type registrationData struct {
	common
	SessionID           string              `json:"session_id,omitempty"`
	Definition          *definition         `json:"definition,omitempty"`
	New                 bool                `json:"new"`
	Moved               bool                `json:"moved"`
	CredentialExpiresAt *time.Time          `json:"credential_expires_at,omitempty"`
	Lineage             registrationLineage `json:"lineage"`
}

// delegationParty is one side of a delegation, as mecak8s reported it.
type delegationParty struct {
	Run          string `json:"run,omitempty"`
	SPIFFEID     string `json:"agent_spiffe_id,omitempty"`
	ActorID      string `json:"agent_actor_id,omitempty"`
	Instance     string `json:"agent_instance_ref,omitempty"`
	Registration string `json:"registration_record,omitempty"`
}

type delegationLineage struct {
	Provenance             string          `json:"provenance"`
	Delegation             string          `json:"delegation,omitempty"`
	Delegating             delegationParty `json:"delegating"`
	DelegatingRunRecorded  bool            `json:"delegating_run_recorded"`
	Receiving              delegationParty `json:"receiving"`
	ParentDelegationRecord string          `json:"parent_delegation_record,omitempty"`
	PreviousRecord         string          `json:"previous_record,omitempty"`
}

type delegationData struct {
	common
	SessionID string            `json:"session_id,omitempty"`
	Lineage   delegationLineage `json:"lineage"`
}

type idjagData struct {
	common
	SessionID string         `json:"session_id,omitempty"`
	IDJAG     *credentialRef `json:"id_jag,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Lineage   callLineage    `json:"lineage"`
}

type decisionData struct {
	common
	SessionID         string `json:"session_id,omitempty"`
	RejectedAttemptID string `json:"rejected_attempt_id,omitempty"`
	ArgumentsSHA256   string `json:"arguments_sha256,omitempty"`
	// CeilingEvaluated reports that the call reached the ceiling check;
	// Ceiling is the effective ceiling it was checked against.
	CeilingEvaluated         bool           `json:"ceiling_evaluated"`
	Ceiling                  string         `json:"ceiling,omitempty"`
	IDJAG                    *credentialRef `json:"id_jag,omitempty"`
	GatewayToken             *credentialRef `json:"gateway_token,omitempty"`
	GatewayTokenUnreferenced bool           `json:"gateway_token_unreferenced,omitempty"`
	CallEntryCheck           string         `json:"call_entry_check,omitempty"`
	Lineage                  callLineage    `json:"lineage"`
}

// dataOf returns rec's Data, version version, and its target and
// correlation IDs.
func dataOf(rec ib.Record, version string) (eventData, error) {
	h := rec.Header()
	c := common{Version: version, Code: string(h.Code), Reason: string(h.Reason)}
	var d eventData
	var v any
	switch r := rec.(type) {
	case *ib.SessionOpenRecord:
		od := sessionOpenData{
			common: c, SessionID: r.SessionID, HostSessionID: r.HostSessionID, Incarnation: r.Incarnation,
			NewSession: r.NewSession, SessionRecord: r.SessionRecord, Kind: string(r.Kind),
			TokenPresented: r.TokenPresented, SourceJTI: r.SourceJTI, HardExpiry: timeRef(r.HardExpiry),
		}
		if r.Replaced != nil {
			od.Replaced = &replacedGrantJS{OpenRecord: r.Replaced.OpenRecord, SourceJTI: r.Replaced.SourceJTI}
		}
		v = od
	case *ib.SessionCloseRecord:
		v = sessionCloseData{
			common: c, SessionID: r.SessionID, HostSessionID: r.HostSessionID, ClosingReason: string(r.ClosingReason),
			Cause: string(r.Cause), SessionRecord: r.SessionRecord,
		}
	case *ib.RegistrationRecord:
		v = registrationData{
			common: c, SessionID: r.SessionID, Definition: definitionRef(r.Definition), New: r.New, Moved: r.Moved,
			CredentialExpiresAt: timeRef(r.CredentialExpiresAt),
			Lineage: registrationLineage{
				Provenance: string(r.LineageProvenance), SessionRecord: r.SessionRecord, HostSessionID: r.HostSessionID,
				Parent: r.Parent.Subject, ParentHostSessionID: r.ParentHostSessionID, ParentRecord: r.ParentRecord,
				PreviousRecord: r.PreviousRecord,
			},
		}
	case *ib.DelegationRecord:
		v = delegationData{common: c, SessionID: r.SessionID, Lineage: delegationLineage{
			Provenance: string(r.LineageProvenance), Delegation: r.DelegationID,
			Delegating: delegationParty{
				Run: r.DelegatingRun, SPIFFEID: r.DelegatingAgent.Subject, ActorID: r.DelegatingAgentID,
				Instance: r.DelegatingInstance, Registration: r.DelegatingRecord,
			},
			DelegatingRunRecorded: r.DelegatingRunRecorded,
			Receiving: delegationParty{
				Run: r.ReceivingRun, SPIFFEID: r.ReceivingAgent.Subject, ActorID: r.ReceivingAgentID,
				Registration: r.ReceivingRecord,
			},
			ParentDelegationRecord: r.ParentDelegationRecord, PreviousRecord: r.PreviousRecord,
		}}
	case *ib.IDJAGRecord:
		d.operationID, d.attemptID = r.OperationID, r.AttemptID
		v = idjagData{
			common: c, SessionID: r.SessionID, IDJAG: credRef(r.IDJAG), ExpiresAt: timeRef(r.ExpiresAt),
			Lineage: callLineage{Run: r.RunID, Delegation: r.DelegationID, DelegationRecord: r.DelegationRecord, RegistrationRecord: r.RegistrationRecord},
		}
	case *ib.DecisionRecord:
		d.operationID, d.attemptID = r.OperationID, r.AttemptID
		v = decisionData{
			common: c, SessionID: r.SessionID, RejectedAttemptID: r.RejectedAttemptID, ArgumentsSHA256: r.ArgumentsSHA256,
			CeilingEvaluated: r.Ceiling != "", Ceiling: r.Ceiling, IDJAG: credRef(r.IDJAG), GatewayToken: credRef(r.GatewayToken),
			GatewayTokenUnreferenced: r.GatewayTokenUnreferenced, CallEntryCheck: string(r.CallEntryCheck),
			Lineage: callLineage{Run: r.RunID, Delegation: r.DelegationID, DelegationRecord: r.DelegationRecord, RegistrationRecord: r.RegistrationRecord},
		}
		d.target = toolTarget(r.Method, r.Server, r.Tool)
	default:
		return eventData{}, fmt.Errorf("%w: no Data structure for %T", errProfile, rec)
	}
	d.sessionID = sessionOf(rec)
	b, err := json.Marshal(v)
	if err != nil {
		return eventData{}, fmt.Errorf("%w: %w", errProfile, err)
	}
	d.json = b
	return d, nil
}

// sessionOf returns the broker session rec names; empty for a refusal
// before a session was known.
func sessionOf(rec ib.Record) string {
	switch r := rec.(type) {
	case *ib.SessionOpenRecord:
		return r.SessionID
	case *ib.SessionCloseRecord:
		return r.SessionID
	case *ib.RegistrationRecord:
		return r.SessionID
	case *ib.DelegationRecord:
		return r.SessionID
	case *ib.IDJAGRecord:
		return r.SessionID
	case *ib.DecisionRecord:
		return r.SessionID
	}
	return ""
}

// toolTarget returns a call's target in toolhive-core's keys: the tool, or
// the server for a tool listing, leaving out what a refusal before
// validation did not record.
func toolTarget(method, server, tool string) map[string]string {
	t := map[string]string{audit.TargetKeyType: audit.TargetTypeTool}
	if method == string(ib.MethodToolsList) {
		t[audit.TargetKeyType] = audit.TargetTypeServer
	}
	if method != "" {
		t[audit.TargetKeyMethod] = method
	}
	if tool != "" {
		t[audit.TargetKeyName] = tool
	}
	if server != "" {
		t[targetKeyServer] = server
	}
	return t
}

func definitionRef(r ib.DefinitionRef) *definition {
	if r == (ib.DefinitionRef{}) {
		return nil
	}
	return &definition{Source: r.Source, Name: r.Name, Revision: r.Revision}
}

func timeRef(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
