package mcpbroker_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// These tests establish value validation, not runtime dispatch or retry guarantees.
func TestSessionInvocationOutcomeArms(t *testing.T) {
	result := &session.ToolResult{CallID: "call"}
	ref := mcpbroker.AuthorizationRef(brokerRef())
	for _, kind := range []mcpbroker.InvocationKind{"", "future", mcpbroker.InvocationCompleted, mcpbroker.InvocationAuthorizationRequired, mcpbroker.InvocationNotDispatched, mcpbroker.InvocationOutcomeUnknown} {
		for _, payload := range []*session.ToolResult{nil, result} {
			for _, auth := range []mcpbroker.AuthorizationRef{"", ref, "invalid"} {
				for _, reason := range []mcpbroker.FailureReason{0, mcpbroker.FailureCatalogueChanged, mcpbroker.FailureAuthorityWithdrawn, mcpbroker.FailureCapacity, mcpbroker.FailureInterrupted, mcpbroker.FailureAuthorizationFailed, mcpbroker.FailureCallChanged, mcpbroker.FailureExpired, 255} {
					want := false
					switch kind {
					case mcpbroker.InvocationCompleted:
						want = payload != nil && auth == "" && reason == 0
					case mcpbroker.InvocationAuthorizationRequired:
						want = payload == nil && auth == ref && reason == 0
					case mcpbroker.InvocationNotDispatched:
						want = payload == nil && auth == "" && reason >= 1 && reason <= 7
					case mcpbroker.InvocationOutcomeUnknown:
						want = payload == nil && auth == "" && reason == 0
					}
					literal := mcpbroker.InvocationOutcome{Kind: kind, Result: payload, Authorization: auth, Reason: reason}
					outcome, err := mcpbroker.NewInvocationOutcome(kind, payload, auth, reason)
					if literal.Valid() != want || (err == nil) != want || (err == nil && !outcome.Valid()) {
						t.Fatalf("kind=%q payload=%v auth=%q reason=%d: outcome=%+v err=%v want valid=%v", kind, payload, auth, reason, outcome, err, want)
					}
				}
			}
		}
	}
}

