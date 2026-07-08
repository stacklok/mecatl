package client

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeListSessionsClient is a scripted HarnessServiceClient for the ListSessions
// wrapper tests (issue #245 Phase 2). Like fakeWorktreesClient it embeds the
// interface and overrides only the one RPC under test, so the proto→plain
// mapping runs offline.
type fakeListSessionsClient struct {
	mecatlv1.HarnessServiceClient

	resp *mecatlv1.ListSessionsResponse
	err  error

	lastReq *mecatlv1.ListSessionsRequest
}

func (f *fakeListSessionsClient) ListSessions(_ context.Context, in *mecatlv1.ListSessionsRequest, _ ...grpc.CallOption) (*mecatlv1.ListSessionsResponse, error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// TestListSessionsFromProto is the table test for the nil-safe mapper: a
// populated slice, a nil slice, and a slice containing a nil summary all map
// without panic (the nil summary yields the zero SessionListItem).
func TestListSessionsFromProto(t *testing.T) {
	cases := []struct {
		name string
		in   []*mecatlv1.SessionSummary
		want []SessionListItem
	}{
		{
			"populated",
			[]*mecatlv1.SessionSummary{
				{SessionId: "s1", ModifiedAtUnix: 1700000000, State: "completed", Turns: 5, ModelId: "openai/gpt-4.5", CreatedAtUnix: 1699999000, Title: "Fix the CI"},
				{SessionId: "s2", ModifiedAtUnix: 1700000001, State: "idle", Turns: 0, ModelId: "anthropic/claude-3.5"},
			},
			[]SessionListItem{
				{ID: "s1", ModifiedAt: 1700000000, State: "completed", Turns: 5, ModelID: "openai/gpt-4.5", CreatedAt: 1699999000, Title: "Fix the CI"},
				{ID: "s2", ModifiedAt: 1700000001, State: "idle", Turns: 0, ModelID: "anthropic/claude-3.5"},
			},
		},
		{"nil slice", nil, []SessionListItem{}},
		{
			"nil summary in slice",
			[]*mecatlv1.SessionSummary{nil, {SessionId: "s3"}},
			[]SessionListItem{{}, {ID: "s3"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listSessionsFromProto(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got %#v)", len(got), len(tc.want), got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestListSessionsWrapper asserts the wrapper calls the RPC and maps the result.
func TestListSessionsWrapper(t *testing.T) {
	fake := &fakeListSessionsClient{resp: &mecatlv1.ListSessionsResponse{Sessions: []*mecatlv1.SessionSummary{
		{SessionId: "s1", ModifiedAtUnix: 100, State: "idle", Turns: 3, ModelId: "m1", CreatedAtUnix: 50},
	}}}
	cl := newFakeClient(fake)

	got, err := cl.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if fake.lastReq == nil {
		t.Fatal("no request captured")
	}
	if len(got) != 1 || got[0].ID != "s1" || got[0].ModifiedAt != 100 || got[0].State != "idle" || got[0].Turns != 3 || got[0].ModelID != "m1" || got[0].CreatedAt != 50 {
		t.Errorf("got = %+v", got)
	}
}

// TestListSessionsWrapperCarriesTitle asserts the wrapper maps the proto Title
// onto the SessionListItem (nil-safe for an absent/empty field).
func TestListSessionsWrapperCarriesTitle(t *testing.T) {
	fake := &fakeListSessionsClient{resp: &mecatlv1.ListSessionsResponse{Sessions: []*mecatlv1.SessionSummary{
		{SessionId: "s1", Title: "My session title"},
		{SessionId: "s2"}, // no title — stays empty
	}}}
	cl := newFakeClient(fake)
	got, err := cl.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Title != "My session title" {
		t.Errorf("got[0].Title = %q, want %q", got[0].Title, "My session title")
	}
	if got[1].Title != "" {
		t.Errorf("got[1].Title = %q, want empty (absent field)", got[1].Title)
	}
}

// TestListSessionsCmdSuccess asserts the cmd returns a SessionsListedMsg with
// the mapped sessions on success.
func TestListSessionsCmdSuccess(t *testing.T) {
	fake := &fakeListSessionsClient{resp: &mecatlv1.ListSessionsResponse{Sessions: []*mecatlv1.SessionSummary{
		{SessionId: "s1", State: "idle"},
	}}}
	cl := newFakeClient(fake)

	msg := ListSessionsCmd(context.Background(), cl)()
	lm, ok := msg.(SessionsListedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SessionsListedMsg", msg)
	}
	if lm.Err != nil {
		t.Fatalf("unexpected err: %v", lm.Err)
	}
	if len(lm.Sessions) != 1 || lm.Sessions[0].ID != "s1" {
		t.Fatalf("sessions = %+v", lm.Sessions)
	}
}

// TestListSessionsCmdError asserts the cmd returns a SessionsListedMsg with Err
// set and a nil session list on failure (distinct from empty-list success).
func TestListSessionsCmdError(t *testing.T) {
	fake := &fakeListSessionsClient{err: errors.New("boom")}
	cl := newFakeClient(fake)

	msg := ListSessionsCmd(context.Background(), cl)()
	lm, ok := msg.(SessionsListedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want SessionsListedMsg", msg)
	}
	if lm.Err == nil {
		t.Fatal("expected err, got nil")
	}
	if lm.Sessions != nil {
		t.Fatalf("expected nil sessions on err, got %+v", lm.Sessions)
	}
}
