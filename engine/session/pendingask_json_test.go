package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// PendingAsk.HookOriginated is the SERIALIZED, cross-process load-bearing marker
// (ADR 0062): the awaiting-resume path keys the skip-preHook branch on it, and that
// path runs in a FRESH process that loaded the durable jsonlstore record. These tests
// pin the JSON contract DIRECTLY (independent of sessnap's in-process struct copy).

// HookOriginated must survive a marshal+unmarshal round-trip as true.
func TestPendingAskHookOriginatedRoundTrips(t *testing.T) {
	in := PendingAsk{AskID: "a1", Tool: "Shell", Call: "c1", HookOriginated: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out PendingAsk
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.HookOriginated {
		t.Fatalf("HookOriginated must round-trip true; got %+v (json=%s)", out, b)
	}
}

// omitempty must keep the field ABSENT from the JSON when false, so an old record (or a
// policy ask) deserializes HookOriginated=false without the key being present.
func TestPendingAskHookOriginatedOmitemptyWhenFalse(t *testing.T) {
	b, err := json.Marshal(PendingAsk{AskID: "a1", Tool: "Shell"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "hook_originated") {
		t.Fatalf("a false HookOriginated must be omitted (omitempty); json=%s", b)
	}
	// And a record with no key deserializes to false.
	var out PendingAsk
	if err := json.Unmarshal([]byte(`{"AskID":"a1","Tool":"Shell"}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.HookOriginated {
		t.Fatal("a record missing hook_originated must deserialize to false")
	}
}

// PendingAsk.PlanOriginated is the SECOND serialized, cross-process load-bearing
// provenance bit (issue #206): the awaiting-resume path keys the plan-flip branch on
// it, and that path runs in a FRESH process that loaded the durable jsonlstore record.
// These tests pin the JSON contract DIRECTLY (mirroring the HookOriginated pair).

// PlanOriginated must survive a marshal+unmarshal round-trip as true.
func TestPendingAskPlanOriginatedRoundTrips(t *testing.T) {
	in := PendingAsk{AskID: "a1", Tool: "PresentPlan", Call: "c1", PlanOriginated: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out PendingAsk
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.PlanOriginated {
		t.Fatalf("PlanOriginated must round-trip true; got %+v (json=%s)", out, b)
	}
}

// omitempty must keep the field ABSENT from the JSON when false, so an old record (or a
// policy/hook ask) deserializes PlanOriginated=false without the key being present.
func TestPendingAskPlanOriginatedOmitemptyWhenFalse(t *testing.T) {
	b, err := json.Marshal(PendingAsk{AskID: "a1", Tool: "Shell"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "plan_originated") {
		t.Fatalf("a false PlanOriginated must be omitted (omitempty); json=%s", b)
	}
	// And a record with no key deserializes to false.
	var out PendingAsk
	if err := json.Unmarshal([]byte(`{"AskID":"a1","Tool":"Shell"}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.PlanOriginated {
		t.Fatal("a record missing plan_originated must deserialize to false")
	}
}

// TestPendingAskOriginAccessor pins the read-time Origin() mapping: none / hook /
// plan, with Hook taking precedence when both bools are (incorrectly) set.
func TestPendingAskOriginAccessor(t *testing.T) {
	cases := []struct {
		name string
		ask  PendingAsk
		want AskOrigin
	}{
		{"none when neither bit set", PendingAsk{Tool: "Shell"}, AskOriginNone},
		{"hook when HookOriginated set", PendingAsk{Tool: "Shell", HookOriginated: true}, AskOriginHook},
		{"plan when PlanOriginated set", PendingAsk{Tool: "PresentPlan", PlanOriginated: true}, AskOriginPlan},
		{"hook wins when both set (tie-break)", PendingAsk{Tool: "Shell", HookOriginated: true, PlanOriginated: true}, AskOriginHook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ask.Origin(); got != tc.want {
				t.Fatalf("Origin() = %v, want %v", got, tc.want)
			}
		})
	}
}
