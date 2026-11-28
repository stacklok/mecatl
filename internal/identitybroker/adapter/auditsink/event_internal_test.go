package auditsink

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	ib "github.com/stacklok/mecatl/internal/identitybroker"
)

func decisionForTest() *ib.DecisionRecord {
	return &ib.DecisionRecord{
		RecordHeader: ib.RecordHeader{AuditID: "rec_d", Type: ib.TypeToolDecision, At: time.Now(), Outcome: ib.OutcomeSuccess},
		SessionID:    "bs_1", Owner: ib.Owner{Issuer: "https://idp.example.com", Subject: "alice"}, OwnerVerified: true,
		UserRecord: "rec_open", User: ib.User{Issuer: "https://as1.example.com", Subject: "u-alice"},
		Method: "tools/call", Server: "github", Tool: "pull_request_read",
	}
}

// TestASubjectOrTargetKeyOutsideTheTypesListIsRefused plants a key the
// profile does not allow, for subjects and for the target.
func TestASubjectOrTargetKeyOutsideTheTypesListIsRefused(t *testing.T) {
	rec := decisionForTest()
	narrow := profiles[ib.TypeToolDecision]
	narrow.subjects = slices.DeleteFunc(slices.Clone(narrow.subjects), func(k string) bool { return k == ib.SubjectRunOwnerID })
	if _, _, err := subjectsOf(rec, narrow); !errors.Is(err, errProfile) {
		t.Fatalf("subjectsOf with run_owner_id not allowed = %v, want refused", err)
	}
	if _, _, err := subjectsOf(rec, profiles[ib.TypeToolDecision]); err != nil {
		t.Fatalf("subjectsOf with the type's own list = %v", err)
	}
	target := map[string]string{"type": "tool", "uri": "file:///etc/passwd"}
	if err := checkKeys(ib.TypeToolDecision, "target", maps.Keys(target), profiles[ib.TypeToolDecision].target); !errors.Is(err, errProfile) {
		t.Fatalf("checkKeys with uri = %v, want refused", err)
	}
}

// TestAnUnverifiedUserIDIsRefused pins that the sink fails closed on a
// user_id not marked verified or taken from a record.
func TestAnUnverifiedUserIDIsRefused(t *testing.T) {
	for p, refused := range map[ib.SubjectProvenance]bool{
		ib.ProvenanceVerified: false, "record:rec_open": false,
		ib.ProvenanceReportedByMecak8s: true, ib.ProvenanceCredentialVerified: true,
	} {
		if got := !verifiedUser(p); got != refused {
			t.Errorf("verifiedUser(%q) refused = %v, want %v", p, got, refused)
		}
	}
}

// TestARecordWithoutItsIDOrTimeIsRefused pins the profile's AuditID and
// occurrence time as required.
func TestARecordWithoutItsIDOrTimeIsRefused(t *testing.T) {
	noID := decisionForTest()
	noID.AuditID = ""
	noTime := decisionForTest()
	noTime.At = time.Time{}
	for name, rec := range map[string]*ib.DecisionRecord{"no AuditID": noID, "no time": noTime} {
		if _, err := Event(rec, Origin{}); !errors.Is(err, errProfile) {
			t.Errorf("%s: Event = %v, want refused", name, err)
		}
	}
	ev, err := Event(decisionForTest(), Origin{})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Source.Value != sourceInProcess || ev.Source.Extra[sourceExtraSessionID] != "bs_1" || ev.Metadata.Extra[ExtraReplica] != unavailable {
		t.Errorf("source %+v extra %v", ev.Source, ev.Metadata.Extra)
	}
}
