package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// refreshingSource is a TokenRefresher whose tokens are labels, never real
// credentials: tok-0 until the first Refresh, then tok-1, tok-2, ...
type refreshingSource struct {
	mu        sync.Mutex
	refreshes int
}

func (s *refreshingSource) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("tok-%d", s.refreshes), nil
}

func (s *refreshingSource) Refresh(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	return fmt.Sprintf("tok-%d", s.refreshes), nil
}

func (s *refreshingSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

type bearerCall struct{ method, token string }

// bearerServer is a HarnessService fake behind an authenticator that records
// each request's bearer and rejects (Unauthenticated, before any handler)
// while reject > 0.
type bearerServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	mu     sync.Mutex
	calls  []bearerCall
	reject int
	// prompts are the prompt texts Converse handlers received.
	prompts []string
	// failAfterEvent makes Converse send one event and then fail
	// Unauthenticated, as a rejection after the run has started would.
	failAfterEvent bool
	// noBrokerSignIn dials without DialConfig.IdentityBrokerSignIn.
	noBrokerSignIn bool
}

func (s *bearerServer) authenticate(ctx context.Context, method string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	token := strings.TrimPrefix(strings.Join(md.Get("authorization"), ","), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, bearerCall{method: method[strings.LastIndex(method, "/")+1:], token: token})
	if s.reject > 0 {
		s.reject--
		return status.Error(codes.Unauthenticated, "invalid_token")
	}
	return nil
}

func (s *bearerServer) rejectNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = n
}

// tokensFor returns the bearers sent on method, in order.
func (s *bearerServer) tokensFor(method string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if c.method == method {
			out = append(out, c.token)
		}
	}
	return out
}

func (*bearerServer) GetCompatibilityInfo(context.Context, *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	return &mecatlv1.GetCompatibilityInfoResponse{}, nil
}

func (*bearerServer) CreateSession(context.Context, *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	return &mecatlv1.CreateSessionResponse{SessionId: "created"}, nil
}

func (*bearerServer) ForkSession(context.Context, *mecatlv1.ForkSessionRequest) (*mecatlv1.ForkSessionResponse, error) {
	return &mecatlv1.ForkSessionResponse{SessionId: "forked"}, nil
}

func (*bearerServer) ClearSession(context.Context, *mecatlv1.ClearSessionRequest) (*mecatlv1.ClearSessionResponse, error) {
	return &mecatlv1.ClearSessionResponse{SessionId: "cleared"}, nil
}

func (*bearerServer) GetSession(_ context.Context, req *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	return &mecatlv1.GetSessionResponse{Session: &mecatlv1.Session{SessionId: req.GetSessionId()}}, nil
}

func (*bearerServer) CompactSession(context.Context, *mecatlv1.CompactSessionRequest) (*mecatlv1.CompactSessionResponse, error) {
	return &mecatlv1.CompactSessionResponse{}, nil
}

func (*bearerServer) ResolveRunAsk(_ context.Context, req *mecatlv1.ResolveRunAskRequest) (*mecatlv1.ResolveRunAskResponse, error) {
	return &mecatlv1.ResolveRunAskResponse{RunId: req.GetExpectedRunId(), AskId: req.GetAskId()}, nil
}

func (s *bearerServer) Converse(stream grpc.BidiStreamingServer[mecatlv1.ConverseRequest, mecatlv1.ConverseResponse]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.prompts = append(s.prompts, req.GetPrompt().GetText())
	failAfterEvent := s.failAfterEvent
	s.mu.Unlock()
	if err := stream.Send(&mecatlv1.ConverseResponse{Event: &mecatlv1.Event{Type: "message.delta", Text: "hello"}}); err != nil {
		return err
	}
	if failAfterEvent {
		return status.Error(codes.Unauthenticated, "invalid_token")
	}
	return nil
}

