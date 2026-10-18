package server

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestWireIdentityBaselineReservesPrivateReplay(t *testing.T) {
	desc := toProtoConversationMessage(session.NewUserMessage("visible")).ProtoReflect().Descriptor()
	if desc.Fields().ByName("reasoning") != nil || desc.Fields().ByName("reasoning_item_id") != nil ||
		!desc.ReservedNames().Has("reasoning") || !desc.ReservedNames().Has("reasoning_item_id") ||
		!desc.ReservedRanges().Has(5) || !desc.ReservedRanges().Has(8) ||
		desc.Fields().ByName("provider_phase").Number() != 6 || desc.Fields().ByName("historical").Number() != 9 {
		t.Fatal("public history replay withdrawal/reservations drifted")
	}
	if APIMajor != 2 || toProto(session.Event{}).ProtoReflect().Descriptor().Fields().ByName("historical").Number() != 27 {
		t.Fatal("canonical identity baseline or event extension drifted")
	}
}

type incompleteHistoryLog struct {
	port.EventLog
	events []session.Event
	err    error
}

func (l incompleteHistoryLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range l.events {
			if !yield(ev, nil) {
				return
			}
		}
		yield(session.Event{}, l.err)
	}
}

func TestHistoricalIncompleteReadPreservesPrefixWithoutProvingPairs(t *testing.T) {
	inc := session.LegacyIncarnationID("incomplete-public", 0, nil)
	var old []session.Event
	for _, raw := range []string{
		`{"Type":"tool.call","RunID":"old","Seq":1,"ToolCall":{"ID":"provider","Name":"Read"}}`,
		`{"Type":"tool.result","RunID":"old","Seq":2,"ToolResult":{"CallID":"provider","Content":"still readable"}}`,
	} {
		ev, err := eventcodec.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		identity := ev.LegacyIdentity()
		identity.Incarnation = inc
		old = append(old, ev.WithLegacyIdentity(*identity))
	}
	current := session.NewToolCall("canonical", "Read", nil)
	for _, events := range [][]session.Event{old, {{Type: session.EvToolCall, ToolCall: &current}}} {
		cause := errors.New("source read failed")
		svc := &Service{cfg: Config{EventLog: incompleteHistoryLog{events: events, err: cause}}}
		stream, err := svc.StreamSessionEvents(t.Context(), "incomplete-public")
		if err != nil {
			t.Fatal(err)
		}
		count, failed := 0, false
		for ev, err := range stream {
			if err != nil {
				failed = errors.Is(err, cause)
				continue
			}
			if events[0].LegacyIdentity() != nil && (ev.Historical == nil || ev.ToolCall != nil || ev.ToolResult != nil) {
				t.Fatal("incomplete source attached a legacy invocation")
			}
			count++
		}
		if !failed || count != len(events) {
			t.Fatalf("prefix/error changed: count=%d error=%t", count, failed)
		}
	}
}

