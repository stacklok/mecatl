package redisstore_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestUnknownPrivateReplayVersionSurvivesSnapshotEventAndCursor(t *testing.T) {
	st := newTestStore(t).(*redisstore.Store)
	data, err := os.ReadFile("../../engine/adapter/sessnap/testdata/legacy-completed.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := sessnap.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	s.Conversation.Messages[1].Replay.Provider = &session.ProviderReplay{Format: "future/private", Version: 99, Data: []byte{0xff, 0, '\n'}}
	if err := st.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	gotData, err := sessnap.Marshal(got)
	if err != nil || !bytes.Equal(wantData, gotData) || !reflect.DeepEqual(got.Conversation.Messages[1].Replay, s.Conversation.Messages[1].Replay) {
		t.Fatalf("snapshot replay: %v", err)
	}
	// Visible JSON arguments use the snapshot's existing normalization; private
	// carrier bytes and bindings above must remain exact.
	ev := session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: session.CloneMessages(got.Conversation.Messages)}}
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

func TestIdentityFormatRejectionDoesNotRewriteStreamOrCursor(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`{"v":"redisstore-eventlog/1","ev":{"Type":"tool.result","ToolResult":false}}`,
		`{"v":"redisstore-eventlog/1","ev":{"Type":"unknown"}}`,
		`{"v":"redisstore-eventlog/1","ev":{"identity_schema":2,"Type":"tool.result","ToolResult":false}}`,
		`{"v":"redisstore-eventlog/1","ev":{"identity_schema":99}}`,
		`{"v":"redisstore-eventlog/1","ev":{"identity_schema":null}}`,
		`{"v":"redisstore-eventlog/99","ev":{"identity_schema":2}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			mr := miniredis.RunT(t)
			st, err := redisstore.New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cursor, err := st.AppendEvent(t.Context(), "s", session.Event{Type: session.EvMessageDelta, Text: "sentinel"})
			if err != nil {
				t.Fatal(err)
			}
			const key = "mecatl:events:s"
			if err := client.XAdd(t.Context(), &redis.XAddArgs{Stream: key, Values: map[string]any{"r": raw}}).Err(); err != nil {
				t.Fatal(err)
			}
			before, err := client.XRange(t.Context(), key, "-", "+").Result()
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
					t.Fatal("cursor reader accepted unknown schema")
				}
				failed = true
			}
			if !failed {
				t.Fatal("cursor reader silently skipped invalid record")
			}
			after, err := client.XRange(t.Context(), key, "-", "+").Result()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("reader rewrote stream")
			}
		})
	}
}

func TestIdentitySnapshotPrivateStateAndLocalSchema(t *testing.T) {
	for _, name := range []string{"legacy-completed", "legacy-pending", "legacy-authorizing", "unknown-schema"} {
		t.Run(name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			st, err := redisstore.New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			old, err := os.ReadFile("../../engine/adapter/sessnap/testdata/legacy-completed.json")
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
			key := "mecatl:session:" + string(s.ID)
			switch name {
			case "legacy-pending":
				old = bytes.Replace(old, []byte(`"state": "completed"`), []byte(`"state": "awaiting", "pending": {}`), 1)
			case "legacy-authorizing":
				old = bytes.Replace(old, []byte(`"state": "completed"`), []byte(`"state": "authorizing", "pending_authorization": {}`), 1)
			case "unknown-schema":
				old = bytes.Replace(old, []byte(`"state": "completed"`), []byte(`"state": "completed", "identity_schema":99`), 1)
			}
			if err := client.HSet(t.Context(), key, "blob", old).Err(); err != nil {
				t.Fatal(err)
			}
			got, err := st.Load(t.Context(), s.ID)
			if name != "legacy-completed" {
				if err == nil {
					t.Fatal("accepted unsupported snapshot")
				}
				unchanged, readErr := client.HGet(t.Context(), key, "blob").Bytes()
				if readErr != nil || !bytes.Equal(old, unchanged) {
					t.Fatal("failed load rewrote snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			data, err := sessnap.Marshal(got)
			if err != nil || !bytes.Equal(want, data) {
				t.Fatal("local snapshot dropped private continuation or replay")
			}
			unchanged, err := client.HGet(t.Context(), key, "blob").Bytes()
			if err != nil || !bytes.Equal(old, unchanged) {
				t.Fatal("load rewrote legacy snapshot")
			}
			for range 2 {
				if err := st.Save(t.Context(), got); err != nil {
					t.Fatal(err)
				}
				got, err = st.Load(t.Context(), s.ID)
				if err != nil {
					t.Fatal(err)
				}
				data, err = sessnap.Marshal(got)
				if err != nil || !bytes.Equal(want, data) || bytes.Contains(data, []byte("identity_hold")) {
					t.Fatal("save/reload resurrected authority or changed history")
				}
			}
			stored, err := client.HGet(t.Context(), key, "blob").Bytes()
			if err != nil {
				t.Fatal(err)
			}
			bad := bytes.Replace(stored, []byte(`"identity_schema":2`), []byte(`"identity_schema":99`), 1)
			if bytes.Equal(bad, stored) {
				t.Fatal("snapshot writer lacks local identity schema")
			}
			if err := client.HSet(t.Context(), key, "blob", bad).Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Load(t.Context(), s.ID); err == nil {
				t.Fatal("accepted unknown local snapshot schema")
			}
			after, err := client.HGet(t.Context(), key, "blob").Bytes()
			if err != nil || !bytes.Equal(after, bad) {
				t.Fatal("failed load rewrote snapshot")
			}
		})
	}
}
