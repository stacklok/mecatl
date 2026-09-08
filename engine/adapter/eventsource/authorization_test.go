package eventsource_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

var authorizationExpiry = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func authorizationRequired(id string, call session.ToolCallID) session.Event {
	return session.Event{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
		AuthorizationID: id, DisplayName: "Calendar", Call: call, ExpiresAt: authorizationExpiry, Status: session.AuthorizationPending,
	}}
}

func authorizationResolved(id string, call session.ToolCallID, status session.AuthorizationStatus) session.Event {
	return session.Event{Type: session.EvAuthorizationResolved, Authorization: &session.AuthorizationPayload{
		AuthorizationID: id, DisplayName: "Calendar", Call: call, ExpiresAt: authorizationExpiry, Status: status,
	}}
}

func authorizationCall(id string, call session.ToolCallID) []session.Event {
	tc := toolCall(string(call), "external", `{}`)
	result := session.NewToolResult(call, "authorized")
	return []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &tc},
		authorizationRequired(id, call),
		{Type: session.EvToolResult, ToolResult: &result},
		authorizationResolved(id, call, session.AuthorizationGranted),
	}
}

func requireFoldError(t *testing.T, events []session.Event, target error) {
	t.Helper()
	got, err := eventsource.Fold(meta(), seq(events))
	if got != nil {
		t.Fatalf("Fold returned session on error: %+v", got)
	}
	if !errors.Is(err, target) {
		t.Fatalf("Fold error = %v, want %v", err, target)
	}
}

func TestFoldAuthorizationUnresolvedRequiresPrivateState(t *testing.T) {
	requireFoldError(t, []session.Event{authorizationRequired("auth-1", "call-1")}, eventsource.ErrPrivateStateRequired)
}

func TestFoldAuthorizationRequiredResultResolved(t *testing.T) {
	folded, err := eventsource.Fold(meta(), seq(authorizationCall("auth-1", "call-1")))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(folded.Conversation.Messages) != 2 || folded.Conversation.Messages[1].ToolResult == nil || folded.Conversation.Messages[1].ToolResult.CallID != "call-1" {
		t.Fatalf("conversation = %+v", folded.Conversation.Messages)
	}
}

func TestFoldAuthorizationAcceptsTerminalStatuses(t *testing.T) {
	statuses := []session.AuthorizationStatus{
		session.AuthorizationGranted,
		session.AuthorizationDenied,
		session.AuthorizationCancelled,
		session.AuthorizationExpired,
		session.AuthorizationInterrupted,
		session.AuthorizationFailed,
		session.AuthorizationClosed,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			events := authorizationCall("auth-1", "call-1")
			events[len(events)-1].Authorization.Status = status
			if _, err := eventsource.Fold(meta(), seq(events)); err != nil {
				t.Fatalf("Fold: %v", err)
			}
		})
	}
}

func TestFoldAuthorizationMultipleHistoricalLifecycles(t *testing.T) {
	events := append(authorizationCall("auth-1", "call-1"), authorizationCall("auth-2", "call-2")...)
	if _, err := eventsource.Fold(meta(), seq(events)); err != nil {
		t.Fatalf("Fold: %v", err)
	}
}

func TestFoldAuthorizationConcurrentLifecyclesResolveIndependently(t *testing.T) {
	call1 := toolCall("call-1", "external", `{}`)
	call2 := toolCall("call-2", "external", `{}`)
	result1 := session.NewToolResult("call-1", "done")
	result2 := session.NewToolResult("call-2", "done")
	events := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &call1},
		{Type: session.EvToolCall, ToolCall: &call2},
		authorizationRequired("auth-1", "call-1"),
		authorizationRequired("auth-2", "call-2"),
		{Type: session.EvToolResult, ToolResult: &result2},
		authorizationResolved("auth-2", "call-2", session.AuthorizationDenied),
		{Type: session.EvToolResult, ToolResult: &result1},
		authorizationResolved("auth-1", "call-1", session.AuthorizationGranted),
	}
	if _, err := eventsource.Fold(meta(), seq(events)); err != nil {
		t.Fatalf("Fold: %v", err)
	}
}

func TestFoldAuthorizationTwoOpenOneUnresolved(t *testing.T) {
	call1 := toolCall("call-1", "external", `{}`)
	call2 := toolCall("call-2", "external", `{}`)
	result1 := session.NewToolResult("call-1", "done")
	result2 := session.NewToolResult("call-2", "done")
	events := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &call1},
		{Type: session.EvToolCall, ToolCall: &call2},
		authorizationRequired("auth-1", "call-1"),
		authorizationRequired("auth-2", "call-2"),
		{Type: session.EvToolResult, ToolResult: &result1},
		authorizationResolved("auth-1", "call-1", session.AuthorizationGranted),
		{Type: session.EvToolResult, ToolResult: &result2},
	}
	requireFoldError(t, events, eventsource.ErrPrivateStateRequired)
}

