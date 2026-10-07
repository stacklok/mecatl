package sessnap_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBrokerAuthorityCarriedSnapshotWithoutAccess(t *testing.T) {
	s := session.New("carried", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "test", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	scope := []string{}
	if err := s.BindAuthority(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "carried", BrokerToolScope: &scope}); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []bool{false, true} {
		snap, err := sessnap.Of(s)
		if err != nil {
			t.Fatal(err)
		}
		if malformed {
			*snap.Authority.BrokerToolScope = []string{"Read", "Read"}
		}
		encoded, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var decoded sessnap.Snapshot
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		restored, restoreErr := decoded.Restore()
		folded, foldErr := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Incarnation: s.Incarnation(), Authority: decoded.Authority, CreatedAt: s.CreatedAt}, func(func(session.Event, error) bool) {})
		if malformed {
			if restoreErr == nil || foldErr == nil {
				t.Fatal("malformed carried scope restored without broker access")
			}
			continue
		}
		if restoreErr != nil || foldErr != nil {
			t.Fatalf("carried restore: %v %v", restoreErr, foldErr)
		}
		for _, got := range []*session.Session{restored, folded} {
			a, _ := got.BoundAuthority()
			if a.BrokerToolScope == nil || *a.BrokerToolScope == nil || len(*a.BrokerToolScope) != 0 {
				t.Fatal("finite empty scope became root discovery")
			}
			if _, ok := got.BrokerAccess(); ok {
				t.Fatal("carried restore acquired broker refs")
			}
		}
	}
}

func TestBrokerAuthoritySnapshotAndFold(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		for _, scoped := range []bool{false, true} {
			t.Run("roundtrip", func(t *testing.T) {
				s := session.New("broker", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "test", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
				authority := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
				if scoped {
					scope := []string{}
					authority.BrokerToolScope = &scope
				}
				if err := s.BindAuthority(authority); err != nil {
					t.Fatal(err)
				}
				ref := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				if err := s.AdoptBrokerCatalogue(session.BrokerSessionRef(ref), session.BrokerCatalogueRef(ref), session.BrokerConnectionRef(ref), time.Unix(100, 0), []string{"mcp__test__x"}); err != nil {
					t.Fatal(err)
				}
				access, _ := s.BrokerAccess()
				access.Current = &session.BrokerHostAttempt{Attempt: session.NewBrokerAttempt(), CallID: "call", Digest: sha256.Sum256([]byte("prepared call"))}
				if err := s.RestoreBrokerAccess(access); err != nil {
					t.Fatal(err)
				}
				if withdrawn {
					if err := s.WithdrawBrokerAccess(); err != nil {
						t.Fatal(err)
					}
				}
				snap, err := sessnap.Of(s)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(snap)
				if err != nil {
					t.Fatal(err)
				}
				if scoped && !strings.Contains(string(encoded), `"broker_tool_scope":[]`) {
					t.Fatalf("empty scope lost: %s", encoded)
				}
				var decoded sessnap.Snapshot
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				restored, err := decoded.Restore()
				if err != nil {
					t.Fatal(err)
				}
				meta := eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Incarnation: s.Incarnation(), Authority: snap.Authority, BrokerAccess: snap.BrokerAccess, CreatedAt: s.CreatedAt}
				folded, err := eventsource.Fold(meta, func(func(session.Event, error) bool) {})
				if err != nil {
					t.Fatal(err)
				}
				want, _ := s.BrokerAccess()
				wantAuthority, _ := s.BoundAuthority()
				for _, got := range []*session.Session{restored, folded} {
					access, _ := got.BrokerAccess()
					bound, _ := got.BoundAuthority()
					if !reflect.DeepEqual(want, access) || !reflect.DeepEqual(wantAuthority, bound) {
						t.Fatalf("restore drift: %+v %+v", access, bound)
					}
				}
				snap.BrokerAccess.IndependentTools[0] = "mutated"
				decoded.BrokerAccess.IndependentTools[0] = "mutated"
				if scoped {
					*snap.Authority.BrokerToolScope = append(*snap.Authority.BrokerToolScope, "mutated")
				}
				for _, got := range []*session.Session{s, restored, folded} {
					access, _ := got.BrokerAccess()
					bound, _ := got.BoundAuthority()
					if !reflect.DeepEqual(want, access) || !reflect.DeepEqual(wantAuthority, bound) {
						t.Fatal("snapshot/fold alias")
					}
				}
			})
		}
	}
}

func TestBrokerAuthorityPersistenceFailsClosed(t *testing.T) {
	s := session.New("broker", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "test", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	scope := []string{"mcp__test__x"}
	if err := s.BindAuthority(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test", BrokerToolScope: &scope}); err != nil {
		t.Fatal(err)
	}
	ref := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err := s.AdoptBrokerCatalogue(session.BrokerSessionRef(ref), session.BrokerCatalogueRef(ref), session.BrokerConnectionRef(ref), time.Unix(100, 0), scope); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*sessnap.Snapshot)
	}{
		{"missing connection", func(s *sessnap.Snapshot) { s.BrokerAccess.Connection = "" }},
		{"malformed connection", func(s *sessnap.Snapshot) { s.BrokerAccess.Connection = "invalid" }},
		{"missing independent", func(s *sessnap.Snapshot) { s.BrokerAccess.IndependentTools = nil }},
		{"missing broker", func(s *sessnap.Snapshot) { s.BrokerAccess.BrokerTools = nil }},
		{"duplicate scope", func(s *sessnap.Snapshot) {
			*s.Authority.BrokerToolScope = append(*s.Authority.BrokerToolScope, "mcp__test__x")
		}},
		{"invalid scope", func(s *sessnap.Snapshot) { *s.Authority.BrokerToolScope = []string{"bad\nname"} }},
		{"out of scope", func(s *sessnap.Snapshot) { *s.Authority.BrokerToolScope = []string{} }},
		{"projection mismatch", func(s *sessnap.Snapshot) { s.Authority.CapabilitySet.Tools = []string{"Read"} }},
		{"withdrawn contribution", func(s *sessnap.Snapshot) { s.BrokerAccess.Withdrawn = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := sessnap.Of(s)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&snap)
			if _, err := snap.Restore(); err == nil {
				t.Fatal("snapshot accepted")
			}
			meta := eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Incarnation: s.Incarnation(), Authority: snap.Authority, BrokerAccess: snap.BrokerAccess, CreatedAt: s.CreatedAt}
			if _, err := eventsource.Fold(meta, func(func(session.Event, error) bool) {}); err == nil {
				t.Fatal("fold accepted")
			}
		})
	}
}
