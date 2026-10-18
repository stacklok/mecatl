package jsonlstore_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyMixedEventIdentityReadRestartAndCursor(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	const id session.SessionID = "legacy-events"
	gap, err := st.AppendGap(ctx, id, "fixture gap")
	if err != nil {
		t.Fatal(err)
	}
	path := canonicalFamilyPath(dir, id, ".events.jsonl")
	raw := []byte(`{"v":"eventlog-json/1","ev":{"Type":"tool.call","Seq":22,"ToolCall":{"ID":"private-provider","Name":"Read","Args":{}}}}
{"v":"eventlog-json/1","ev":{"Type":"tool.result","Seq":1,"ToolResult":{"CallID":"private-provider","Content":"old"}}}
`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	read := func(st *jsonlstore.Store) []session.Event {
		t.Helper()
		var events []session.Event
		for e, err := range st.Read(ctx, id) {
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, e)
		}
		return events
	}
	es := read(st)
	if len(es) != 2 || es[0].Type != session.EvHistorical || es[0].ToolCall != nil {
		t.Fatal("direct read reinterpreted old provider IDs")
	}
	inc := session.LegacyIncarnationID(id, 0, nil)
	p, err := eventcodec.Project(es, inc)
	if err != nil {
		t.Fatal(err)
	}
	again, err := eventcodec.Project(read(st), inc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatal("reads before first save disagree")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read rewrote raw legacy log")
	}
	var fromCursor []session.Event
	for rec, err := range st.ReadAfter(ctx, id, gap, port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		fromCursor = append(fromCursor, rec.Event)
	}
	if len(fromCursor) != 2 {
		t.Fatal("gap/ordering changed")
	}
	for i, e := range fromCursor {
		if e.LegacyIdentity().Coordinate != es[i].LegacyIdentity().Coordinate || e.LegacyIdentity().Namespace != es[i].LegacyIdentity().Namespace {
			t.Fatal("read/cursor disagree about source position")
		}
	}
	// Append a new canonical pair, not a rewrite of the old pair.
	c := session.NewToolCall(session.NewToolCallID(), "Read", nil)
	for _, e := range []session.Event{{Type: session.EvToolCall, ToolCall: &c}, {Type: session.EvToolResult, ToolResult: &session.ToolResult{CallID: c.ID, Content: "new"}}} {
		if err := st.Append(ctx, id, e); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	mixed := read(reopened)
	migrated, err := eventcodec.Project(mixed, inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.Events) != 4 || migrated.Events[0].ToolCall.ID != p.Events[0].ToolCall.ID || migrated.Events[2].ToolCall.ID != c.ID || migrated.Events[3].ToolResult.CallID != c.ID {
		t.Fatal("mixed restart changed identity")
	}
}