func TestSessionFlowStatusArms(t *testing.T) {
	catalogue, err := mcpbroker.NewCatalogue(brokerRef(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []mcpbroker.FlowKind{"", "future", mcpbroker.FlowPending, mcpbroker.FlowCompleted, mcpbroker.FlowCancelled, mcpbroker.FlowExpired, mcpbroker.FlowFailed} {
		for _, payload := range []mcpbroker.Catalogue{nil, catalogue} {
			for _, reason := range []mcpbroker.FailureReason{0, mcpbroker.FailureInterrupted, 255} {
				want := false
				switch kind {
				case mcpbroker.FlowPending, mcpbroker.FlowCancelled, mcpbroker.FlowExpired:
					want = payload == nil && reason == 0
				case mcpbroker.FlowCompleted:
					want = payload != nil && reason == 0
				case mcpbroker.FlowFailed:
					want = payload == nil && reason == mcpbroker.FailureInterrupted
				}
				literal := mcpbroker.FlowStatus{Kind: kind, Catalogue: payload, Reason: reason}
				outcome, err := mcpbroker.NewFlowStatus(kind, payload, reason)
				if literal.Valid() != want || (err == nil) != want || (err == nil && !outcome.Valid()) {
					t.Fatalf("kind=%q catalogue=%v reason=%d: outcome=%+v err=%v want valid=%v", kind, payload, reason, outcome, err, want)
				}
			}
		}
	}
}

func TestSessionEnrollmentOutcomeArms(t *testing.T) {
	catalogue, err := mcpbroker.NewCatalogue(brokerRef(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := &mcpbroker.EnrollmentStarted{Ref: mcpbroker.EnrollmentRef(brokerRef()), Prompt: mcpbroker.BrowserPrompt{URL: "https://example.com/login", ExpiresAt: time.Unix(100, 0)}}
	for _, kind := range []mcpbroker.EnrollmentKind{"", "future", mcpbroker.EnrollmentStartedKind, mcpbroker.EnrollmentCompletedKind, mcpbroker.EnrollmentAlreadyConnected} {
		for _, pending := range []*mcpbroker.EnrollmentStarted{nil, {}, started} {
			for _, payload := range []mcpbroker.Catalogue{nil, catalogue} {
				want := false
				switch kind {
				case mcpbroker.EnrollmentStartedKind:
					want = pending == started && payload == nil
				case mcpbroker.EnrollmentCompletedKind:
					want = pending == nil && payload != nil
				case mcpbroker.EnrollmentAlreadyConnected:
					want = pending == nil && payload == nil
				}
				literal := mcpbroker.BeginEnrollmentOutcome{Kind: kind, Started: pending, Catalogue: payload}
				outcome, err := mcpbroker.NewBeginEnrollmentOutcome(kind, pending, payload)
				if literal.Valid() != want || (err == nil) != want || (err == nil && !outcome.Valid()) {
					t.Fatalf("kind=%q pending=%v catalogue=%v: outcome=%+v err=%v want valid=%v", kind, pending, payload, outcome, err, want)
				}
			}
		}
	}
	outcome, err := mcpbroker.NewBeginEnrollmentOutcome(mcpbroker.EnrollmentStartedKind, started, nil)
	if err != nil {
		t.Fatal(err)
	}
	started.Ref, started.Prompt.URL = "changed", "http://example.com"
	if !outcome.Valid() || outcome.Started.Ref != mcpbroker.EnrollmentRef(brokerRef()) || outcome.Started.Prompt.URL != "https://example.com/login" {
		t.Fatal("started outcome aliases source")
	}
}

func TestSessionBrowserPromptValidation(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		valid     bool
	}{
		{"https", "https://example.com/authorize", true},
		{"empty", "", false}, {"http", "http://example.com", false},
		{"relative", "/auth", false}, {"no host", "https:///auth", false},
		{"invalid UTF-8", "https://example.com/" + string([]byte{0xff}), false},
		{"credentials", "https://user:pass@example.com/", false},
		{"at bound", "https://example.com/" + strings.Repeat("a", 8192-len("https://example.com/")), true},
		{"oversize", "https://example.com/" + strings.Repeat("a", 8192), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mcpbroker.BrowserPrompt{URL: tc.url, ExpiresAt: time.Unix(100, 0)}
			if p.Valid() != tc.valid {
				t.Fatalf("Valid() = %v want %v", p.Valid(), tc.valid)
			}
			_, err := mcpbroker.NewBeginEnrollmentOutcome(mcpbroker.EnrollmentStartedKind, &mcpbroker.EnrollmentStarted{Ref: mcpbroker.EnrollmentRef(brokerRef()), Prompt: p}, nil)
			if (err == nil) != tc.valid {
				t.Fatalf("enrollment validation: %v", err)
			}
		})
	}
	if (mcpbroker.BrowserPrompt{URL: "https://example.com"}).Valid() {
		t.Fatal("accepted zero expiry")
	}
}

func TestSessionOutcomeEnumsRejectUnknown(t *testing.T) {
	for value := 0; value <= 255; value++ {
		if mcpbroker.CancelResult(value).Valid() != (value >= 1 && value <= 2) ||
			mcpbroker.DeleteResult(value).Valid() != (value >= 1 && value <= 2) ||
			mcpbroker.DisconnectResult(value).Valid() != (value >= 1 && value <= 3) ||
			mcpbroker.FailureReason(value).Valid() != (value >= 1 && value <= 7) {
			t.Fatalf("invalid enum acceptance for %d", value)
		}
	}
}

func TestSessionInvocationResultValidationAndCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		part session.Content
	}{
		{"text", session.Content{BlockKind: session.BlockText, Text: "text"}},
		{"structured", session.Content{BlockKind: session.BlockStructuredContent, Text: `{"ok":true}`}},
		{"image", session.Content{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0, 255}}},
		{"audio", session.Content{BlockKind: session.BlockAudio, Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{0, 255}}},
		{"resource text", session.Content{BlockKind: session.BlockEmbeddedResource, URL: "urn:test", Text: "resource"}},
		{"resource blob", session.Content{BlockKind: session.BlockEmbeddedResource, URL: "urn:test", Data: []byte{0, 255}, Audience: []string{"model"}}},
		{"link", session.Content{BlockKind: session.BlockResourceLink, URL: "https://example.com/resource"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &session.ToolResult{CallID: "call", Content: "summary", IsError: true, Parts: []session.Content{tc.part}}
			outcome, err := mcpbroker.NewInvocationOutcome(mcpbroker.InvocationCompleted, result, "", 0)
			if err != nil || !outcome.Valid() {
				t.Fatalf("valid result: %+v %v", outcome, err)
			}
			wantData := append([]byte(nil), tc.part.Data...)
			if len(result.Parts[0].Data) > 0 {
				result.Parts[0].Data[0] = 42
			}
			if len(result.Parts[0].Audience) > 0 {
				result.Parts[0].Audience[0] = "changed"
			}
			result.CallID, result.Content, result.IsError = "changed", "changed", false
			result.Parts[0] = session.Content{}
			if outcome.Result.CallID != "call" || outcome.Result.Content != "summary" || !outcome.Result.IsError || outcome.Result.Parts[0].BlockKind != tc.part.BlockKind || !bytes.Equal(outcome.Result.Parts[0].Data, wantData) {
				t.Fatal("result aliases source")
			}
			if len(outcome.Result.Parts[0].Audience) > 0 && outcome.Result.Parts[0].Audience[0] != "model" {
				t.Fatal("audience aliases source")
			}
		})
	}
	for _, result := range []*session.ToolResult{
		{}, {CallID: session.ToolCallID(strings.Repeat("x", 257))}, {CallID: session.ToolCallID(string([]byte{0xff}))},
		{CallID: "call", Content: string([]byte{0xff})},
	} {
		if _, err := mcpbroker.NewInvocationOutcome(mcpbroker.InvocationCompleted, result, "", 0); err == nil {
			t.Fatalf("accepted invalid result %+v", result)
		}
	}
	for _, part := range []session.Content{
		{BlockKind: "future"}, {BlockKind: session.BlockText, Text: string([]byte{0xff})},
		{BlockKind: session.BlockText, Data: []byte{1}}, {BlockKind: session.BlockText, URL: "https://example.com"},
		{BlockKind: session.BlockImage, Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{1}},
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png"},
		{BlockKind: session.BlockEmbeddedResource, URL: "urn:test"},
		{BlockKind: session.BlockEmbeddedResource, URL: "urn:test", Text: "both", Data: []byte{1}},
		{BlockKind: session.BlockResourceLink}, {BlockKind: session.BlockResourceLink, URL: "https://example.com", Text: "extra"},
		{BlockKind: session.BlockText, Text: "valid", Audience: []string{string([]byte{0xff})}},
		{BlockKind: session.BlockText, Text: "valid", Title: string([]byte{0xff})},
	} {
		result := &session.ToolResult{CallID: "call", Parts: []session.Content{part}}
		if _, err := mcpbroker.NewInvocationOutcome(mcpbroker.InvocationCompleted, result, "", 0); err == nil {
			t.Fatalf("accepted invalid part %+v", part)
		}
	}
}
