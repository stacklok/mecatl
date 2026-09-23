package session

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
)

func grantAuthoritySession(t *testing.T) *Session {
	t.Helper()
	s := New("grant-authority", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "workspace", Revision: "r1"}, Limits{MaxTurns: 9, MaxToolCalls: 8}, time.Unix(10, 0).UTC())
	s.Owner = &Principal{Issuer: "issuer", Subject: "owner", Name: "Owner", GrantType: GrantTypeUser}
	if err := s.BindAuthority(Authority{
		CapabilitySet: governance.CapabilitySet{
			Tools:                    []string{"legacy/tool name", "Read"},
			RemainingDelegationDepth: 3,
			FileSystem:               true,
			DirectWrite:              true,
		},
		Provenance:         "configured:test",
		DefinitionIdentity: "agent:test",
	}); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	s.Conversation.Append(NewUserMessage("preserve history"))
	s.Counters = Counters{Turns: 4, ToolCalls: 5}
	s.Usage = Usage{InputTokens: 6, OutputTokens: 7}
	return s
}

func TestADR_0351_GrantToolAuthorityPreservesAggregateState(t *testing.T) {
	t.Run("duplicate stable union and input cloned", func(t *testing.T) {
		s := grantAuthoritySession(t)
		before := *s
		input := []string{"Read", "mcp__one__first", "mcp__one__first", "mcp__two__工具"}
		if err := s.GrantToolAuthority(input); err != nil {
			t.Fatalf("GrantToolAuthority: %v", err)
		}
		input[1] = "mutated"
		got, bound := s.BoundAuthority()
		if !bound {
			t.Fatal("authority became unbound")
		}
		want := []string{"legacy/tool name", "Read", "mcp__one__first", "mcp__two__工具"}
		if !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
			t.Fatalf("tools = %q, want stable union %q", got.CapabilitySet.Tools, want)
		}
		if got.CapabilitySet.RemainingDelegationDepth != 3 || !got.CapabilitySet.FileSystem || !got.CapabilitySet.DirectWrite || got.Provenance != "configured:test" || got.DefinitionIdentity != "agent:test" {
			t.Fatalf("unrelated authority changed: %+v", got)
		}
		afterWithoutAuthority := *s
		afterWithoutAuthority.Authority = before.Authority
		if !reflect.DeepEqual(afterWithoutAuthority, before) {
			t.Fatalf("non-authority aggregate state changed\nbefore: %#v\nafter:  %#v", before, afterWithoutAuthority)
		}
	})

	t.Run("exact 256 byte boundary", func(t *testing.T) {
		s := grantAuthoritySession(t)
		exact := strings.Repeat("é", 128)
		if err := s.GrantToolAuthority([]string{exact}); err != nil {
			t.Fatalf("256-byte UTF-8 name rejected: %v", err)
		}
		got, _ := s.BoundAuthority()
		if !got.CapabilitySet.AllowsTool(exact) {
			t.Fatal("256-byte name was not granted")
		}
	})

	for _, tc := range []struct {
		name  string
		names []string
	}{
		{"empty", []string{"ok", ""}},
		{"invalid UTF-8", []string{"ok", string([]byte{0xff})}},
		{"ASCII control", []string{"ok", "bad\nname"}},
		{"Unicode control", []string{"ok", "bad\u0085name"}},
		{"over 256 bytes", []string{"ok", strings.Repeat("x", 257)}},
	} {
		t.Run("invalid batch atomic/"+tc.name, func(t *testing.T) {
			s := grantAuthoritySession(t)
			before := *s
			beforeAuthority := s.Authority.Clone()
			if err := s.GrantToolAuthority(tc.names); err == nil {
				t.Fatal("GrantToolAuthority accepted invalid batch")
			}
			if !reflect.DeepEqual(s.Authority, beforeAuthority) {
				t.Fatalf("rejected batch mutated authority: %+v", s.Authority)
			}
			if !reflect.DeepEqual(*s, before) {
				t.Fatal("rejected batch mutated aggregate")
			}
		})
	}

	t.Run("unbound rejected", func(t *testing.T) {
		s := New("unbound", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "workspace", Revision: "r1"}, Limits{}, time.Unix(10, 0).UTC())
		before := *s
		if err := s.GrantToolAuthority([]string{"new"}); err == nil {
			t.Fatal("GrantToolAuthority accepted an unbound session")
		}
		if !reflect.DeepEqual(*s, before) {
			t.Fatal("unbound rejection mutated aggregate")
		}
	})

	t.Run("completed remains completed", func(t *testing.T) {
		s := grantAuthoritySession(t)
		s.State = StateCompleted
		if err := s.GrantToolAuthority([]string{"new"}); err != nil {
			t.Fatalf("GrantToolAuthority from completed: %v", err)
		}
		if s.State != StateCompleted {
			t.Fatalf("state = %q, want completed", s.State)
		}
	})

	for _, state := range []State{StateRunning, StateAwaiting, StateAuthorizing, StateFailed, StateCancelled} {
		t.Run("state rejected/"+string(state), func(t *testing.T) {
			s := grantAuthoritySession(t)
			s.State = state
			before := s.Authority.Clone()
			if err := s.GrantToolAuthority([]string{"new"}); err == nil {
				t.Fatalf("GrantToolAuthority accepted state %q", state)
			}
			if s.State != state || !reflect.DeepEqual(s.Authority, before) {
				t.Fatal("state rejection mutated aggregate")
			}
		})
	}

	t.Run("pending control rejected in idle and completed", func(t *testing.T) {
		controls := []struct {
			name string
			set  func(*Session)
		}{
			{"permission", func(s *Session) { s.pending = &PendingAsk{AskID: "pending"} }},
			{"authorization", func(s *Session) { s.pendingAuthorization = &PendingAuthorization{} }},
			{"workspace enrollment", func(s *Session) { s.pendingWorkspaceEnrollment = &PendingWorkspaceEnrollment{ID: "pending"} }},
		}
		for _, control := range controls {
			for _, state := range []State{StateIdle, StateCompleted} {
				t.Run(control.name+"/"+string(state), func(t *testing.T) {
					s := grantAuthoritySession(t)
					s.State = state
					control.set(s)
					before := s.Authority.Clone()
					if err := s.GrantToolAuthority([]string{"new"}); err == nil {
						t.Fatalf("GrantToolAuthority accepted pending control in %q", state)
					}
					if s.State != state || !reflect.DeepEqual(s.Authority, before) {
						t.Fatal("pending-control rejection mutated aggregate")
					}
				})
			}
		}
	})
}
