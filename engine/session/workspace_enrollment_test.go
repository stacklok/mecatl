package session

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
)

func newEnrollmentSession(t *testing.T) *Session {
	t.Helper()
	s := New("enrollment-session", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(Authority{Provenance: "test"}); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	return s
}

func testWorkspaceEnrollment() PendingWorkspaceEnrollment {
	return PendingWorkspaceEnrollment{
		ID:               WorkspaceEnrollmentID("enroll_123"),
		RequiredServices: 3,
		ExpiresAt:        time.Unix(100, 0).UTC(),
	}
}

func TestWorkspaceEnrollmentLifecycleIsIndependent(t *testing.T) {
	s := newEnrollmentSession(t)
	want := testWorkspaceEnrollment()
	input := want
	if err := s.BeginWorkspaceEnrollment(input); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	input.ID = "input-mutated"
	if s.State != StateIdle {
		t.Fatalf("State = %q, want idle", s.State)
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatal("PendingAsk unexpectedly changed")
	}
	if _, ok := s.PendingAuthorization(); ok {
		t.Fatal("PendingAuthorization unexpectedly changed")
	}

	got, ok := s.PendingWorkspaceEnrollment()
	if !ok || got != want {
		t.Fatalf("PendingWorkspaceEnrollment = %+v, %v; want %+v, true", got, ok, want)
	}
	got.ID = "mutated"
	again, _ := s.PendingWorkspaceEnrollment()
	if again != want {
		t.Fatalf("accessor mutation changed aggregate: %+v", again)
	}

	if err := s.AbortWorkspaceEnrollment("other"); err == nil {
		t.Fatal("AbortWorkspaceEnrollment accepted mismatched ID")
	}
	if again, ok = s.PendingWorkspaceEnrollment(); !ok || again != want {
		t.Fatalf("mismatched abort changed pending enrollment: %+v, %v", again, ok)
	}
	if err := s.AbortWorkspaceEnrollment(want.ID); err != nil {
		t.Fatalf("AbortWorkspaceEnrollment: %v", err)
	}
	if _, ok := s.PendingWorkspaceEnrollment(); ok {
		t.Fatal("enrollment remains after exact-ID abort")
	}
	if s.State != StateIdle {
		t.Fatalf("abort changed State to %q", s.State)
	}
}

func TestCompleteWorkspaceEnrollmentWithBindingAdoptsFreshBrokerBinding(t *testing.T) {
	s := newEnrollmentSession(t)
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollmentWithBinding(pending, ExternalBinding("b2-binding"), []string{"mcp__calendar__list"}); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollmentWithBinding: %v", err)
	}
	if s.ExternalBinding != ExternalBinding("b2-binding") {
		t.Fatalf("ExternalBinding = %q", s.ExternalBinding)
	}
	if _, ok := s.PendingWorkspaceEnrollment(); ok {
		t.Fatal("pending enrollment remains")
	}
	if len(s.Authority.CapabilitySet.Tools) != 1 || s.Authority.CapabilitySet.Tools[0] != "mcp__calendar__list" {
		t.Fatalf("authority tools = %#v", s.Authority.CapabilitySet.Tools)
	}
}

