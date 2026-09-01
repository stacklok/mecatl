package ui

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/statusline"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type headerDebugServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	ids []string

	mu     sync.Mutex
	target string
}

func (s *headerDebugServer) ListSessions(context.Context, *mecatlv1.ListSessionsRequest) (*mecatlv1.ListSessionsResponse, error) {
	items := make([]*mecatlv1.SessionSummary, 0, len(s.ids))
	for _, id := range s.ids {
		items = append(items, &mecatlv1.SessionSummary{SessionId: id})
	}
	return &mecatlv1.ListSessionsResponse{Sessions: items}, nil
}

func (s *headerDebugServer) CreateSession(_ context.Context, req *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	s.mu.Lock()
	s.target = req.GetDebugTargetSessionId()
	s.mu.Unlock()
	return &mecatlv1.CreateSessionResponse{
		SessionId:    "debug-created",
		Capabilities: &mecatlv1.ServerCapabilities{SessionDebug: true},
	}, nil
}

func (s *headerDebugServer) createdTarget() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

func TestPredictableSessionHandles_Scenario2_RealHeaderHandleCreatesBoundDebugger(t *testing.T) {
	ids := []string{
		"0123456789abcdef0123456789abcdef",
		"legacy\x1b-session-é",
	}
	service := &headerDebugServer{ids: ids}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, service)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	cl, err := client.Dial(client.DialConfig{Server: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	for _, fullID := range ids {
		t.Run(fullID, func(t *testing.T) {
			m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background()})
			m.sessionID = fullID
			m.width = 160
			header := stripANSIstr(m.renderHeader())
			_, visible, ok := strings.Cut(header, "session ")
			if !ok {
				t.Fatalf("normal header has no session segment: %q", header)
			}
			headerLiteral, _, ok := strings.Cut(visible, "  ·  ")
			if !ok || headerLiteral == "" {
				t.Fatalf("normal header has no literal handle boundary: %q", header)
			}

			_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), headerLiteral, 0, client.ModelSelection{})
			if err != nil {
				t.Fatal(err)
			}
			if target := service.createdTarget(); resolved != fullID || target != fullID {
				t.Fatalf("visible header literal %q resolved=%q request target=%q, want exact ID %q", headerLiteral, resolved, target, fullID)
			}
		})
	}
}

func TestPredictableSessionHandles_Scenario1_SharedNormalHandle(t *testing.T) {
	const id = "sess-01JABCDEFGH-rest"
	const want = "sess-01JABCD"
	if got := client.SessionHandle(id); got != want {
		t.Fatalf("SessionHandle(%q) = %q, want first twelve characters %q", id, got, want)
	}

	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background()})
	m.sessionID = id
	m.sessionTitle = "ordinary session"
	if got := stripANSIstr(m.renderHeader()); !strings.Contains(got, "session "+want) || strings.Contains(got, "#"+want) {
		t.Fatalf("header does not use bare handle %q: %q", want, got)
	}
	if got := m.windowTitle(); !strings.Contains(got, " "+want+" — ") || strings.Contains(got, "#"+want) {
		t.Fatalf("ordinary window title = %q, want bare handle %q", got, want)
	}
	m.deps.DebugTarget = id
	if got := stripANSIstr(m.renderHeader()); !strings.Contains(got, "DEBUG target "+want) || strings.Contains(got, "#"+want) {
		t.Fatalf("debugger target chrome does not use bare handle %q: %q", want, got)
	}
	if got := m.windowTitle(); !strings.HasPrefix(got, "DEBUG "+want+" — ") {
		t.Fatalf("debug window title = %q, want debugger handle %q", got, want)
	}

	st := newSessionsPanelState()
	st.loading = false
	st.loadState = sessionsComplete
	st.sessions = []client.SessionListItem{{ID: id, Title: "ordinary session", Kind: client.SessionKindMain}}
	st.syncFilter()
	rows := stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{}, helpKeys{}, 100, 30, ""))
	if !strings.Contains(rows, "  "+want) || strings.Contains(rows, "#"+want) {
		t.Fatalf("/sessions row does not use bare handle %q: %q", want, rows)
	}

	m.deps.DebugTarget = ""
	if got := m.statusLineInput(time.Unix(1, 0)).Session.Handle; got != want {
		t.Fatalf("status input handle = %q, want %q", got, want)
	}
}

func TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior(t *testing.T) {
	const prefix = "same-prefix-"
	ids := []string{prefix + "first", prefix + "second"}
	if len(prefix) != client.SessionHandleWidth {
		t.Fatalf("test prefix width = %d, want %d", len(prefix), client.SessionHandleWidth)
	}
	for _, id := range ids {
		if got := client.SessionHandle(id); got != prefix {
			t.Fatalf("SessionHandle(%q) = %q, want fixed collision %q", id, got, prefix)
		}
	}

	projections := [][]client.SessionListItem{
		{{ID: ids[0]}},
		{{ID: ids[0]}, {ID: ids[1]}},
		{{ID: ids[1]}, {ID: ids[0]}},
	}
	for _, rows := range projections {
		handles := sessionDisplayHandles(rows)
		for _, row := range rows {
			if handles[row.ID] != prefix {
				t.Fatalf("inventory/order expanded %q to %q", row.ID, handles[row.ID])
			}
		}
	}
}

func TestPredictableSessionHandles_Scenario1_EscapedUTF8AndControls(t *testing.T) {
	tests := []struct {
		name, id, want string
	}{
		{"empty", "", ""},
		{"exactly twelve safe", "abcdefghijkl", "abcdefghijkl"},
		{"normal cap", "abcdefghijklmnop", "abcdefghijkl"},
		{"escaped byte exactly fits", "123456789$tail", "123456789%24"},
		{"escaped byte cannot fit", "1234567890$tail", "1234567890"},
		{"shell significant", "abc$def;ghi", "abc%24def%3B"},
		{"controls", "a\x1b\n\tb", "a%1B%0A%09b"},
		{"multibyte utf8", "éclair", "%C3%A9clair"},
		{"multibyte byte atom boundary", "123456789é", "123456789%C3"},
		{"invalid utf8", string([]byte{0xff, 'a', 'b', 'c'}), ""},
		{"literal percent escaped", "100% ready", "100%25%20rea"},
	}
	allowed := regexp.MustCompile(`^[A-Za-z0-9._%-]*$`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := client.SessionHandle(tc.id)
			if got != tc.want {
				t.Fatalf("SessionHandle(%q) = %q, want %q", tc.id, got, tc.want)
			}
			if len(got) > client.SessionHandleWidth || !allowed.MatchString(got) {
				t.Fatalf("handle %q violates fixed ASCII grammar", got)
			}
		})
	}
}

func TestADR_0278_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles(t *testing.T) {
	const id = "legacy\x1b/$雪-session"
	want := client.SessionHandle(id)
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: context.Background(), DebugTarget: id})
	m.sessionID = id
	m.sessionTitle = "debug"

	presentations := map[string]string{
		"header":         stripANSIstr(m.renderHeader()),
		"debugger title": m.windowTitle(),
	}
	st := newSessionsPanelState()
	st.loading, st.loadState = false, sessionsComplete
	st.sessions = []client.SessionListItem{{ID: id, Title: "legacy", Kind: client.SessionKindMain}}
	st.syncFilter()
	presentations["sessions"] = stripANSIstr(renderSessionsPanel(testTheme(), st, client.Capabilities{}, helpKeys{}, 100, 30, ""))
	for name, rendered := range presentations {
		if !strings.Contains(rendered, want) || strings.Contains(rendered, "#"+want) || strings.Contains(rendered, "\x1b") {
			t.Fatalf("%s does not use terminal-safe shared handle %q: %q", name, want, rendered)
		}
	}

	m.deps.DebugTarget = ""
	input := m.statusLineInput(time.Unix(1, 0))
	if input.Version != 2 || input.Session.Handle != want {
		t.Fatalf("status protocol = v%d handle %q, want v2 %q", input.Version, input.Session.Handle, want)
	}
	if _, exists := reflect.TypeFor[statusline.Session]().FieldByName("Digest"); exists {
		t.Fatal("status protocol retains removed Session.Digest alias")
	}
	wire, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"Handle":"`+want+`"`) || strings.Contains(string(wire), `"Digest"`) {
		t.Fatalf("status command JSON does not expose only Session.Handle: %s", wire)
	}
	source := statusline.NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	input.Terminal.HeaderAvailCols = 100
	source.Submit(input)
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("shipped StatusML template did not publish")
	}
	var shipped strings.Builder
	for _, span := range source.Latest().Header.Spans {
		shipped.WriteString(span.Text)
	}
	if got := shipped.String(); !strings.Contains(got, "session "+want) || strings.Contains(got, "#"+want) {
		t.Fatalf("shipped template does not use bare handle %q: %q", want, got)
	}

	// Ordinary presentation changes must not rewrite exact IDs or evidence digests.
	m.deps.DebugTarget = id
	if got := m.sessionDetails().DebugTargetID; got != id {
		t.Fatalf("debug target exact ID changed from %q to %q", id, got)
	}
	evidence := client.LearningEvidence{SessionID: id, Digest: strings.Repeat("d", 64)}
	if evidence.SessionID != id || evidence.Digest != strings.Repeat("d", 64) {
		t.Fatalf("debugger evidence identity/digest was projected as ordinary handle: %#v", evidence)
	}
}
