package grpcdriver

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Frozen shipped format guards, independent of the newest format constants and
// read sets. Only the decoding after a successful old guard uses today's codec.
// This fixture proves rejection happens before decoding or backend mutation.
type frozenSnapshotWrapper struct {
	driverv1.SessionStoreServiceServer
}

func (s frozenSnapshotWrapper) Save(ctx context.Context, req *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	if req.GetSnapshot().GetFormat() != "sessnap-json/1" {
		return nil, status.Error(codes.InvalidArgument, "unknown snapshot format")
	}
	return s.SessionStoreServiceServer.Save(ctx, req)
}
func (s frozenSnapshotWrapper) Create(ctx context.Context, req *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	if req.GetSnapshot().GetFormat() != "sessnap-json/1" {
		return nil, status.Error(codes.InvalidArgument, "valid snapshot required")
	}
	return s.SessionStoreServiceServer.Create(ctx, req)
}

type frozenEventWrapper struct{ driverv1.EventLogServiceServer }

func (s frozenEventWrapper) Append(ctx context.Context, req *driverv1.AppendRequest) (*driverv1.AppendResponse, error) {
	if req.GetEvent().GetFormat() != "eventlog-json/1" {
		return nil, status.Error(codes.InvalidArgument, "unknown event format")
	}
	return s.EventLogServiceServer.Append(ctx, req)
}
func frozenSnapshotRead(env *driverv1.SessionSnapshot) error {
	if env.GetFormat() != "sessnap-json/1" {
		return fmt.Errorf("unknown snapshot format %q", env.GetFormat())
	}
	return nil
}
func frozenEventRead(env *driverv1.LoggedEvent) error {
	if env.GetFormat() != "eventlog-json/1" {
		return fmt.Errorf("unknown event format %q", env.GetFormat())
	}
	return nil
}

type countedSnapshotStore struct {
	*memstore.Store
	writes int
}

func (s *countedSnapshotStore) Save(ctx context.Context, v *session.Session) error {
	s.writes++
	return s.Store.Save(ctx, v)
}
func (s *countedSnapshotStore) Create(ctx context.Context, v *session.Session) error {
	s.writes++
	return s.Store.Create(ctx, v)
}

type countedEventLog struct {
	*memstore.EventLog
	writes int
}

func (s *countedEventLog) Append(ctx context.Context, id session.SessionID, e session.Event) error {
	s.writes++
	return s.EventLog.Append(ctx, id, e)
}
func (s *countedEventLog) AppendEvent(ctx context.Context, id session.SessionID, e session.Event) (port.Cursor, error) {
	s.writes++
	return s.EventLog.AppendEvent(ctx, id, e)
}

func legacySnapshotBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../engine/adapter/sessnap/testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodingWrapperSkewAndRejectedWritesPreserveBackend(t *testing.T) {
	ctx := t.Context()
	for _, frozen := range []bool{false, true} {
		t.Run(fmt.Sprint("frozen=", frozen), func(t *testing.T) {
			oldData := legacySnapshotBytes(t, "legacy-completed")
			s, err := sessnap.Unmarshal(oldData)
			if err != nil {
				t.Fatal(err)
			}
			newData, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			backend := &countedSnapshotStore{Store: memstore.New()}
			if err := backend.Store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			server := NewSessionStoreServer(backend)
			if frozen {
				server = frozenSnapshotWrapper{server}
			}
			for _, format := range []string{SnapshotFormat, "sessnap-json/99", "sessnap-json/1"} {
				// /1 carrying schema 2 must also fail on the new wrapper. Old
				// wrappers cannot detect that lie; the new writer never sends it.
				if frozen && format == "sessnap-json/1" || !frozen && format == SnapshotFormat {
					continue
				}
				for _, write := range []func(context.Context, *driverv1.SaveRequest) (*driverv1.SaveResponse, error){server.Save, server.Create} {
					_, err := write(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: format, Payload: newData}})
					if status.Code(err) != codes.InvalidArgument {
						t.Fatalf("format %s: %v", format, err)
					}
				}
			}
			for _, payload := range [][]byte{oldData, bytes.Replace(newData, []byte(`"identity_schema":2`), []byte(`"identity_schema":99`), 1)} {
				for _, write := range []func(context.Context, *driverv1.SaveRequest) (*driverv1.SaveResponse, error){server.Save, server.Create} {
					_, err := write(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: payload}})
					if status.Code(err) != codes.InvalidArgument {
						t.Fatalf("contradictory snapshot: %v", err)
					}
				}
			}
			if backend.writes != 0 {
				t.Fatalf("rejected snapshot performed %d writes", backend.writes)
			}
			got, err := backend.Load(ctx, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotData, err := sessnap.Marshal(got)
			if err != nil || !bytes.Equal(gotData, newData) {
				t.Fatal("sentinel changed")
			}
			if _, err := server.Save(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/1", Payload: oldData}}); err != nil {
				t.Fatal(err)
			}
			// Positive /1 Create on a fresh backend proves the frozen guard is
			// not an always-reject stub.
			fresh := &countedSnapshotStore{Store: memstore.New()}
			createServer := NewSessionStoreServer(fresh)
			if frozen {
				createServer = frozenSnapshotWrapper{createServer}
			}
			if _, err := createServer.Create(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/1", Payload: oldData}}); err != nil || fresh.writes != 1 {
				t.Fatalf("old create: %v writes=%d", err, fresh.writes)
			}
			upgradedRead, err := NewSessionStoreServer(fresh).Load(ctx, &driverv1.LoadRequest{SessionId: string(s.ID)})
			if err != nil || upgradedRead.GetSnapshot().GetFormat() != "sessnap-json/2" {
				t.Fatalf("upgraded decoding read: %v", err)
			}
			if err := frozenSnapshotRead(upgradedRead.Snapshot); err == nil {
				t.Fatal("old reader accepted upgraded decoding wrapper output")
			}

			log := memstore.NewEventLog()
			counted := &countedEventLog{EventLog: log}
			cursor, err := log.AppendEvent(ctx, s.ID, session.Event{Type: session.EvMessageDelta, Text: "sentinel"})
			if err != nil {
				t.Fatal(err)
			}
			eventServer := NewEventLogServer(counted)
			if frozen {
				eventServer = frozenEventWrapper{eventServer}
			}
			eventData, err := eventcodec.Marshal(session.Event{Type: session.EvToolResult, ToolResult: &session.ToolResult{CallID: "engine-id", Content: "new"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{EventLogFormat, "eventlog-json/99", "eventlog-json/1"} {
				if !frozen && format == EventLogFormat || frozen && format == "eventlog-json/1" {
					continue
				}
				_, err := eventServer.Append(ctx, &driverv1.AppendRequest{SessionId: string(s.ID), Event: &driverv1.LoggedEvent{Format: format, Payload: eventData}})
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("append %s: %v", format, err)
				}
			}
			for _, payload := range [][]byte{[]byte(`{"Type":"message.delta"}`), bytes.Replace(eventData, []byte(`"identity_schema":2`), []byte(`"identity_schema":99`), 1)} {
				_, err := eventServer.Append(ctx, &driverv1.AppendRequest{SessionId: string(s.ID), Event: &driverv1.LoggedEvent{Format: EventLogFormat, Payload: payload}})
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("contradictory event: %v", err)
				}
			}
			if counted.writes != 0 {
				t.Fatal("rejected append touched backend")
			}
			for _, err := range log.ReadAfter(ctx, s.ID, cursor, port.ReadOptions{}) {
				t.Fatalf("rejected append advanced cursor: %v", err)
			}
			_, err = eventServer.Append(ctx, &driverv1.AppendRequest{SessionId: string(s.ID), Event: &driverv1.LoggedEvent{Format: "eventlog-json/1", Payload: []byte(`{"Type":"message.delta","Text":"old"}`)}})
			if err != nil || counted.writes != 1 {
				t.Fatalf("positive old append: %v writes=%d", err, counted.writes)
			}
		})
	}
}

