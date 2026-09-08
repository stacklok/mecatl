package server_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// drainRun consumes a run's events to completion so the session reaches a
// terminal state and the store holds the final snapshot.
func drainRun(t *testing.T, run interface{ Events() <-chan session.Event }) {
	t.Helper()
	for range run.Events() {
	}
}

func TestStartRunContentRecordsMultimodal(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("I see it"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	parts := []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50}},
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "what is this", parts)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	drainRun(t, run)
	// Persist while the run is still registered so the store holds the final
	// in-memory session the engine mutated; then deregister.
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	got, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// Find the recorded user message and assert it carries the media part.
	var found bool
	for _, m := range got.Conversation.Messages {
		if m.Role == session.RoleUser && len(m.Parts) == 1 && m.Parts[0].Kind == session.MediaImage {
			found = true
			if m.Text != "what is this" {
				t.Fatalf("user text = %q, want preserved", m.Text)
			}
		}
	}
	if !found {
		t.Fatalf("no multimodal user message recorded: %+v", got.Conversation.Messages)
	}
}

func TestStartRunContentEmptyRejected(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = svc.StartRunContent(context.Background(), sess.ID, "", nil)
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestStartRunDelegatesToContent(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("ok"))
	svc := newService(t, llm, allowRules())
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// StartRun with empty text must still reject (delegation preserves the guard).
	if _, err := svc.StartRun(context.Background(), sess.ID, ""); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("StartRun(empty) err = %v, want ErrInvalidArgument", err)
	}
	// A normal text StartRun records a text-only user message (no parts).
	run, err := svc.StartRun(context.Background(), sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(t, run)
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	got, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, m := range got.Conversation.Messages {
		if m.Role == session.RoleUser && m.Parts != nil {
			t.Fatalf("text StartRun recorded parts: %+v", m.Parts)
		}
	}
}

// TestStartRunContentReopensCompletedSession is the regression guard for the
// in-process multi-turn bug: a first prompt drives the session to StateCompleted
// (and persists it), and a second StartRun on the same session id must reopen it
// rather than fail with "RecordUserPrompt from completed". It mirrors the
// cross-process LoadSession resume path for an interactive multi-turn chat.
func TestStartRunContentReopensCompletedSession(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// First turn → runs to completion and persists StateCompleted.
	run, err := svc.StartRun(context.Background(), sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun #1: %v", err)
	}
	drainRun(t, run)
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	if got, _ := svc.GetSession(context.Background(), sess.ID); got.State != session.StateCompleted {
		t.Fatalf("precondition: state after turn #1 = %q, want completed", got.State)
	}

	// Second prompt on the SAME session must succeed (reopen-if-completed).
	run, err = svc.StartRun(context.Background(), sess.ID, "again")
	if err != nil {
		t.Fatalf("StartRun #2 on completed session: %v", err)
	}
	drainRun(t, run)
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)

	got, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	var userPrompts int
	for _, m := range got.Conversation.Messages {
		if m.Role == session.RoleUser {
			userPrompts++
		}
	}
	if userPrompts != 2 {
		t.Fatalf("user prompts = %d, want 2 (history preserved across reopen)", userPrompts)
	}
}

func TestProviderCapabilitiesSurface(t *testing.T) {
	want := port.ProviderCapabilities{Image: true, EmbeddedContext: true}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(want)})
	svc := newService(t, llm, allowRules())
	if got := svc.ProviderCapabilities(); !got.Image || got.Audio {
		t.Fatalf("ProviderCapabilities() = %+v, want image-only", got)
	}
	_ = time.Now
}

// TestGRPCConverseRejectsBadPart asserts the gRPC Converse wire path rejects a
// malformed Content part (KIND_UNSPECIFIED) with InvalidArgument before starting
// a run.
func TestGRPCConverseRejectsBadPart(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if serr := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
		Prompt: &mecatlv1.Prompt{
			SessionId: cs.GetSessionId(),
			Text:      "look",
			Parts:     []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_UNSPECIFIED, MimeType: "image/png", Data: []byte{1}}},
		},
	}}); serr != nil {
		t.Fatalf("Send: %v", serr)
	}
	_, rerr := stream.Recv()
	if status.Code(rerr) != codes.InvalidArgument {
		t.Fatalf("recv code = %v, want InvalidArgument (err=%v)", status.Code(rerr), rerr)
	}
}

// TestGRPCConverseCarriesMediaPart asserts a VALID image part survives the FULL
// wire→domain→engine→provider path (the load-bearing accept-path counterpart to
// TestGRPCConverseRejectsBadPart). A mockllm request observer captures the
// LLMRequest the provider actually received, and the test asserts the user message
// in it carries a session.MediaImage part with the expected bytes — so dropping the
// parts anywhere on that path (e.g. the gRPC handler passing nil to
// StartRunContent) FAILS this test, which a "no InvalidArgument" check alone could
// not detect. Fully offline (mockllm + bufconn).
func TestGRPCConverseCarriesMediaPart(t *testing.T) {
	wantBytes := []byte{0x89, 0x50, 0x4e, 0x47}

	var (
		mu   sync.Mutex
		seen []session.Message
	)
	observe := func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		seen = req.Messages // last request wins; the single turn here is enough
	}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(observe)}, mockllm.TextTurn("I see it"))
	svc := newService(t, llm, allowRules())
	gclient, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs, err := gclient.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := gclient.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if serr := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
		Prompt: &mecatlv1.Prompt{
			SessionId: cs.GetSessionId(),
			Text:      "what is this",
			Parts:     []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: wantBytes}},
		},
	}}); serr != nil {
		t.Fatalf("Send: %v", serr)
	}
	// Drain events to the terminal result; a clean EOF (not an InvalidArgument)
	// proves the part was accepted and the run ran. The mock turn yields a result.
	var sawResult bool
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			if status.Code(rerr) == codes.InvalidArgument {
				t.Fatalf("valid image part rejected: %v", rerr)
			}
			break // clean EOF
		}
		if resp.GetEvent().GetType() == string(session.EvResult) {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatalf("no terminal result event over the wire (run did not complete)")
	}

	// The load-bearing assertion: the provider actually saw the media part.
	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, msg := range seen {
		if msg.Role != session.RoleUser {
			continue
		}
		for _, part := range msg.Parts {
			if part.Kind == session.MediaImage && bytes.Equal(part.Data, wantBytes) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("provider did not receive the image part across the wire: %+v", seen)
	}
}
