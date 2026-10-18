package jsonlstore_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestUnknownPrivateReplayVersionSurvivesSnapshotEventAndCursor(t *testing.T) {
	st, _ := newStore(t)
	s := driven(t)
	s.Conversation.Messages[0].Replay = &session.MessageReplay{
		Bindings: []session.ToolCallBinding{{CallID: "c1", PairingID: "provider", ItemID: "item"}},
		Provider: &session.ProviderReplay{Format: "future/private", Version: 99, Data: []byte{0xff, 0, '\n'}},
	}
	if err := st.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(t.Context(), s.ID)
	if err != nil || !reflect.DeepEqual(got.Conversation.Messages, s.Conversation.Messages) {
		t.Fatalf("snapshot replay: %v", err)
	}
	ev := session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: session.CloneMessages(s.Conversation.Messages)}}
	cursor, err := st.AppendEvent(t.Context(), s.ID, ev)
	if err != nil {
		t.Fatal(err)
	}
	for got, err := range st.Read(t.Context(), s.ID) {
		if err != nil || !reflect.DeepEqual(got, ev) {
			t.Fatalf("event replay: %v", err)
		}
	}
	for rec, err := range st.ReadAfter(t.Context(), s.ID, "", port.ReadOptions{}) {
		if err != nil || rec.Cursor != cursor || !reflect.DeepEqual(rec.Event, ev) {
			t.Fatalf("cursor replay: %v", err)
		}
	}
}

func TestIdentityFormatRejectionDoesNotRewriteLogOrCursor(t *testing.T) {
	for _, raw := range []string{
		`{"v":"eventlog-json/1","ev":{"identity_schema":2}}`,
		`{"v":"eventlog-json/2","ev":{}}`,
		`{"v":"eventlog-json/2","ev":{"identity_schema":99}}`,
		`{"v":"eventlog-json/99","ev":{"identity_schema":2}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			st, dir := newStore(t)
			cursor, err := st.AppendEvent(t.Context(), "s", session.Event{Type: session.EvMessageDelta, Text: "sentinel"})
			if err != nil {
				t.Fatal(err)
			}
			path := canonicalFamilyPath(dir, "s", ".events.jsonl")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(raw + "\n"); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, failed := 0, false
			for _, err := range st.Read(t.Context(), "s") {
				if err != nil {
					failed = true
				} else {
					count++
				}
			}
			if !failed || count != 1 {
				t.Fatalf("read count=%d failed=%v", count, failed)
			}
			failed = false
			for rec, err := range st.ReadAfter(t.Context(), "s", cursor, port.ReadOptions{}) {
				if err == nil || rec.Cursor != "" {
					t.Fatal("cursor reader accepted contradictory format")
				}
				failed = true
			}
			if !failed {
				t.Fatal("cursor reader silently skipped invalid record")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("reader rewrote committed log")
			}
		})
	}
}

func TestIdentitySnapshotPrivateStateAndLocalSchema(t *testing.T) {
	for _, name := range []string{"legacy-authorizing", "legacy-ambiguous-pending"} {
		t.Run(name, func(t *testing.T) {
			st, dir := newStore(t)
			old, err := os.ReadFile("../../engine/adapter/sessnap/testdata/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			s, err := sessnap.Unmarshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Save(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			got, err := st.Load(t.Context(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
			want, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			data, err := sessnap.Marshal(got)
			if err != nil || !bytes.Equal(want, data) || !reflect.DeepEqual(s.IdentityHoldSnapshot(), got.IdentityHoldSnapshot()) {
				t.Fatal("local snapshot dropped private continuation or replay")
			}
			path := canonicalSnapshotPath(dir, s.ID)
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			bad := bytes.Replace(stored, []byte(`"identity_schema":2`), []byte(`"identity_schema":99`), 1)
			if bytes.Equal(bad, stored) {
				t.Fatal("snapshot writer lacks local identity schema")
			}
			if err := os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Load(t.Context(), s.ID); err == nil {
				t.Fatal("accepted unknown local snapshot schema")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, bad) {
				t.Fatal("failed load rewrote snapshot")
			}
		})
	}
}
