package grpcdriver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// SkillSource is a tool.SkillSource over a remote SkillSourceService driver.
// It is translation plus a DEFENSIVE normalization layer on ListSkills (the
// driver sits at the operator-infrastructure trust tier, but its metadata
// feeds the always-in-context tool description, so the client re-enforces the
// invariants the port promises rather than trusting the wire): invalid skill
// names are dropped, duplicate names de-dup first-wins, the result is
// name-sorted, descriptions are forced single-line (control characters →
// spaces) then re-truncated to the always-in-context cap
// (skills.MaxDescriptionBytes), and Origin is stamped SkillOriginDriver
// UNCONDITIONALLY — a driver-served skill IS driver tier; a driver must not
// claim the "project"/"user" admission labels (the wire origin field stays
// driver-side observability only). Bodies/assets pass through; the
// SkillSource is retained by composition and enforces bounded logical payload
// reads through the Skill tool.
type SkillSource struct {
	client driverv1.SkillSourceServiceClient
}

// compile-time assertion that SkillSource satisfies the port.
var _ tool.SkillSource = (*SkillSource)(nil)

const (
	maxSkillAssetDataBytes        = 25_000
	maxSkillAssetRPCResponseBytes = 26 << 10
	maxSkillInventoryRPCBytes     = 64 << 10
	maxSkillInventoryEntries      = 1_024
	maxSkillInventoryNameBytes    = 32 << 10
)

// NewSkillSource wraps an established driver connection (see Dial) as a
// tool.SkillSource.
func NewSkillSource(conn grpc.ClientConnInterface) *SkillSource {
	return &SkillSource{client: driverv1.NewSkillSourceServiceClient(conn)}
}

// ListSkills returns the driver's skill metadata snapshot, defensively
// normalized (see the type doc).
func (s *SkillSource) ListSkills(ctx context.Context) ([]tool.SkillMeta, error) {
	resp, err := s.client.ListSkills(ctx, &driverv1.ListSkillsRequest{})
	if err != nil {
		return nil, rpcErr(ctx, "list skills", err)
	}
	wire := resp.GetSkills()
	out := make([]tool.SkillMeta, 0, len(wire))
	seen := make(map[string]bool, len(wire))
	for _, m := range wire {
		name := m.GetName()
		if !skills.ValidSkillName(name) {
			continue // drop invalid model-facing identity data
		}
		if seen[name] {
			continue // de-dup first-wins (wire order)
		}
		seen[name] = true
		out = append(out, tool.SkillMeta{
			Name:        name,
			Description: toolkit.TruncateRunes(singleLine(m.GetDescription()), skills.MaxDescriptionBytes),
			// UNCONDITIONAL: every skill listed by a driver is driver tier —
			// the wire's origin label is never adopted (a driver claiming
			// "project"/"user" would launder itself into a trusted-looking tier).
			Origin:    tool.SkillOriginDriver,
			HasAssets: m.GetHasAssets(),
			// Advisory frontmatter metadata (agentskills.io, #419): re-clamped
			// defensively to the parser's caps — a hostile/buggy driver must not
			// bloat the always-in-context tool spec.
			License:       clampAdvisoryString(m.GetLicense(), skills.MaxLicenseBytes),
			Compatibility: clampAdvisoryString(m.GetCompatibility(), skills.MaxCompatibilityBytes),
			Metadata:      clampAdvisoryMetadata(m.GetMetadata()),
			AllowedTools:  clampAllowedTools(m.GetAllowedTools()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SkillBody returns the named skill's full instruction body. A driver
// NOT_FOUND wraps tool.ErrSkillNotFound with the name in the message.
func (s *SkillSource) SkillBody(ctx context.Context, name string) (string, error) {
	resp, err := s.client.GetSkillBody(ctx, &driverv1.GetSkillBodyRequest{Name: name})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
		}
		return "", rpcErr(ctx, "get skill body", err)
	}
	return resp.GetBody(), nil
}

// ListSkillAssets returns the named skill's payload descriptors. A driver
// NOT_FOUND wraps tool.ErrSkillNotFound.
func (s *SkillSource) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	resp, err := s.client.ListSkillAssets(ctx, &driverv1.ListSkillAssetsRequest{Name: name}, grpc.MaxCallRecvMsgSize(maxSkillInventoryRPCBytes))
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
		}
		return nil, rpcErr(ctx, "list skill assets", err)
	}
	wire := resp.GetAssets()
	if len(wire) > maxSkillInventoryEntries {
		return nil, fmt.Errorf("list skill assets: inventory has %d entries, limit %d", len(wire), maxSkillInventoryEntries)
	}
	out := make([]tool.SkillAsset, 0, len(wire))
	nameBytes := 0
	for _, a := range wire {
		nameBytes += len(a.GetName())
		if nameBytes > maxSkillInventoryNameBytes {
			return nil, fmt.Errorf("list skill assets: inventory names exceed %d bytes", maxSkillInventoryNameBytes)
		}
		if !tool.ValidSkillAssetName(a.GetName()) {
			return nil, fmt.Errorf("list skill assets: invalid logical asset name %q", a.GetName())
		}
		out = append(out, tool.SkillAsset{Name: a.GetName(), Size: a.GetSize(), Executable: a.GetExecutable()})
	}
	// Name-sorted for FS-source parity (the FS listAssets walk is sorted): the
	// Skill tool's "Bundled files:" enumeration renders in this order, so a
	// driver must not impose wire order on the model-facing listing.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadSkillAsset returns one payload's bytes. A driver NOT_FOUND (unknown
// skill OR asset) wraps tool.ErrSkillAssetNotFound; an INVALID_ARGUMENT (the
// server pre-validates logical names via tool.ValidSkillAssetName) surfaces
// as a non-nil infrastructure error — never content.
func (s *SkillSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	if !tool.ValidSkillAssetName(asset) {
		return nil, fmt.Errorf("read skill asset: invalid logical asset name %q", asset)
	}
	resp, err := s.client.ReadSkillAsset(ctx, &driverv1.ReadSkillAssetRequest{Skill: skill, Asset: asset}, grpc.MaxCallRecvMsgSize(maxSkillAssetRPCResponseBytes))
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
		}
		return nil, rpcErr(ctx, "read skill asset", err)
	}
	data := resp.GetData()
	if len(data) > maxSkillAssetDataBytes {
		return nil, fmt.Errorf("read skill asset: payload is %d bytes, limit %d", len(data), maxSkillAssetDataBytes)
	}
	return data, nil
}