// A raw driver has no codec dependency and deliberately accepts arbitrary tags.
type opaqueSnapshotFixture struct {
	driverv1.UnimplementedSessionStoreServiceServer
	mu       sync.Mutex
	snapshot *driverv1.SessionSnapshot
}

func (*opaqueSnapshotFixture) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	return &driverv1.SessionStoreCapabilitiesResponse{Contract: "session-store/1", Create: true}, nil
}
func (s *opaqueSnapshotFixture) Save(_ context.Context, r *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = proto.Clone(r.Snapshot).(*driverv1.SessionSnapshot)
	return &driverv1.SaveResponse{}, nil
}
func (s *opaqueSnapshotFixture) Create(ctx context.Context, r *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	return s.Save(ctx, r)
}
func (s *opaqueSnapshotFixture) Load(context.Context, *driverv1.LoadRequest) (*driverv1.LoadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &driverv1.LoadResponse{Snapshot: proto.Clone(s.snapshot).(*driverv1.SessionSnapshot)}, nil
}

type opaqueEventFixture struct {
	driverv1.UnimplementedEventLogServiceServer
	mu    sync.Mutex
	event *driverv1.LoggedEvent
}

func (s *opaqueEventFixture) Append(_ context.Context, r *driverv1.AppendRequest) (*driverv1.AppendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.event = proto.Clone(r.Event).(*driverv1.LoggedEvent)
	return &driverv1.AppendResponse{Cursor: "private-cursor/unchanged"}, nil
}
func (s *opaqueEventFixture) Read(_ *driverv1.ReadRequest, stream grpc.ServerStreamingServer[driverv1.ReadResponse]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stream.Send(&driverv1.ReadResponse{Event: s.event})
}
func (s *opaqueEventFixture) ReadAfter(_ *driverv1.ReadAfterRequest, stream grpc.ServerStreamingServer[driverv1.ReadAfterResponse]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stream.Send(&driverv1.ReadAfterResponse{Kind: string(port.LogRecordEvent), Cursor: "private-cursor/unchanged", Event: s.event})
}

