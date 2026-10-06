package grpcdriver

import (
	"context"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adaptersupport/soulbody"
)

// SoulOptions configures a remote SoulSource client.
type SoulOptions struct {
	// MaxBytes caps the accepted body; 0 uses soulbody.DefaultMaxBytes. Over the
	// cap the body is REJECTED (no fragment), not truncated — the same rule as
	// the local file store.
	MaxBytes int
	// Diagnostics is the operational-logging sink for the fail-soft branches
	// (driver fault at runtime → WARN; body rejected by re-validation → Debug).
	// nil defaults to port.NopDiagnostics.
	Diagnostics port.Diagnostics
}

// SoulSource is a prompt.SoulSource over a remote SoulSourceService driver.
// The driver's body is RE-VALIDATED client-side with the full local soul
// discipline (soulbody.ValidateBody: byte cap, injection scan, fence integrity,
// trim) — a driver is never trusted to sanitize. Load upholds the
// prompt.SoulSource fail-soft contract: a driver fault at RUNTIME degrades to
// ("", nil) with a logged WARN, never an error that aborts a run; the
// BUILD-time reachability check is the separate Probe (loud-misconfig
// posture, fatal in the composition layer).
type SoulSource struct {
	client   driverv1.SoulSourceServiceClient
	maxBytes int
	diag     port.Diagnostics
}

// compile-time assertion that SoulSource satisfies the prompt port.
var _ prompt.SoulSource = (*SoulSource)(nil)

// NewSoulSource wraps an established driver connection (see Dial) as a
// prompt.SoulSource.
func NewSoulSource(conn grpc.ClientConnInterface, opts SoulOptions) *SoulSource {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = soulbody.DefaultMaxBytes
	}
	diag := opts.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &SoulSource{client: driverv1.NewSoulSourceServiceClient(conn), maxBytes: maxBytes, diag: diag}
}

// Load returns the re-validated soul body, or ("", nil) when there is no
// usable soul — fail-soft at every branch (driver fault, empty body,
// validation rejection), exactly like the local store's Load.
func (s *SoulSource) Load(ctx context.Context) (string, error) {
	resp, err := s.client.LoadSoul(ctx, &driverv1.LoadSoulRequest{})
	if err != nil {
		// RUNTIME fail-soft per the SoulSource contract: the persona is
		// best-effort context, never correctness — but the operator gets a WARN
		// (a silent degradation would hide a dead driver).
		s.diag.Log(ctx, port.LevelWarn, "soul: driver LoadSoul failed; no soul fragment this run (fail-soft)",
			"err", rpcErr(ctx, "load soul", err))
		return "", nil
	}
	body, reject := soulbody.ValidateBody(resp.GetBody(), s.maxBytes)
	if reject != "" {
		s.diag.Log(ctx, port.LevelDebug, "soul: driver body rejected; no soul loaded", "reason", reject)
		return "", nil
	}
	return body, nil
}

// Probe performs the BUILD-time reachability check: one LoadSoul round trip,
// returning the wrapped RPC error on a fault (the composition layer treats it
// as FATAL — an explicitly configured driver that cannot answer is a
// misconfiguration, unlike a runtime blip which Load degrades on). The body
// itself is NOT judged here: an empty or invalid persona is a legal "no soul",
// not a misconfig.
func (s *SoulSource) Probe(ctx context.Context) error {
	if _, err := s.client.LoadSoul(ctx, &driverv1.LoadSoulRequest{}); err != nil {
		return rpcErr(ctx, "load soul", err)
	}
	return nil
}