// singleLine enforces the port's single-line Description promise on wire
// metadata BEFORE the byte-cap truncation: every control character (newlines,
// carriage returns, tabs, DEL, ...) becomes a space, so a multi-line driver
// description cannot smuggle line structure into the always-in-context Skill
// tool description.
func singleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// clampAdvisoryString bounds one advisory single-string field (license,
// compatibility) to the parser's cap, rune-safe. These ride the
// always-in-context SkillMeta, so an unbounded wire value must not inflate
// every prompt.
func clampAdvisoryString(s string, maxLen int) string {
	return toolkit.TruncateRunes(strings.TrimSpace(s), maxLen)
}

// clampAdvisoryMetadata defensively bounds the advisory metadata map to the
// parser's caps: at most skills.MaxMetadataEntries entries, each value ≤
// skills.MaxMetadataValueBytes. On ANY overflow the WHOLE map drops to nil
// (advisory data — dropping is honest, silent truncation is not). A nil/empty
// map stays nil.
func clampAdvisoryMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > skills.MaxMetadataEntries {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) > skills.MaxMetadataValueBytes {
			return nil
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clampAllowedTools defensively bounds the advisory allowed-tools list to the
// parser's caps: at most skills.MaxAllowedTools names, each ≤
// skills.MaxAllowedToolNameBytes. On count overflow the parsed PREFIX is kept
// (it is a list of names — the prefix is the honest partial signal); an
// over-long single name is truncated rune-safe. An empty list stays nil.
func clampAllowedTools(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > skills.MaxAllowedTools {
		in = in[:skills.MaxAllowedTools]
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, toolkit.TruncateRunes(name, skills.MaxAllowedToolNameBytes))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