func TestOpaqueDriverFormatsAndPrivateCarrierRoundTrip(t *testing.T) {
	ctx := t.Context()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, &opaqueSnapshotFixture{})
		driverv1.RegisterEventLogServiceServer(gs, &opaqueEventFixture{})
	})
	rawStore := driverv1.NewSessionStoreServiceClient(conn)
	rawLog := driverv1.NewEventLogServiceClient(conn)
	store := mustNewSessionStore(t, conn)
	log := NewEventLog(conn)
	for _, name := range []string{"legacy-completed", "legacy-retry"} {
		s, err := sessnap.Unmarshal(legacySnapshotBytes(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
		env, err := rawStore.Load(ctx, &driverv1.LoadRequest{SessionId: string(s.ID)})
		if err != nil {
			t.Fatal(err)
		}
		want, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if env.Snapshot.Format != "sessnap-json/2" || !bytes.Equal(env.Snapshot.Payload, want) {
			t.Fatal("opaque snapshot changed bytes or tag")
		}
		if err := frozenSnapshotRead(env.Snapshot); err == nil {
			t.Fatal("old snapshot reader accepted /2")
		}
		got, err := store.Load(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := sessnap.Marshal(got)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatal("new reader dropped pending, evidence or held state")
		}
		if err := frozenSnapshotRead(&driverv1.SessionSnapshot{Format: "sessnap-json/1"}); err != nil {
			t.Fatal(err)
		}
	}
	// Unknown private provider versions are opaque even to the new harness.
	s := session.New("private", session.ModeDefault, session.EnvironmentRef{Kind: "mem", ID: "ws", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	m := session.Message{Role: session.RoleAssistant, Replay: &session.MessageReplay{Provider: &session.ProviderReplay{Format: "future/private", Version: 99, Data: []byte{0xff, 0, '\n'}}}}
	if err := s.RecordAssistant(m); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, s.ID)
	if err != nil || !reflect.DeepEqual(got.Conversation.Messages, s.Conversation.Messages) {
		t.Fatalf("private carrier: %v", err)
	}
	current := session.Event{Type: session.EvToolResult, ToolResult: &session.ToolResult{CallID: session.NewToolCallID(), Content: "current"}}
	cursor, err := log.AppendEvent(ctx, s.ID, current)
	if err != nil || cursor != "private-cursor/unchanged" {
		t.Fatalf("opaque cursor: %q %v", cursor, err)
	}
	stream, err := rawLog.Read(ctx, &driverv1.ReadRequest{SessionId: string(s.ID)})
	if err != nil {
		t.Fatal(err)
	}
	env, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	wantEvent, err := eventcodec.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if env.Event.Format != "eventlog-json/2" || !bytes.Equal(env.Event.Payload, wantEvent) {
		t.Fatal("opaque event changed bytes or tag")
	}
	if err := frozenEventRead(env.Event); err == nil {
		t.Fatal("old event reader accepted /2")
	}
	for e, err := range log.Read(ctx, s.ID) {
		if err != nil || !reflect.DeepEqual(e, current) {
			t.Fatalf("event private state: %v", err)
		}
	}
	for rec, err := range log.ReadAfter(ctx, s.ID, "", port.ReadOptions{}) {
		if err != nil || rec.Cursor != cursor || !reflect.DeepEqual(rec.Event, current) {
			t.Fatalf("cursor private state: %v", err)
		}
	}
	archive := session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{m}}}
	for _, target := range []*EventLog{log, newWiredEventLog(t, memstore.NewEventLog())} {
		at, err := target.AppendEvent(ctx, s.ID, archive)
		if err != nil {
			t.Fatal(err)
		}
		for got, err := range target.Read(ctx, s.ID) {
			if err != nil || !reflect.DeepEqual(got, archive) {
				t.Fatalf("archive replay: %v", err)
			}
		}
		for rec, err := range target.ReadAfter(ctx, s.ID, "", port.ReadOptions{}) {
			if err != nil || rec.Cursor != at || !reflect.DeepEqual(rec.Event, archive) {
				t.Fatalf("archive cursor replay: %v", err)
			}
		}
	}
	// The raw protocol carries even unknown outer encodings without decoding.
	unknownSnapshot := &driverv1.SessionSnapshot{Format: "private-snapshot/99", Payload: []byte{0xff, 0, 1}}
	if _, err := rawStore.Save(ctx, &driverv1.SaveRequest{SessionId: "unknown", Snapshot: unknownSnapshot}); err != nil {
		t.Fatal(err)
	}
	unknown, err := rawStore.Load(ctx, &driverv1.LoadRequest{SessionId: "unknown"})
	if err != nil || !proto.Equal(unknown.Snapshot, unknownSnapshot) {
		t.Fatalf("raw unknown snapshot: %v", err)
	}
	unknownEvent := &driverv1.LoggedEvent{Format: "private-event/99", Payload: []byte{0xff, 0, 2}}
	if _, err := rawLog.Append(ctx, &driverv1.AppendRequest{SessionId: "unknown", Event: unknownEvent}); err != nil {
		t.Fatal(err)
	}
	unknownStream, err := rawLog.ReadAfter(ctx, &driverv1.ReadAfterRequest{SessionId: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	unknownRecord, err := unknownStream.Recv()
	if err != nil || !proto.Equal(unknownRecord.Event, unknownEvent) || unknownRecord.Cursor != string(cursor) {
		t.Fatalf("raw unknown cursor record: %v", err)
	}
}

func TestCursorReaderRejectsContradictoryIdentityFormats(t *testing.T) {
	for _, tc := range []struct {
		format, payload string
		valid           bool
	}{
		{"eventlog-json/1", `{"Type":"message.delta","Text":"old"}`, true},
		{"eventlog-json/2", `{"identity_schema":2,"Type":"message.delta","Text":"new"}`, true},
		{"eventlog-json/1", `{"identity_schema":2}`, false},
		{"eventlog-json/2", `{}`, false},
		{"eventlog-json/2", `{"identity_schema":99}`, false},
		{"eventlog-json/99", `{"identity_schema":2}`, false},
	} {
		r, err := logRecordFromProto("s", &driverv1.ReadAfterResponse{Kind: string(port.LogRecordEvent), Cursor: "sentinel", Event: &driverv1.LoggedEvent{Format: tc.format, Payload: []byte(tc.payload)}})
		if (err == nil) != tc.valid {
			t.Fatalf("%s %s: %v", tc.format, tc.payload, err)
		}
		if err == nil && r.Cursor != "sentinel" {
			t.Fatal("reader rewrote cursor")
		}
		if err != nil && r.Cursor != "" {
			t.Fatal("reader advanced rejected cursor")
		}
		fixture := &opaqueEventFixture{event: &driverv1.LoggedEvent{Format: tc.format, Payload: []byte(tc.payload)}}
		conn := dialBufconn(t, func(gs *grpc.Server) { driverv1.RegisterEventLogServiceServer(gs, fixture) })
		client := NewEventLog(conn)
		count := 0
		for _, readErr := range client.Read(t.Context(), "s") {
			count++
			if (readErr == nil) != tc.valid {
				t.Fatalf("wire read %s: %v", tc.format, readErr)
			}
		}
		if count != 1 {
			t.Fatal("wire reader dropped record")
		}
		count = 0
		for _, readErr := range client.ReadAfter(t.Context(), "s", "", port.ReadOptions{}) {
			count++
			if (readErr == nil) != tc.valid {
				t.Fatalf("wire cursor read %s: %v", tc.format, readErr)
			}
		}
		if count != 1 {
			t.Fatal("wire cursor reader dropped record")
		}
	}
}

func TestLegacySnapshotContinuationRejected(t *testing.T) {
	ctx := t.Context()
	old := legacySnapshotBytes(t, "legacy-completed")
	s, err := sessnap.Unmarshal(old)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{
		`"state": "awaiting"`,
		`"state": "authorizing"`,
		`"state": "completed", "pending": {}`,
		`"state": "completed", "pending_authorization": {}`,
		`"state": "completed", "identity_hold": {}`,
	} {
		t.Run(replacement, func(t *testing.T) {
			payload := bytes.Replace(old, []byte(`"state": "completed"`), []byte(replacement), 1)
			backend := &countedSnapshotStore{Store: memstore.New()}
			if err := backend.Store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			server := NewSessionStoreServer(backend)
			for _, write := range []func(context.Context, *driverv1.SaveRequest) (*driverv1.SaveResponse, error){server.Save, server.Create} {
				_, err := write(ctx, &driverv1.SaveRequest{SessionId: string(s.ID), Snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/1", Payload: payload}})
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("unsupported snapshot write: %v", err)
				}
			}
			if backend.writes != 0 {
				t.Fatal("unsupported snapshot reached backend")
			}
			fixture := &opaqueSnapshotFixture{snapshot: &driverv1.SessionSnapshot{Format: "sessnap-json/1", Payload: payload}}
			conn := dialBufconn(t, func(gs *grpc.Server) { driverv1.RegisterSessionStoreServiceServer(gs, fixture) })
			if _, err := mustNewSessionStore(t, conn).Load(ctx, s.ID); err == nil {
				t.Fatal("client repaired unsupported snapshot")
			}
		})
	}
}

func TestSnapshotClientReadFormatSet(t *testing.T) {
	old := legacySnapshotBytes(t, "legacy-completed")
	s, err := sessnap.Unmarshal(old)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		format string
		data   []byte
		valid  bool
	}{
		{"sessnap-json/1", old, true}, {"sessnap-json/2", newData, true},
		{"sessnap-json/1", newData, false}, {"sessnap-json/2", old, false}, {"sessnap-json/99", newData, false},
	} {
		fixture := &opaqueSnapshotFixture{snapshot: &driverv1.SessionSnapshot{Format: tc.format, Payload: tc.data}}
		conn := dialBufconn(t, func(gs *grpc.Server) { driverv1.RegisterSessionStoreServiceServer(gs, fixture) })
		_, err := mustNewSessionStore(t, conn).Load(t.Context(), s.ID)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.format, err)
		}
	}
}
