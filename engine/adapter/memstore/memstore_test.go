package memstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func driven(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("s1", session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{
		MaxTurns: 5, MaxToolCalls: 9, MaxConsecutiveFailures: 2,
	}, time.Unix(1700000000, 0).UTC())
	_ = s.BeginTurn()
	_ = s.RecordAssistant(session.NewAssistantMessage("hi", "", []session.ToolCall{
		session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a"}`)),
	}))
	_ = s.RecordToolResults([]session.ToolResult{session.NewToolResult("c1", "data")})
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	want := driven(t)
	if err := st.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != want.State || got.Mode != want.Mode || got.Limits != want.Limits ||
		got.Counters != want.Counters {
		t.Fatalf("scalar mismatch: got %+v want %+v", got, want)
	}
	if !reflect.DeepEqual(got.Conversation, want.Conversation) {
		t.Fatalf("conversation mismatch:\n got %+v\nwant %+v", got.Conversation, want.Conversation)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := memstore.New().Load(context.Background(), "nope")
	if !errors.Is(err, memstore.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveDeepCopyNoAliasing(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := driven(t)
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Mutate the original after saving; the stored copy must be unaffected.
	_ = s.RecordToolResults([]session.ToolResult{session.NewToolResult("c2", "more")})

	got, err := st.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Conversation.Len() != 2 {
		t.Fatalf("stored conversation len = %d, want 2 (no aliasing to mutated original)", got.Conversation.Len())
	}
}

func TestConcurrentSaveLoad(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := driven(t)
			if err := st.Save(ctx, s); err != nil {
				t.Errorf("Save: %v", err)
				return
			}
			if _, err := st.Load(ctx, "s1"); err != nil {
				t.Errorf("Load: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestEstimatedBytesTracksTranscriptOverwriteAndDelete(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()

	if err := st.Save(ctx, sessionWithTranscript(t, "estimate", 1, "x")); err != nil {
		t.Fatalf("Save small: %v", err)
	}
	small := onlyEstimatedBytes(t, st)

	if err := st.Save(ctx, sessionWithTranscript(t, "estimate", 128, string(make([]byte, 1024)))); err != nil {
		t.Fatalf("Save large overwrite: %v", err)
	}
	large := onlyEstimatedBytes(t, st)
	if large <= small {
		t.Fatalf("large transcript estimate = %d, want greater than small estimate %d", large, small)
	}

	if err := st.Delete(ctx, "estimate"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	page, err := st.PageSessionMetadata(ctx, sessionMetadataRequest())
	if err != nil {
		t.Fatalf("PageSessionMetadata after delete: %v", err)
	}
	if len(page.Sessions) != 0 {
		t.Fatalf("sessions after delete = %d, want 0", len(page.Sessions))
	}
}

func TestPageSessionMetadataWorkIsIndependentOfTranscriptSize(t *testing.T) {
	measure := func(sess *session.Session) testing.BenchmarkResult {
		st := memstore.New()
		if err := st.Save(context.Background(), sess); err != nil {
			t.Fatalf("Save: %v", err)
		}
		request := sessionMetadataRequest()
		return testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				page, err := st.PageSessionMetadata(context.Background(), request)
				if err != nil || len(page.Sessions) != 1 {
					b.Fatalf("PageSessionMetadata: rows=%d err=%v", len(page.Sessions), err)
				}
			}
		})
	}

	small := measure(sessionWithTranscript(t, "work", 256, string(make([]byte, 4096))))
	large := measure(sessionWithTranscript(t, "work", 1024, string(make([]byte, 4096))))
	if large.AllocedBytesPerOp() > small.AllocedBytesPerOp()+256 {
		t.Fatalf("page allocated bytes grew with transcript: small=%d large=%d (allocs small=%d large=%d)",
			small.AllocedBytesPerOp(), large.AllocedBytesPerOp(), small.AllocsPerOp(), large.AllocsPerOp())
	}
}

func sessionWithTranscript(t testing.TB, id session.SessionID, messages int, text string) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	for range messages {
		if err := s.RecordAssistant(session.NewAssistantMessage(text, "", nil)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func onlyEstimatedBytes(t testing.TB, st *memstore.Store) int64 {
	t.Helper()
	page, err := st.PageSessionMetadata(context.Background(), sessionMetadataRequest())
	if err != nil {
		t.Fatalf("PageSessionMetadata: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("page sessions = %d, want 1", len(page.Sessions))
	}
	return page.Sessions[0].EstimatedBytes
}

func sessionMetadataRequest() port.SessionMetadataPageRequest {
	return port.SessionMetadataPageRequest{Limit: 10}
}

// TestWithNowStampsDeterministicModifiedAt pins the injected-clock seam: List
// reports each session's ModifiedAt as the WithNow clock read at Save time, so
// retention tests can assert age deterministically.
func TestWithNowStampsDeterministicModifiedAt(t *testing.T) {
	ctx := context.Background()
	current := time.Unix(1000, 0).UTC()
	st := memstore.New(memstore.WithNow(func() time.Time { return current }))

	a := session.New("clock-a", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, current)
	if err := st.Save(ctx, a); err != nil {
		t.Fatalf("Save(a): %v", err)
	}
	current = current.Add(time.Hour)
	b := session.New("clock-b", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, current)
	if err := st.Save(ctx, b); err != nil {
		t.Fatalf("Save(b): %v", err)
	}

	entries, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make(map[session.SessionID]time.Time, len(entries))
	for _, e := range entries {
		got[e.ID] = e.ModifiedAt
	}
	if want := time.Unix(1000, 0).UTC(); !got["clock-a"].Equal(want) {
		t.Errorf("a.ModifiedAt = %v, want the fake clock's %v", got["clock-a"], want)
	}
	if want := time.Unix(1000, 0).UTC().Add(time.Hour); !got["clock-b"].Equal(want) {
		t.Errorf("b.ModifiedAt = %v, want the fake clock's %v", got["clock-b"], want)
	}
}

// TestEventLogAppendRead pins the in-memory EventLog: Append records each event
// and Read replays them in append order; a miss yields an empty sequence.
func TestEventLogAppendRead(t *testing.T) {
	ctx := context.Background()
	log := memstore.NewEventLog()

	// Miss → empty.
	n := 0
	for range log.Read(ctx, "ghost") {
		n++
	}
	if n != 0 {
		t.Fatalf("miss yielded %d events, want 0", n)
	}

	want := []session.EventType{session.EvToolCall, session.EvPermissionAsk, session.EvApproval, session.EvResult}
	for i, ty := range want {
		if err := log.Append(ctx, "s1", session.Event{Type: ty, Seq: int64(i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	var got []session.EventType
	for ev, err := range log.Read(ctx, "s1") {
		if err != nil {
			t.Fatalf("Read item error: %v", err)
		}
		got = append(got, ev.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read order = %v, want %v", got, want)
	}
	// A different id is isolated.
	m := 0
	for range log.Read(ctx, "s2") {
		m++
	}
	if m != 0 {
		t.Fatalf("isolated id yielded %d events, want 0", m)
	}
}

// TestEventLogReadEarlyBreak pins the iter.Seq2 contract for the in-memory log: a
// Read that breaks after the first event returns cleanly, and a subsequent Append +
// full Read still works (the snapshot copy means the early break never corrupts state).
func TestEventLogReadEarlyBreak(t *testing.T) {
	ctx := context.Background()
	log := memstore.NewEventLog()
	for i := 0; i < 4; i++ {
		if err := log.Append(ctx, "s1", session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got := 0
	for range log.Read(ctx, "s1") {
		got++
		break
	}
	if got != 1 {
		t.Fatalf("early-break yielded %d events, want 1", got)
	}
	if err := log.Append(ctx, "s1", session.Event{Type: session.EvResult, Seq: 4}); err != nil {
		t.Fatalf("Append after early-break: %v", err)
	}
	total := 0
	for range log.Read(ctx, "s1") {
		total++
	}
	if total != 5 {
		t.Fatalf("Read after early-break returned %d events, want 5", total)
	}
}
