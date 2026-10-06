package grpcdriver

import (
	"context"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
)

// CommandOptions configures a remote CommandSource client.
type CommandOptions struct {
	// Diagnostics is the operational-logging sink for the RUNTIME fail-soft
	// branches (a driver fault during a run degrades to "no commands from this
	// source" with a WARN, never an aborted run). nil defaults to
	// port.NopDiagnostics.
	Diagnostics port.Diagnostics
}

// CommandSource is a prompt.CommandSource over a remote CommandSourceService
// driver. LIVE semantics: every Expand/List consults the driver (no snapshot,
// matching the file expander's reads-current-files discipline) — and
// deliberately NO latching: a transient fault must not latch a command
// "missing", and grammar-invalid names are re-dropped statelessly per List.
//
// Defensive normalization on List (the driver is operator infrastructure, but
// the metadata feeds the palette): names that violate the invocation grammar
// are DROPPED (prompt.ValidCommandName — they could never be invoked),
// duplicates de-dup first-wins, the result is name-sorted, and descriptions
// are forced single-line then rune-capped to prompt.MaxCommandDescriptionRunes.
//
// Fault posture: a RUNTIME fault fail-softs — ListCommands → (nil, nil) +
// WARN (prompt.MultiExpander.List aborts the whole palette walk on a child
// error, so a transient driver blip must not propagate), CommandBody →
// ("", false, nil) + WARN (the raw input passes through). A caller-cancelled
// ctx still surfaces (rpcErr rewraps so errors.Is(ctx.Err()) holds), as does
// the server's INVALID_ARGUMENT pre-validation (a programming error, not a
// blip). The BUILD-time reachability check is the separate Probe
// (loud-misconfig posture, fatal in the composition layer).
type CommandSource struct {
	client driverv1.CommandSourceServiceClient
	diag   port.Diagnostics
}

// compile-time assertion that CommandSource satisfies the prompt port.
var _ prompt.CommandSource = (*CommandSource)(nil)

// NewCommandSource wraps an established driver connection (see Dial) as a
// prompt.CommandSource.
func NewCommandSource(conn grpc.ClientConnInterface, opts CommandOptions) *CommandSource {
	diag := opts.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &CommandSource{client: driverv1.NewCommandSourceServiceClient(conn), diag: diag}
}

// ListCommands returns the driver's CURRENT command metadata, defensively
// normalized (see the type doc). A runtime driver fault degrades to
// (nil, nil) with a WARN.
func (s *CommandSource) ListCommands(ctx context.Context) ([]prompt.Command, error) {
	resp, err := s.client.ListCommands(ctx, &driverv1.ListCommandsRequest{})
	if err != nil {
		if ctx.Err() != nil {
			return nil, rpcErr(ctx, "list commands", err)
		}
		s.diag.Log(ctx, port.LevelWarn, "slash commands: driver ListCommands failed; no driver commands this call (fail-soft)",
			"err", rpcErr(ctx, "list commands", err))
		return nil, nil
	}
	wire := resp.GetCommands()
	out := make([]prompt.Command, 0, len(wire))
	seen := make(map[string]bool, len(wire))
	for _, c := range wire {
		name := c.GetName()
		if !prompt.ValidCommandName(name) {
			continue // drop grammar violators: they could never be invoked as "/<name>"
		}
		if seen[name] {
			continue // de-dup first-wins (wire order)
		}
		seen[name] = true
		out = append(out, prompt.Command{
			Name:        name,
			Description: capRunes(singleLine(c.GetDescription()), prompt.MaxCommandDescriptionRunes),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CommandBody returns the named command's RAW template. A driver NOT_FOUND is
// the NORMAL unknown-command outcome (found=false, nil error → the input
// passes through unchanged); a runtime fault degrades the same way with a
// WARN; a caller-cancelled ctx and a server INVALID_ARGUMENT (blank name —
// pre-validated server-side) surface as non-nil errors.
func (s *CommandSource) CommandBody(ctx context.Context, name string) (string, bool, error) {
	resp, err := s.client.GetCommandBody(ctx, &driverv1.GetCommandBodyRequest{Name: name})
	if err != nil {
		switch {
		case status.Code(err) == codes.NotFound:
			return "", false, nil // unknown name is NORMAL, never an error
		case ctx.Err() != nil:
			return "", false, rpcErr(ctx, "get command body", err)
		case status.Code(err) == codes.InvalidArgument:
			return "", false, rpcErr(ctx, "get command body", err)
		default:
			s.diag.Log(ctx, port.LevelWarn, "slash commands: driver GetCommandBody failed; input passes through unchanged (fail-soft)",
				"command", name, "err", rpcErr(ctx, "get command body", err))
			return "", false, nil
		}
	}
	return resp.GetBody(), true, nil
}

// Probe performs the BUILD-time reachability check: one ListCommands round
// trip, returning the wrapped RPC error on a fault (the composition layer
// treats it as FATAL — an explicitly configured driver that cannot answer is
// a misconfiguration, unlike a runtime blip which the live calls degrade on).
// The command set itself is NOT judged: an empty set is a legal "no commands".
func (s *CommandSource) Probe(ctx context.Context) error {
	if _, err := s.client.ListCommands(ctx, &driverv1.ListCommandsRequest{}); err != nil {
		return rpcErr(ctx, "list commands", err)
	}
	return nil
}

// capRunes truncates s to at most limit RUNES, appending "…" (which counts
// toward the limit) when it overflows — the same rendering discipline as the
// prompt package's derived command descriptions.
func capRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}
