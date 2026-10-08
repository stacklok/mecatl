package grpcdriver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
)

// hostileCommandServer is a RAW driverv1 server so the client's defensive
// normalization can be fed wire data the harness wrapper could never produce:
// grammar-invalid names, duplicates, unsorted order, oversized multi-line
// descriptions — or outright faults.
type hostileCommandServer struct {
	driverv1.UnimplementedCommandSourceServiceServer
	commands []*driverv1.CommandMeta
	listErr  error
	bodyErr  error
}

func (s *hostileCommandServer) ListCommands(context.Context, *driverv1.ListCommandsRequest) (*driverv1.ListCommandsResponse, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &driverv1.ListCommandsResponse{Commands: s.commands}, nil
}

func (s *hostileCommandServer) GetCommandBody(_ context.Context, req *driverv1.GetCommandBodyRequest) (*driverv1.GetCommandBodyResponse, error) {
	if s.bodyErr != nil {
		return nil, s.bodyErr
	}
	return &driverv1.GetCommandBodyResponse{Body: "body of " + req.GetName()}, nil
}

func newHostileCommandClient(t *testing.T, srv *hostileCommandServer, diag port.Diagnostics) *CommandSource {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterCommandSourceServiceServer(gs, srv)
	})
	return NewCommandSource(conn, CommandOptions{Diagnostics: diag})
}

// TestCommandSourceListDefensiveDiscipline pins the client discipline on
// List: drop grammar-invalid names (they could never be invoked), de-dup
// first-wins, sort by name, force descriptions single-line then rune-cap to
// prompt.MaxCommandDescriptionRunes.
func TestCommandSourceListDefensiveDiscipline(t *testing.T) {
	long := strings.Repeat("é", prompt.MaxCommandDescriptionRunes*2) // multibyte: rune cap, not byte cap
	src := newHostileCommandClient(t, &hostileCommandServer{commands: []*driverv1.CommandMeta{
		{Name: "zeta", Description: "last alphabetically"},
		{Name: "has space", Description: "grammar violator must be dropped"},
		{Name: "", Description: "blank name must be dropped"},
		{Name: "dup", Description: "first wins"},
		{Name: "dup", Description: "second loses"},
		{Name: "alpha", Description: long},
		{Name: "multi", Description: "line one\nline two\ttabbed"},
	}}, nil)
	got, err := src.ListCommands(context.Background())
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListCommands kept %d, want 4 (invalid/blank dropped, dup de-duped): %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "dup" || got[2].Name != "multi" || got[3].Name != "zeta" {
		t.Errorf("not name-sorted: %+v", got)
	}
	if runes := len([]rune(got[0].Description)); runes > prompt.MaxCommandDescriptionRunes {
		t.Errorf("description not rune-capped: %d runes > %d", runes, prompt.MaxCommandDescriptionRunes)
	}
	if !strings.HasSuffix(got[0].Description, "…") {
		t.Errorf("capped description should end with the ellipsis: %q", got[0].Description)
	}
	if got[1].Description != "first wins" {
		t.Errorf("de-dup kept %q, want the FIRST wire entry", got[1].Description)
	}
	if strings.ContainsAny(got[2].Description, "\n\t") {
		t.Errorf("multi-line description leaked control characters: %q", got[2].Description)
	}
}

// TestCommandSourceNotFoundIsNormal pins the unknown-command mapping over the
// REAL harness wrapper: a driver NOT_FOUND is ("", false, nil) — the normal
// pass-through, never an error.
func TestCommandSourceNotFoundIsNormal(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterCommandSourceServiceServer(gs, NewCommandSourceServer(sourceconformance.NewCommandFixtureSource()))
	})
	src := NewCommandSource(conn, CommandOptions{})
	body, found, err := src.CommandBody(context.Background(), "no-such-command")
	if err != nil || found || body != "" {
		t.Errorf("CommandBody(unknown) = (%q, %v, %v), want (\"\", false, nil)", body, found, err)
	}
}

