package session

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationEventStrings(t *testing.T) {
	if EvAuthorizationRequired != "authorization.required" || EvAuthorizationResolved != "authorization.resolved" {
		t.Fatalf("event strings = (%q, %q)", EvAuthorizationRequired, EvAuthorizationResolved)
	}
	want := []struct {
		status AuthorizationStatus
		value  string
	}{
		{AuthorizationPending, "pending"},
		{AuthorizationGranted, "granted"},
		{AuthorizationDenied, "denied"},
		{AuthorizationCancelled, "cancelled"},
		{AuthorizationExpired, "expired"},
		{AuthorizationInterrupted, "interrupted"},
		{AuthorizationFailed, "failed"},
		{AuthorizationClosed, "closed"},
	}
	for _, item := range want {
		if string(item.status) != item.value {
			t.Errorf("status %q = %q, want %q", item.status, item.status, item.value)
		}
	}
}

func TestAuthorizationPayloadValid(t *testing.T) {
	valid := AuthorizationPayload{
		AuthorizationID: "auth-1",
		DisplayName:     "GitHub Enterprise",
		Call:            "call-1",
		ExpiresAt:       time.Unix(1, 0),
		Status:          AuthorizationPending,
	}
	if !valid.Valid() {
		t.Fatal("valid payload rejected")
	}

	invalidUTF8 := string([]byte{0xff})
	for _, tc := range []struct {
		name   string
		mutate func(*AuthorizationPayload)
	}{
		{"oversized authorization id", func(p *AuthorizationPayload) { p.AuthorizationID = strings.Repeat("a", 257) }},
		{"invalid UTF-8 authorization id", func(p *AuthorizationPayload) { p.AuthorizationID = invalidUTF8 }},
		{"control authorization id", func(p *AuthorizationPayload) { p.AuthorizationID = "auth\n1" }},
		{"whitespace authorization id", func(p *AuthorizationPayload) { p.AuthorizationID = "auth 1" }},
		{"oversized display name", func(p *AuthorizationPayload) { p.DisplayName = strings.Repeat("a", 257) }},
		{"invalid UTF-8 display name", func(p *AuthorizationPayload) { p.DisplayName = invalidUTF8 }},
		{"control display name", func(p *AuthorizationPayload) { p.DisplayName = "GitHub\nforged" }},
		{"surrounding whitespace display name", func(p *AuthorizationPayload) { p.DisplayName = " GitHub" }},
		{"oversized call id", func(p *AuthorizationPayload) { p.Call = ToolCallID(strings.Repeat("c", 257)) }},
		{"invalid UTF-8 call id", func(p *AuthorizationPayload) { p.Call = ToolCallID(invalidUTF8) }},
		{"control call id", func(p *AuthorizationPayload) { p.Call = "call\t1" }},
		{"whitespace call id", func(p *AuthorizationPayload) { p.Call = "call 1" }},
		{"zero expiry", func(p *AuthorizationPayload) { p.ExpiresAt = time.Time{} }},
		{"unknown status", func(p *AuthorizationPayload) { p.Status = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := valid
			tc.mutate(&payload)
			if payload.Valid() {
				t.Fatalf("invalid payload accepted: %+v", payload)
			}
		})
	}
}

func TestAuthorizationPayloadSafeShape(t *testing.T) {
	typ := reflect.TypeOf(AuthorizationPayload{})
	want := map[string]reflect.Type{
		"AuthorizationID": reflect.TypeOf(""),
		"DisplayName":     reflect.TypeOf(""),
		"Call":            reflect.TypeOf(ToolCallID("")),
		"ExpiresAt":       reflect.TypeOf(time.Time{}),
		"Status":          reflect.TypeOf(AuthorizationStatus("")),
	}
	if typ.NumField() != len(want) {
		t.Fatalf("AuthorizationPayload has %d fields, want exactly %d", typ.NumField(), len(want))
	}
	for name, fieldType := range want {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Errorf("field %s is missing", name)
			continue
		}
		if field.Type != fieldType {
			t.Errorf("field %s type = %v, want %v", name, field.Type, fieldType)
		}
	}

	eventField, ok := reflect.TypeOf(Event{}).FieldByName("Authorization")
	if !ok {
		t.Fatal("Event.Authorization is missing")
	}
	if eventField.Type != reflect.TypeOf((*AuthorizationPayload)(nil)) {
		t.Fatalf("Event.Authorization type = %v", eventField.Type)
	}
}