func TestFoldAuthorizationRejectsResolvedWithoutRequired(t *testing.T) {
	requireFoldError(t, []session.Event{authorizationResolved("auth-1", "call-1", session.AuthorizationDenied)}, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsDuplicateLiveCall(t *testing.T) {
	events := []session.Event{authorizationRequired("auth-1", "call-1"), authorizationRequired("auth-2", "call-1")}
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsHistoricalIDReuse(t *testing.T) {
	events := append(authorizationCall("auth-1", "call-1"), authorizationRequired("auth-1", "call-2"))
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsHistoricalCallReuse(t *testing.T) {
	events := append(authorizationCall("auth-1", "call-1"), authorizationRequired("auth-2", "call-1"))
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsResultBeforeRequired(t *testing.T) {
	result := session.NewToolResult("call-1", "done")
	events := []session.Event{{Type: session.EvToolResult, ToolResult: &result}, authorizationRequired("auth-1", "call-1")}
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsDuplicateRequired(t *testing.T) {
	events := []session.Event{authorizationRequired("auth-1", "call-1"), authorizationRequired("auth-1", "call-1")}
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsInvalidStatuses(t *testing.T) {
	result := session.NewToolResult("call-1", "done")
	for _, tc := range []struct {
		name   string
		events []session.Event
	}{
		{"required terminal", []session.Event{{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{AuthorizationID: "auth-1", Call: "call-1", ExpiresAt: authorizationExpiry, Status: session.AuthorizationGranted}}}},
		{"required unknown", []session.Event{{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{AuthorizationID: "auth-1", Call: "call-1", ExpiresAt: authorizationExpiry, Status: "unknown"}}}},
		{"resolved pending", []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &result}, authorizationResolved("auth-1", "call-1", session.AuthorizationPending)}},
		{"resolved unknown", []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &result}, authorizationResolved("auth-1", "call-1", "unknown")}},
	} {
		t.Run(tc.name, func(t *testing.T) { requireFoldError(t, tc.events, eventsource.ErrReconstruct) })
	}
}

func TestFoldAuthorizationRejectsResolvedDisplayNameMismatch(t *testing.T) {
	result := session.NewToolResult("call-1", "done")
	resolved := authorizationResolved("auth-1", "call-1", session.AuthorizationGranted)
	resolved.Authorization.DisplayName = "Different"
	events := []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &result}, resolved}
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsResolvedExpiryMismatch(t *testing.T) {
	result := session.NewToolResult("call-1", "done")
	for _, tc := range []struct {
		name   string
		expiry time.Time
	}{
		{"zero", time.Time{}},
		{"mismatched", authorizationExpiry.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := authorizationResolved("auth-1", "call-1", session.AuthorizationGranted)
			resolved.Authorization.ExpiresAt = tc.expiry
			events := []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &result}, resolved}
			requireFoldError(t, events, eventsource.ErrReconstruct)
		})
	}
}

func TestFoldAuthorizationRejectsMismatchedCall(t *testing.T) {
	result := session.NewToolResult("call-1", "done")
	events := []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &result}, authorizationResolved("auth-1", "call-2", session.AuthorizationGranted)}
	requireFoldError(t, events, eventsource.ErrReconstruct)
}

func TestFoldAuthorizationRejectsResolvedBeforeOrWithoutMatchingResult(t *testing.T) {
	wrong := session.NewToolResult("call-2", "done")
	for _, tc := range []struct {
		name   string
		events []session.Event
	}{
		{"before result", []session.Event{authorizationRequired("auth-1", "call-1"), authorizationResolved("auth-1", "call-1", session.AuthorizationGranted)}},
		{"wrong result", []session.Event{authorizationRequired("auth-1", "call-1"), {Type: session.EvToolResult, ToolResult: &wrong}, authorizationResolved("auth-1", "call-1", session.AuthorizationGranted)}},
	} {
		t.Run(tc.name, func(t *testing.T) { requireFoldError(t, tc.events, eventsource.ErrReconstruct) })
	}
}

func TestFoldAuthorizationRejectsNilOrMalformedPayload(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	for _, tc := range []struct {
		name  string
		event session.Event
	}{
		{"nil required", session.Event{Type: session.EvAuthorizationRequired}},
		{"nil resolved", session.Event{Type: session.EvAuthorizationResolved}},
		{"missing id", authorizationRequired("", "call-1")},
		{"missing call", authorizationRequired("auth-1", "")},
		{"oversized id", authorizationRequired(strings.Repeat("a", 257), "call-1")},
		{"invalid UTF-8 id", authorizationRequired(invalidUTF8, "call-1")},
		{"control id", authorizationRequired("auth\n1", "call-1")},
		{"whitespace id", authorizationRequired("auth 1", "call-1")},
		{"oversized call", authorizationRequired("auth-1", session.ToolCallID(strings.Repeat("c", 257)))},
		{"invalid UTF-8 call", authorizationRequired("auth-1", session.ToolCallID(invalidUTF8))},
		{"control call", authorizationRequired("auth-1", "call\t1")},
		{"whitespace call", authorizationRequired("auth-1", "call 1")},
		{"missing expiry", session.Event{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{AuthorizationID: "auth-1", Call: "call-1", Status: session.AuthorizationPending}}},
	} {
		t.Run(tc.name, func(t *testing.T) { requireFoldError(t, []session.Event{tc.event}, eventsource.ErrReconstruct) })
	}
}