func TestPendingWorkspaceEnrollmentRejectsHistoryAndLifecycleMutation(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Session) error
	}{
		{"RecordUserPromptWithParts", func(s *Session) error { return s.RecordUserPromptWithParts("prompt", nil, nil) }},
		{"RecordAssistant", func(s *Session) error { return s.RecordAssistant(NewAssistantMessage("answer", "", nil)) }},
		{"RecordToolResults", func(s *Session) error { return s.RecordToolResults([]ToolResult{NewToolResult("call", "result")}) }},
		{"BeginTurn", func(s *Session) error { return s.BeginTurn() }},
		{"SeedHistory", func(s *Session) error { return s.SeedHistory([]Message{NewUserMessage("seed")}) }},
		{"ReplaceHistory", func(s *Session) error { return s.ReplaceHistory([]Message{NewUserMessage("replacement")}) }},
		{"ReplaceHistoryAtBoundary", func(s *Session) error { return s.ReplaceHistoryAtBoundary([]Message{NewUserMessage("replacement")}) }},
		{"PauseForApproval", func(s *Session) error { return s.PauseForApproval(PendingAsk{AskID: "ask"}) }},
		{"PauseForAuthorization", func(s *Session) error { return s.PauseForAuthorization(PendingAuthorization{}) }},
		{"Complete", func(s *Session) error { return s.Complete() }},
		{"Stop", func(s *Session) error { return s.Stop(StopBudget) }},
		{"Cancel", func(s *Session) error { return s.Cancel() }},
		{"Fail", func(s *Session) error { return s.Fail() }},
		{"Reopen", func(s *Session) error { return s.Reopen() }},
		{"Interrupt", func(s *Session) error { return s.Interrupt() }},
		{"Recover", func(s *Session) error { return s.Recover() }},
		{"Abandon", func(s *Session) error { return s.Abandon() }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			pending := testWorkspaceEnrollment()
			mustOK(t, s.BeginWorkspaceEnrollment(pending))
			state, counters := s.State, s.Counters
			if err := operation.run(s); err == nil {
				t.Fatalf("%s succeeded while workspace enrollment was pending", operation.name)
			}
			if s.State != state || s.Counters != counters || len(s.Conversation.Messages) != 0 {
				t.Fatalf("rejected %s mutated aggregate: state=%q counters=%+v messages=%d", operation.name, s.State, s.Counters, len(s.Conversation.Messages))
			}
			if _, ok := s.RecordedStopReason(); ok {
				t.Fatalf("rejected %s recorded a stop reason", operation.name)
			}
			got, ok := s.PendingWorkspaceEnrollment()
			if !ok || got != pending {
				t.Fatalf("rejected %s changed enrollment: %+v, %v", operation.name, got, ok)
			}
			if _, ok := s.PendingAsk(); ok {
				t.Fatalf("rejected %s installed PendingAsk", operation.name)
			}
			if _, ok := s.PendingAuthorization(); ok {
				t.Fatalf("rejected %s installed PendingAuthorization", operation.name)
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentRejectsOtherPendingState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Session)
	}{
		{"permission ask", func(s *Session) { s.pending = &PendingAsk{AskID: "ask"} }},
		{"external authorization", func(s *Session) { s.pendingAuthorization = &PendingAuthorization{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			tc.setup(s)
			if err := s.BeginWorkspaceEnrollment(testWorkspaceEnrollment()); err == nil {
				t.Fatal("BeginWorkspaceEnrollment accepted other pending state")
			}
			if s.State != StateIdle || len(s.Conversation.Messages) != 0 {
				t.Fatalf("rejected begin mutated aggregate: state=%q messages=%d", s.State, len(s.Conversation.Messages))
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("rejected begin installed enrollment")
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentPrerequisites(t *testing.T) {
	pending := testWorkspaceEnrollment()
	tests := []struct {
		name  string
		setup func(*Session)
	}{
		{"unbound authority", func(*Session) {}},
		{"not idle", func(s *Session) { _ = s.BindAuthority(Authority{Provenance: "test"}); _ = s.BeginTurn() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New("s", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
			tc.setup(s)
			state := s.State
			if err := s.BeginWorkspaceEnrollment(pending); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("error = %v, want ErrIllegalTransition", err)
			}
			if s.State != state {
				t.Fatalf("rejected begin changed State: %q -> %q", state, s.State)
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("rejected begin installed enrollment")
			}
		})
	}
}

func TestBeginWorkspaceEnrollmentRejectsMalformedAndReplacement(t *testing.T) {
	valid := testWorkspaceEnrollment()
	tests := []struct {
		name   string
		mutate func(*PendingWorkspaceEnrollment)
	}{
		{"empty ID", func(p *PendingWorkspaceEnrollment) { p.ID = "" }},
		{"whitespace ID", func(p *PendingWorkspaceEnrollment) { p.ID = "bad id" }},
		{"NUL ID", func(p *PendingWorkspaceEnrollment) { p.ID = "bad\x00id" }},
		{"invalid UTF-8 ID", func(p *PendingWorkspaceEnrollment) { p.ID = WorkspaceEnrollmentID(string([]byte{0xff})) }},
		{"oversized ID", func(p *PendingWorkspaceEnrollment) {
			p.ID = WorkspaceEnrollmentID(strings.Repeat("x", maxWorkspaceEnrollmentIDBytes+1))
		}},
		{"no services", func(p *PendingWorkspaceEnrollment) { p.RequiredServices = 0 }},
		{"zero expiry", func(p *PendingWorkspaceEnrollment) { p.ExpiresAt = time.Time{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newEnrollmentSession(t)
			pending := valid
			tc.mutate(&pending)
			if err := s.BeginWorkspaceEnrollment(pending); err == nil {
				t.Fatal("BeginWorkspaceEnrollment accepted malformed value")
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("malformed begin installed enrollment")
			}
		})
	}

	exactlyBounded := valid
	exactlyBounded.ID = WorkspaceEnrollmentID(strings.Repeat("x", maxWorkspaceEnrollmentIDBytes))
	boundedSession := newEnrollmentSession(t)
	if err := boundedSession.BeginWorkspaceEnrollment(exactlyBounded); err != nil {
		t.Fatalf("exactly %d-byte ID rejected: %v", maxWorkspaceEnrollmentIDBytes, err)
	}

	s := newEnrollmentSession(t)
	if err := s.BeginWorkspaceEnrollment(valid); err != nil {
		t.Fatal(err)
	}
	replacement := valid
	replacement.ID = "replacement"
	if err := s.BeginWorkspaceEnrollment(replacement); err == nil {
		t.Fatal("BeginWorkspaceEnrollment replaced pending enrollment")
	}
	got, _ := s.PendingWorkspaceEnrollment()
	if got != valid {
		t.Fatalf("replacement attempt changed pending enrollment: %+v", got)
	}
}

func enrollmentAuthority(tools ...string) Authority {
	return Authority{
		CapabilitySet: governance.CapabilitySet{
			Tools:                    append([]string(nil), tools...),
			RemainingDelegationDepth: 2,
			FileSystem:               true,
			DirectWrite:              true,
		},
		Provenance:         "configured:test",
		DefinitionIdentity: "agent:test",
	}
}

func sessionWithEnrollmentAuthority(t *testing.T) (*Session, PendingWorkspaceEnrollment) {
	t.Helper()
	s := New("enrollment-complete", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	s.Owner = &Principal{Issuer: "issuer", Subject: "owner", GrantType: GrantTypeUser}
	if err := s.BindAuthority(enrollmentAuthority("Read", "stale")); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	return s, pending
}

func assertOnlyAuthorityToolsChanged(t *testing.T, before, after Authority, wantTools []string) {
	t.Helper()
	beforeValue, afterValue := reflect.ValueOf(before), reflect.ValueOf(after)
	authorityType := beforeValue.Type()
	for i := 0; i < authorityType.NumField(); i++ {
		field := authorityType.Field(i)
		if field.Name == "CapabilitySet" {
			beforeCapabilities := beforeValue.Field(i)
			afterCapabilities := afterValue.Field(i)
			capabilityType := beforeCapabilities.Type()
			for j := 0; j < capabilityType.NumField(); j++ {
				capabilityField := capabilityType.Field(j)
				if capabilityField.Name == "Tools" {
					if got := afterCapabilities.Field(j).Interface().([]string); !reflect.DeepEqual(got, wantTools) {
						t.Fatalf("authority tools = %q, want %q", got, wantTools)
					}
					continue
				}
				if !reflect.DeepEqual(beforeCapabilities.Field(j).Interface(), afterCapabilities.Field(j).Interface()) {
					t.Fatalf("non-tool capability axis %s changed: before=%v after=%v", capabilityField.Name, beforeCapabilities.Field(j), afterCapabilities.Field(j))
				}
			}
			continue
		}
		if !reflect.DeepEqual(beforeValue.Field(i).Interface(), afterValue.Field(i).Interface()) {
			t.Fatalf("non-tool authority field %s changed: before=%v after=%v", field.Name, beforeValue.Field(i), afterValue.Field(i))
		}
	}
}

func TestCompleteWorkspaceEnrollmentReplacesExactToolAuthority(t *testing.T) {
	s, pending := sessionWithEnrollmentAuthority(t)
	before := s.Authority
	owner := *s.Owner
	tools := []string{"Read", "mcp__github__issues"}
	if err := s.CompleteWorkspaceEnrollment(pending, tools); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollment: %v", err)
	}
	after := s.Authority
	assertOnlyAuthorityToolsChanged(t, before, after, []string{"Read", "mcp__github__issues"})
	got, bound := s.BoundAuthority()
	if !bound || !reflect.DeepEqual(got, after) {
		t.Fatalf("BoundAuthority = %+v, %v; want direct aggregate authority %+v, true", got, bound, after)
	}
	if got.CapabilitySet.AllowsTool("stale") {
		t.Fatal("exact replacement retained stale tool")
	}
	if *s.Owner != owner {
		t.Fatalf("completion changed principal: %+v", s.Owner)
	}
	if _, ok := s.PendingWorkspaceEnrollment(); ok {
		t.Fatal("successful completion retained pending enrollment")
	}
	tools[0] = "mutated"
	got, _ = s.BoundAuthority()
	if got.CapabilitySet.Tools[0] != "Read" {
		t.Fatal("tool-name input aliases bound authority")
	}
}

func TestCompleteWorkspaceEnrollmentPreservesNonToolAuthorityByAPIShape(t *testing.T) {
	method, ok := reflect.TypeOf((*Session)(nil)).MethodByName("CompleteWorkspaceEnrollment")
	if !ok {
		t.Fatal("CompleteWorkspaceEnrollment method missing")
	}
	want := []reflect.Type{
		reflect.TypeOf((*Session)(nil)),
		reflect.TypeOf(PendingWorkspaceEnrollment{}),
		reflect.TypeOf([]string(nil)),
	}
	if method.Type.NumIn() != len(want) {
		t.Fatalf("method accepts %d inputs, want receiver, pending correlation, and tool names", method.Type.NumIn())
	}
	for i, typ := range want {
		if got := method.Type.In(i); got != typ {
			t.Fatalf("input %d = %s, want %s", i, got, typ)
		}
	}
}

func TestCompleteWorkspaceEnrollmentRejectsChangesAtomically(t *testing.T) {
	valid := testWorkspaceEnrollment()
	tests := []struct {
		name    string
		pending PendingWorkspaceEnrollment
		tools   []string
	}{
		{"mismatched ID", func() PendingWorkspaceEnrollment { p := valid; p.ID = "other"; return p }(), []string{"replacement"}},
		{"mismatched service count", func() PendingWorkspaceEnrollment { p := valid; p.RequiredServices++; return p }(), []string{"replacement"}},
		{"mismatched expiry", func() PendingWorkspaceEnrollment { p := valid; p.ExpiresAt = p.ExpiresAt.Add(time.Second); return p }(), []string{"replacement"}},
		{"empty tool", valid, []string{""}},
		{"control in tool", valid, []string{"bad\nname"}},
		{"invalid UTF-8 tool", valid, []string{string([]byte{0xff})}},
		{"oversized tool", valid, []string{strings.Repeat("x", maxWorkspaceEnrollmentToolNameBytes+1)}},
		{"duplicate tool", valid, []string{"Read", "Read"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, pending := sessionWithEnrollmentAuthority(t)
			before, _ := s.BoundAuthority()
			if err := s.CompleteWorkspaceEnrollment(tc.pending, tc.tools); err == nil {
				t.Fatal("CompleteWorkspaceEnrollment accepted invalid replacement")
			}
			after, _ := s.BoundAuthority()
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected completion changed authority: got %+v, want %+v", after, before)
			}
			gotPending, ok := s.PendingWorkspaceEnrollment()
			if !ok || gotPending != pending {
				t.Fatalf("rejected completion changed pending: %+v, %v", gotPending, ok)
			}
		})
	}
}

func TestWorkspaceEnrollmentToolNamesPreserveLegacyGrammar(t *testing.T) {
	legacy := []string{"provider/tool name", "unicode-工具", "mcp__server__tool.with:punctuation"}
	if !ValidWorkspaceEnrollmentToolNames(legacy) {
		t.Fatalf("legacy-compatible names rejected: %q", legacy)
	}
	s := New("legacy-authority", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"duplicate", "duplicate", "legacy/tool name"}},
		Provenance:    "legacy",
	}); err != nil {
		t.Fatalf("global Authority validation was tightened: %v", err)
	}
}

func TestWorkspaceEnrollmentToolNamesBoundaries(t *testing.T) {
	exactAggregate := numberedWorkspaceEnrollmentTools(maxWorkspaceEnrollmentToolNamesBytes/128, 128)
	oneOverAggregate := append([]string(nil), exactAggregate...)
	oneOverAggregate[len(oneOverAggregate)-1] += "x"
	for _, tc := range []struct {
		name  string
		names []string
		valid bool
	}{
		{"exact tool count", numberedWorkspaceEnrollmentTools(maxWorkspaceEnrollmentTools, 4), true},
		{"one over tool count", numberedWorkspaceEnrollmentTools(maxWorkspaceEnrollmentTools+1, 4), false},
		{"exact aggregate bytes", exactAggregate, true},
		{"one over aggregate bytes", oneOverAggregate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidWorkspaceEnrollmentToolNames(tc.names); got != tc.valid {
				t.Fatalf("ValidWorkspaceEnrollmentToolNames() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func numberedWorkspaceEnrollmentTools(count, width int) []string {
	names := make([]string, count)
	for i := range names {
		suffix := strconv.Itoa(i)
		names[i] = strings.Repeat("x", width-len(suffix)) + suffix
	}
	return names
}

func TestCompleteWorkspaceEnrollmentRejectsAggregateBoundsAtomically(t *testing.T) {
	s, pending := sessionWithEnrollmentAuthority(t)
	before, _ := s.BoundAuthority()
	tools := numberedWorkspaceEnrollmentTools(maxWorkspaceEnrollmentToolNamesBytes/128, 128)
	tools[len(tools)-1] += "x"
	if err := s.CompleteWorkspaceEnrollment(pending, tools); err == nil {
		t.Fatal("CompleteWorkspaceEnrollment accepted one-over aggregate tool names")
	}
	after, _ := s.BoundAuthority()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected completion changed authority: got %+v, want %+v", after, before)
	}
	if got, ok := s.PendingWorkspaceEnrollment(); !ok || got != pending {
		t.Fatalf("rejected completion changed pending: %+v, %v", got, ok)
	}
}

func TestCompleteWorkspaceEnrollmentRequiresIdleSession(t *testing.T) {
	s, pending := sessionWithEnrollmentAuthority(t)
	before, _ := s.BoundAuthority()
	s.State = StateRunning
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"replacement"}); err == nil {
		t.Fatal("CompleteWorkspaceEnrollment accepted non-idle aggregate state")
	}
	after, _ := s.BoundAuthority()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected completion changed authority: %+v", after)
	}
	if got, ok := s.PendingWorkspaceEnrollment(); !ok || got != pending {
		t.Fatalf("rejected completion changed pending: %+v, %v", got, ok)
	}
}

func TestBeginWorkspaceEnrollmentAllowsEstablishedIdleConversation(t *testing.T) {
	s := newEnrollmentSession(t)
	s.Conversation.Append(NewUserMessage("prompt"))
	if err := s.BeginWorkspaceEnrollment(testWorkspaceEnrollment()); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
}
func TestCompleteWorkspaceEnrollmentAllowsEstablishedIdleConversation(t *testing.T) {
	s, pending := sessionWithEnrollmentAuthority(t)
	s.Conversation.Append(NewUserMessage("prompt"))
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"replacement"}); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollment: %v", err)
	}
}