func (s *bearerServer) receivedPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// dialBearerServer serves fake over loopback and dials it through Dial, so
// the production interceptors and per-RPC credentials carry the bearer.
func dialBearerServer(t *testing.T, fake *bearerServer, source TokenSource) *Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := fake.authenticate(ctx, info.FullMethod); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := fake.authenticate(ss.Context(), info.FullMethod); err != nil {
				return err
			}
			return handler(srv, ss)
		}),
	)
	mecatlv1.RegisterHarnessServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	c, err := Dial(DialConfig{Server: lis.Addr().String(), TokenSource: source, IdentityBrokerSignIn: !fake.noBrokerSignIn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestWithoutIdentityBrokerSignInSessionOpensDoNotRefresh: a server that does
// not sign users in through AS-1 accepts a token for any number of session
// opens, so create, fork, clear, and a resumed session's first run entry use
// the current token without a forced refresh.
func TestWithoutIdentityBrokerSignInSessionOpensDoNotRefresh(t *testing.T) {
	ctx := t.Context()
	fake := &bearerServer{noBrokerSignIn: true}
	source := &refreshingSource{}
	c := dialBearerServer(t, fake, source)
	if _, _, _, err := c.CreateSession(ctx, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ForkSession(ctx, "created", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ClearSession(ctx, "created", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompactSession(ctx, "resumed"); err != nil {
		t.Fatal(err)
	}
	if got := source.count(); got != 0 {
		t.Fatalf("session opens without identity broker sign-in refreshed %d times, want 0", got)
	}
}

// TestSessionOpeningRequestsCarryAFreshToken: create, fork, carryover fork,
// and clear each force one refresh and carry the refreshed token, so no two
// session opens share a token; the surrounding reads do not refresh.
func TestSessionOpeningRequestsCarryAFreshToken(t *testing.T) {
	ctx := t.Context()
	fake := &bearerServer{}
	source := &refreshingSource{}
	c := dialBearerServer(t, fake, source)

	for _, op := range []struct {
		method string
		open   func() error
	}{
		{"CreateSession", func() error {
			_, _, _, err := c.CreateSession(ctx, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
			return err
		}},
		{"ForkSession", func() error { _, err := c.ForkSession(ctx, "created", "", ""); return err }},
		{"ForkSession", func() error {
			_, _, _, err := c.CreateSessionWithCarryover(ctx, ModelSelection{ModelID: "m"}, "created")
			return err
		}},
		{"ClearSession", func() error { _, _, err := c.ClearSession(ctx, "created", nil); return err }},
	} {
		before := source.count()
		if err := op.open(); err != nil {
			t.Fatalf("%s: %v", op.method, err)
		}
		if got := source.count() - before; got != 1 {
			t.Fatalf("%s refreshed %d times, want 1", op.method, got)
		}
		tokens := fake.tokensFor(op.method)
		if latest, _ := source.Token(ctx); len(tokens) == 0 || tokens[len(tokens)-1] != latest {
			t.Fatalf("%s did not carry the refreshed token", op.method)
		}
	}
	seen := map[string]bool{}
	for _, method := range []string{"CreateSession", "ForkSession", "ClearSession"} {
		for _, token := range fake.tokensFor(method) {
			if seen[token] {
				t.Fatalf("two session opens carried the same token (%s)", method)
			}
			seen[token] = true
		}
	}
}

// TestResumedSessionFirstRunEntryRefreshesOnce: a session this client did not
// open gets one fresh token on its first run-entry request, whichever kind
// comes first; a session it created does not.
func TestResumedSessionFirstRunEntryRefreshesOnce(t *testing.T) {
	ctx := t.Context()
	fake := &bearerServer{}
	source := &refreshingSource{}
	c := dialBearerServer(t, fake, source)

	if _, err := c.CompactSession(ctx, "resumed"); err != nil {
		t.Fatal(err)
	}
	if got := source.count(); got != 1 {
		t.Fatalf("first run entry for a resumed session refreshed %d times, want 1", got)
	}
	if _, err := c.CompactSession(ctx, "resumed"); err != nil {
		t.Fatal(err)
	}
	approval := PendingApproval{SessionID: "resumed", RunID: "run", AskID: "ask"}
	if err := c.ResolvePendingApproval(ctx, approval, VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	if got := source.count(); got != 1 {
		t.Fatalf("later run entries refreshed again: %d refreshes, want 1", got)
	}

	approval.SessionID = "resumed-2"
	if err := c.ResolvePendingApproval(ctx, approval, VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	if got := source.count(); got != 2 {
		t.Fatalf("first ResolveRunAsk for another resumed session: %d refreshes, want 2", got)
	}

	id, _, _, err := c.CreateSession(ctx, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	afterCreate := source.count()
	runConverse(t, c, id)
	if got := source.count(); got != afterCreate {
		t.Fatalf("first run entry after create refreshed %d times, want 0", got-afterCreate)
	}
}

// TestRejectedBearerRetriesOnceWithAFreshToken: one Unauthenticated is
// answered by one refresh and one retry; a second is surfaced as the
// rejected-bearer auth failure without a further retry.
func TestRejectedBearerRetriesOnceWithAFreshToken(t *testing.T) {
	ctx := t.Context()
	fake := &bearerServer{}
	source := &refreshingSource{}
	c := dialBearerServer(t, fake, source)

	fake.rejectNext(1)
	if _, err := c.GetSession(ctx, "s"); err != nil {
		t.Fatalf("GetSession after one rejection: %v", err)
	}
	tokens := fake.tokensFor("GetSession")
	if len(tokens) != 2 || tokens[0] == tokens[1] || source.count() != 1 {
		t.Fatalf("one rejection: %d attempts, %d refreshes, distinct=%v; want 2, 1, true", len(tokens), source.count(), len(tokens) == 2 && tokens[0] != tokens[1])
	}

	fake.rejectNext(2)
	_, err := c.GetSession(ctx, "s")
	if reason, ok := AuthFailure(err, true); !ok || reason != AuthRejected {
		t.Fatalf("two rejections: AuthFailure = %q, %v; want %q", reason, ok, AuthRejected)
	}
	if got := len(fake.tokensFor("GetSession")); got != 4 || source.count() != 2 {
		t.Fatalf("two rejections: %d total attempts, %d refreshes; want 4, 2", got, source.count())
	}
}

// TestStaticTokenIsNeitherRefreshedNorRetried: a source without Refresh keeps
// today's behaviour.
func TestStaticTokenIsNeitherRefreshedNorRetried(t *testing.T) {
	ctx := t.Context()
	fake := &bearerServer{}
	c := dialBearerServer(t, fake, tokenSourceFunc(func(context.Context) (string, error) { return "static", nil }))
	if _, _, _, err := c.CreateSession(ctx, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{}); err != nil {
		t.Fatal(err)
	}
	fake.rejectNext(1)
	_, err := c.CompactSession(ctx, "resumed")
	if reason, ok := AuthFailure(err, true); !ok || reason != AuthRejected {
		t.Fatalf("AuthFailure = %q, %v; want %q", reason, ok, AuthRejected)
	}
	if got := fake.tokensFor("CompactSession"); len(got) != 1 {
		t.Fatalf("static token retried: %d attempts, want 1", len(got))
	}
	if got := fake.tokensFor("CreateSession"); len(got) != 1 || got[0] != "static" {
		t.Fatal("CreateSession did not carry the static token")
	}
}

// runConverse opens a run on id, sends one prompt, and returns every message
// the read loop produced.
func runConverse(t *testing.T, c *Client, id string) []tea.Msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := c.OpenConverseForSession(ctx, id)
	if err != nil {
		t.Fatalf("open converse: %v", err)
	}
	out := make(chan tea.Msg, 16)
	go stream.ReadLoop(ctx, out)
	if err := stream.SendPrompt(id, "hello", nil); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	var msgs []tea.Msg
	for m := range out {
		msgs = append(msgs, m)
	}
	return msgs
}

// endedSender is the send side of a stream the server already ended.
type endedSender struct{}

func (endedSender) Send(*mecatlv1.ConverseRequest) error { return io.EOF }

// TestStreamReplaysAFrameSentAfterTheRejection pins the ordering gRPC does not
// guarantee: the prompt is sent after the server ended the stream (Send sees
// io.EOF), and the reopen replays it on the replacement exactly once.
func TestStreamReplaysAFrameSentAfterTheRejection(t *testing.T) {
	rejected := newFakeStream()
	rejected.endErr = status.Error(codes.Unauthenticated, "invalid_token")
	replacement := newFakeStream(resp(&mecatlv1.Event{Type: "message.delta", Text: "hi"}))
	s := newAuthenticatedStream(rejected, endedSender{}, true)
	reopens := 0
	s.enableReopen(func() (Recver, Sender, error) { reopens++; return replacement, replacement, nil })

	if err := s.SendPrompt("s", "hello", nil); err != nil {
		t.Fatalf("SendPrompt on an ended, reopenable stream: %v", err)
	}
	out := make(chan tea.Msg, 4)
	s.ReadLoop(t.Context(), out)
	var msgs []tea.Msg
	for m := range out {
		msgs = append(msgs, m)
	}
	if len(msgs) != 2 || reopens != 1 {
		t.Fatalf("messages = %#v after %d reopens, want the delta and the close after 1", msgs, reopens)
	}
	if sent := replacement.sentFrames(); len(sent) != 1 || sent[0].GetPrompt().GetText() != "hello" {
		t.Fatalf("replacement received %d frames, want the prompt once", len(sent))
	}
	// After the first response, frames are no longer recorded and go straight
	// to the replacement.
	if err := s.SendCancel(); err != nil {
		t.Fatalf("SendCancel on the replacement: %v", err)
	}
	if got := len(replacement.sentFrames()); got != 2 {
		t.Fatalf("replacement received %d frames, want 2", got)
	}
}

// TestConverseReopensOnceBeforeAnyEvent: a Converse stream rejected before
// any event is reopened once with a fresh token and the prompt is replayed; a
// second rejection, or one after an event, is surfaced and not retried.
func TestConverseReopensOnceBeforeAnyEvent(t *testing.T) {
	t.Run("one rejection", func(t *testing.T) {
		fake := &bearerServer{}
		source := &refreshingSource{}
		c := dialBearerServer(t, fake, source)
		c.noteOpened("s") // not resumed: only the retry refreshes
		fake.rejectNext(1)
		msgs := runConverse(t, c, "s")
		if len(msgs) != 2 {
			t.Fatalf("messages = %#v, want the delta and the close", msgs)
		}
		if _, ok := msgs[0].(AssistantDeltaMsg); !ok {
			t.Fatalf("first message = %#v, want the replacement stream's delta", msgs[0])
		}
		if _, ok := msgs[1].(StreamClosedMsg); !ok {
			t.Fatalf("last message = %#v, want StreamClosedMsg", msgs[1])
		}
		tokens := fake.tokensFor("Converse")
		if len(tokens) != 2 || tokens[0] == tokens[1] || source.count() != 1 {
			t.Fatalf("%d stream attempts, %d refreshes; want 2, 1 with distinct tokens", len(tokens), source.count())
		}
		if got := fake.receivedPrompts(); len(got) != 1 || got[0] != "hello" {
			t.Fatalf("prompts received = %q, want the replayed prompt once", got)
		}
	})
	t.Run("two rejections", func(t *testing.T) {
		fake := &bearerServer{}
		source := &refreshingSource{}
		c := dialBearerServer(t, fake, source)
		c.noteOpened("s")
		fake.rejectNext(2)
		msgs := runConverse(t, c, "s")
		if len(msgs) != 1 {
			t.Fatalf("messages = %#v, want one stream error", msgs)
		}
		if m, ok := msgs[0].(StreamErrMsg); !ok || m.AuthReason != AuthRejected {
			t.Fatalf("message = %#v, want StreamErrMsg with %q", msgs[0], AuthRejected)
		}
		if got := len(fake.tokensFor("Converse")); got != 2 || source.count() != 1 {
			t.Fatalf("%d stream attempts, %d refreshes; want 2, 1", got, source.count())
		}
	})
	t.Run("rejection after an event", func(t *testing.T) {
		fake := &bearerServer{failAfterEvent: true}
		source := &refreshingSource{}
		c := dialBearerServer(t, fake, source)
		c.noteOpened("s")
		msgs := runConverse(t, c, "s")
		if len(msgs) != 2 {
			t.Fatalf("messages = %#v, want the delta and one stream error", msgs)
		}
		if m, ok := msgs[1].(StreamErrMsg); !ok || m.AuthReason != AuthRejected {
			t.Fatalf("message = %#v, want StreamErrMsg with %q", msgs[1], AuthRejected)
		}
		if got := len(fake.tokensFor("Converse")); got != 1 || source.count() != 0 {
			t.Fatalf("%d stream attempts, %d refreshes; want 1, 0", got, source.count())
		}
	})
}