func TestHistoricalSnapshotArchiveAndEventPrivacy(t *testing.T) {
	sess, err := sessnap.Unmarshal([]byte(`{"id":"historical-public","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"assistant","text":"visible","reasoning":"PRIVATE-REPLAY","reasoning_item_id":"PRIVATE-ITEM","phase":"commentary","tool_calls":[{"ID":"PROVIDER-ORPHAN","Name":"Read"},{"ID":"PROVIDER-ORPHAN","Name":"Read"}]},{"role":"tool","tool_result":{"CallID":"PROVIDER-MISSING","Content":"orphan readable result","Parts":[{"Kind":"image","MIMEType":"image/png","Data":"AQ=="}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{Store: store}}
	transcript, err := svc.GetTranscript(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	wire := toProtoTranscript(transcript)
	archive := toProtoCompactionArchive(session.CompactionArchivePayload{Replaced: sess.Conversation.Messages})
	for i, m := range wire.Messages {
		if m.Historical == nil || !m.Historical.AttachmentUnavailable || m.Historical.RecordId == "" || m.ToolResult != nil || len(m.ToolCalls) != 0 {
			t.Fatalf("unsafe message: %+v", m)
		}
		if archive.Replaced[i].Historical.RecordId != m.Historical.RecordId {
			t.Fatal("archive changed source-local identity")
		}
	}
	if wire.Messages[0].ProviderPhase != "commentary" || wire.Messages[1].Historical.Text != "orphan readable result" || len(wire.Messages[1].Historical.Parts) != 1 {
		t.Fatalf("lost readable content: %+v", wire)
	}
	for _, message := range archive.Replaced {
		raw, err := protojson.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"PRIVATE-REPLAY", "PRIVATE-ITEM", "PROVIDER-ORPHAN", "PROVIDER-MISSING"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("public archive leaked %s", private)
			}
		}
	}
	legacyRel := session.SessionRelationship{ParentSessionID: "parent", Legacy: &session.LegacyRelationshipIdentity{ProviderCallID: "PROVIDER-EDGE"}}
	if toProtoSessionRelationship(legacyRel) != nil {
		t.Fatal("unproven parent edge projected as a normal relationship")
	}
}

func TestHistoricalFullReplayMixedAndCursorProjection(t *testing.T) {
	ctx := context.Background()
	sess, err := sessnap.Unmarshal([]byte(`{"id":"mixed-public","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	log := memstore.NewEventLog()
	legacy := []string{
		`{"Type":"tool.call","RunID":"old","Seq":1,"ToolCall":{"ID":"PROVIDER-CALL","Name":"Read"}}`,
		`{"Type":"tool.result","RunID":"old","Seq":2,"ToolResult":{"CallID":"PROVIDER-CALL","Content":"paired content"}}`,
		`{"Type":"hook","RunID":"old","Seq":3,"Hook":{"CallID":"PROVIDER-CALL","Phase":"PostToolUse","Tool":"Read","Decision":"info"}}`,
		`{"Type":"tool.result","RunID":"old","Seq":4,"ToolResult":{"CallID":"PROVIDER-ORPHAN","Content":"unattached content"}}`,
	}
	var events []session.Event
	for i, raw := range legacy {
		ev, err := eventcodec.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		ev = ev.WithLegacySource(eventcodec.LegacyNamespace, "record:"+string(rune('a'+i)))
		events = append(events, ev)
		if err := log.Append(ctx, sess.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	current := session.NewToolCall("canonical-current", "Read", nil)
	if err := log.Append(ctx, sess.ID, session.Event{Type: session.EvToolCall, RunID: "new", Seq: 1, ToolCall: &current}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{Store: store, EventLog: log}, cursorLog: log}
	stream, err := svc.StreamSessionEvents(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var projected []session.Event
	for ev, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		projected = append(projected, ev)
	}
	if len(projected) != 5 || projected[0].Historical == nil || projected[1].Historical == nil || projected[0].ToolCall != nil || projected[1].ToolResult != nil || projected[1].Historical.Text != "paired content" || projected[4].ToolCall.ID != current.ID {
		t.Fatalf("streaming legacy/count/current changed: %+v", projected)
	}
	for i, ev := range projected[:4] {
		if ev.Seq != int64(i+1) || ev.RunID != "old" {
			t.Fatal("projection changed order or run sequence")
		}
		wire := toProto(ev)
		raw, err := protojson.Marshal(wire)
		if err != nil || strings.Contains(string(raw), "PROVIDER-") {
			t.Fatalf("unsafe event %s: %v", raw, err)
		}
		if wire.Historical == nil || wire.Hook != nil || wire.ToolCall != nil || wire.ToolResult != nil {
			t.Fatal("unproven streaming attachment projected as normal payload")
		}
	}
	watchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	watch, err := svc.WatchSessionEvents(watchCtx, sess.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var envelopes []WatchEnvelope
	for env, err := range watch {
		if err != nil {
			t.Fatal(err)
		}
		envelopes = append(envelopes, env)
		if env.Event == nil {
			break
		}
	}
	if len(envelopes) != 6 || envelopes[5].Phase != WatchPhaseLive || envelopes[5].Cursor != envelopes[4].Cursor {
		t.Fatalf("watch synthesized/lost records or reset position: %+v", envelopes)
	}
	for i, env := range envelopes[:4] {
		if env.Phase != WatchPhaseReplay || env.Event.Historical == nil || env.Cursor == "" || env.Event.Seq != int64(i+1) {
			t.Fatalf("watch legacy record attached or reordered: %+v", env)
		}
	}
	after := envelopes[1].Cursor
	resume, err := svc.WatchSessionEvents(watchCtx, sess.ID, after, "old")
	if err != nil {
		t.Fatal(err)
	}
	var resumed []WatchEnvelope
	for env, err := range resume {
		if err != nil {
			t.Fatal(err)
		}
		if env.Event == nil {
			break
		}
		resumed = append(resumed, env)
	}
	if len(resumed) != 2 || resumed[0].Cursor != envelopes[2].Cursor || resumed[1].Cursor != envelopes[3].Cursor || resumed[0].Event.Historical.RecordID != envelopes[2].Event.Historical.RecordID {
		t.Fatal("cursor resume changed source-local identity or delivery position")
	}
	var last port.Cursor
	for _, env := range envelopes[:5] {
		if env.Cursor == last {
			t.Fatal("watch repeated delivery position")
		}
		last = env.Cursor
	}
	for _, ev := range events {
		one, err := svc.projectWatchEvent(ctx, sess.ID, ev)
		if err != nil {
			t.Fatal(err)
		}
		wire := toProto(one)
		if wire.Historical == nil || wire.Historical.RecordId == "" || wire.ToolCall != nil || wire.ToolResult != nil || wire.Hook != nil || one.Seq != ev.Seq {
			t.Fatalf("unsafe cursor projection: %+v", wire)
		}
	}
}