// TestCommandSourceRuntimeFaultFailsSoft pins the LIVE fail-soft posture: a
// runtime driver fault degrades List to (nil, nil) and CommandBody to
// ("", false, nil), each with a WARN through the injected Diagnostics — a
// transient blip must never latch a command "missing" nor abort a run
// (MultiExpander.List aborts the whole palette walk on a child error).
func TestCommandSourceRuntimeFaultFailsSoft(t *testing.T) {
	boom := status.Error(codes.Unavailable, "driver down")
	diag := &recordingDiag{}
	src := newHostileCommandClient(t, &hostileCommandServer{listErr: boom, bodyErr: boom}, diag)

	cmds, err := src.ListCommands(context.Background())
	if err != nil || cmds != nil {
		t.Errorf("ListCommands(fault) = (%v, %v), want (nil, nil) fail-soft", cmds, err)
	}
	if !diag.find(port.LevelWarn, "ListCommands failed") {
		t.Errorf("List fault must WARN through Diagnostics; got %+v", diag.entries)
	}

	body, found, err := src.CommandBody(context.Background(), "review")
	if err != nil || found || body != "" {
		t.Errorf("CommandBody(fault) = (%q, %v, %v), want (\"\", false, nil) fail-soft", body, found, err)
	}
	if !diag.find(port.LevelWarn, "GetCommandBody failed") {
		t.Errorf("body fault must WARN through Diagnostics; got %+v", diag.entries)
	}
}

// TestCommandSourceProbe pins the build-time posture: Probe surfaces a driver
// fault as a non-nil error (the composition layer treats it as FATAL) and
// passes on a healthy driver.
func TestCommandSourceProbe(t *testing.T) {
	healthy := newHostileCommandClient(t, &hostileCommandServer{}, nil)
	if err := healthy.Probe(context.Background()); err != nil {
		t.Errorf("Probe(healthy) = %v, want nil", err)
	}
	broken := newHostileCommandClient(t, &hostileCommandServer{listErr: status.Error(codes.Unavailable, "down")}, nil)
	if err := broken.Probe(context.Background()); err == nil {
		t.Error("Probe(faulting driver) = nil, want the wrapped RPC error")
	}
}

// TestCommandSourceCtxRewrap pins the §H context row: a caller-cancelled ctx
// surfaces (it is NOT swallowed by the fail-soft branch) so
// errors.Is(err, context.Canceled) holds harness-side.
func TestCommandSourceCtxRewrap(t *testing.T) {
	src := newHostileCommandClient(t, &hostileCommandServer{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.ListCommands(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListCommands(cancelled ctx) = %v, want errors.Is(_, context.Canceled)", err)
	}
	if _, _, err := src.CommandBody(ctx, "review"); !errors.Is(err, context.Canceled) {
		t.Errorf("CommandBody(cancelled ctx) = %v, want errors.Is(_, context.Canceled)", err)
	}
}

// TestCommandSourceBlankNameInvalidArgument pins BOTH halves of the §H
// blank-name row over the REAL harness wrapper (the C1 lesson — assert the
// actual wire code, not "some error"): server-side, a blank name is
// pre-validated to INVALID_ARGUMENT before the source is consulted;
// client-side, that surfaces as a NON-NIL error (a programming error, NOT
// swallowed into the runtime fail-soft branch — and therefore NO WARN).
func TestCommandSourceBlankNameInvalidArgument(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterCommandSourceServiceServer(gs, NewCommandSourceServer(sourceconformance.NewCommandFixtureSource()))
	})

	// Server-side: the raw generated client sees INVALID_ARGUMENT on the wire.
	raw := driverv1.NewCommandSourceServiceClient(conn)
	_, rawErr := raw.GetCommandBody(context.Background(), &driverv1.GetCommandBodyRequest{Name: ""})
	if got := status.Code(rawErr); got != codes.InvalidArgument {
		t.Fatalf("server GetCommandBody(blank) code = %v (err %v), want InvalidArgument (pre-validated before the source)", got, rawErr)
	}

	// Client-side: the harness client returns a non-nil error — NOT the
	// fail-soft ("",false,nil) degradation — and logs NO fail-soft WARN.
	diag := &recordingDiag{}
	src := NewCommandSource(conn, CommandOptions{Diagnostics: diag})
	body, found, err := src.CommandBody(context.Background(), "")
	if err == nil {
		t.Fatal("client CommandBody(blank) error = nil; an INVALID_ARGUMENT must surface, never fail-soft")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("client CommandBody(blank) code = %v, want InvalidArgument preserved through the rewrap", got)
	}
	if found || body != "" {
		t.Errorf("client CommandBody(blank) = (%q, %v), want (\"\", false)", body, found)
	}
	if diag.find(port.LevelWarn, "GetCommandBody failed") {
		t.Errorf("blank-name INVALID_ARGUMENT must not take the fail-soft WARN branch; got %+v", diag.entries)
	}
}
