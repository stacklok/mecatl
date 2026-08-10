package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	// maxAssetBytes caps ONE materialized payload. The protocol's unary
	// ReadSkillAsset rides the 64 MiB driver message ceiling; the harness is
	// stricter per asset so a single runaway payload cannot eat the bundle cap.
	maxAssetBytes = 16 << 20
	// maxBundleBytes caps a skill's WHOLE materialized bundle, matching the
	// driver protocol's message ceiling (grpcdriver.MaxSnapshotBytes). Over the
	// cap the ACTIVATION fails as a model-addressable error — never a partial
	// bundle on disk.
	maxBundleBytes = 64 << 20
	// maxBundleAssets caps the NUMBER of payloads in one bundle: a hostile
	// driver listing millions of tiny assets would otherwise exhaust inodes/
	// directory entries while staying under the byte caps. Over-cap fails the
	// activation exactly like the byte caps.
	maxBundleAssets = 4096
	// maxAssetNameDepth caps a logical name's SEGMENT count: pathologically
	// deep nesting ("a/a/a/…") is rejected exactly like the byte caps.
	maxAssetNameDepth = 16
)

// errBundleRejected marks a DETERMINISTIC local rejection of a bundle:
// invalid/over-deep logical names, containment escapes, the byte/count caps,
// or a hostile skill-name segment. These are properties of the bundle itself
// — retrying the identical bundle cannot succeed — so Provision latches them
// permanently. Transport/ctx faults are NEVER wrapped with this sentinel and
// always retry on the next Provision.
var errBundleRejected = errors.New("skills: bundle rejected")

// AssetMaterializer provisions a remote skill's auxiliary payloads onto the
// REAL disk — <base>/<skill>/<logical-name> — so the existing Read/read-roots
// contract and Bash script execution work unchanged for driver-served skills
// (a virtual overlay cannot be executed; the Skill activation header
// advertises the materialized directory). It works over the tool.SkillSource
// PORT only: no path concept crosses the port; the cache base is the
// composition layer's (an eagerly-created temp dir registered as the single
// driver read root, removed on catalog close).
//
// Discipline:
//   - LAZY, per-skill: a skill's payloads transfer on its FIRST activation
//     only; a never-activated skill transfers zero bytes. Concurrent
//     Provision calls for one skill serialize on a per-skill lock, so the
//     bundle transfers at most once.
//   - SUCCESS latches permanently; a DETERMINISTIC rejection (errBundleRejected:
//     caps, invalid names, containment) latches too — retrying the same
//     bundle cannot succeed. A TRANSPORT/ctx fault is returned but NOT
//     latched: the next Provision retries, so a cancelled or flaky transfer
//     never bricks a skill for the (build-scoped, shared) cache's lifetime.
//   - Every logical name passes tool.ValidSkillAssetName, the
//     maxAssetNameDepth segment cap, AND a post-Clean containment check under
//     the skill's directory; a violation fails the whole activation, never
//     writes outside, never leaves a partial bundle.
//   - Caps: maxAssetBytes per payload and maxBundleBytes per bundle, enforced
//     on the ACTUAL bytes read (the advertised Size is advisory), plus
//     maxBundleAssets on the payload count. Over-cap fails the activation
//     with a model-addressable error.
//   - An Executable payload is written 0o755, others 0o644.
//   - A skill with no payloads provisions NOTHING and yields dir "" (the
//     activation header then omits the Base-directory block).
type AssetMaterializer struct {
	src  tool.SkillSource
	base string

	mu    sync.Mutex
	state map[string]*provisionState // by skill name
}

// provisionState is one skill's provisioning record. st.mu serializes
// concurrent Provision calls for the skill (the second caller blocks, then
// reads the latched outcome — never a second transfer); done/rejected are the
// two PERMANENT latches, anything else retries.
type provisionState struct {
	mu       sync.Mutex
	done     bool   // success latched permanently
	dir      string // valid when done
	rejected bool   // deterministic errBundleRejected latched permanently
	err      error  // valid when rejected
}

// NewAssetMaterializer builds a materializer provisioning under base, which
// must already exist (the composition layer creates it EAGERLY at build — the
// osfs Workspace opens its read roots at construction and skips non-existent
// dirs, so a late-born root would be unreadable).
func NewAssetMaterializer(src tool.SkillSource, base string) *AssetMaterializer {
	return &AssetMaterializer{src: src, base: base, state: make(map[string]*provisionState)}
}

// Provision materializes the named skill's payloads and returns the skill's
// materialized directory, or ("", nil) when the skill has no payloads. A
// success (and a deterministic bundle rejection) is latched; a transport/ctx
// fault is returned to THIS caller and retried by the next one.
func (m *AssetMaterializer) Provision(ctx context.Context, skill string) (string, error) {
	m.mu.Lock()
	st, ok := m.state[skill]
	if !ok {
		st = &provisionState{}
		m.state[skill] = st
	}
	m.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.done:
		return st.dir, nil
	case st.rejected:
		return "", st.err
	}
	dir, err := m.provision(ctx, skill)
	if err != nil {
		if errors.Is(err, errBundleRejected) {
			// Deterministic: the bundle itself is invalid/over-cap — latch it.
			st.rejected, st.err = true, err
		}
		// Transport/ctx faults stay unlatched: the next Provision retries.
		return "", err
	}
	st.done, st.dir = true, dir
	return dir, nil
}

// provision is the locked body: list, validate, transfer, then return the
// per-skill dir. Any failure removes the partial dir before returning.
func (m *AssetMaterializer) provision(ctx context.Context, skill string) (string, error) {
	if !validSkillDirSegment(skill) {
		return "", fmt.Errorf("skill name %q is not a valid cache segment: %w", skill, errBundleRejected)
	}
	assets, err := m.src.ListSkillAssets(ctx, skill)
	if err != nil {
		return "", fmt.Errorf("listing bundled files of skill %q: %w", skill, err)
	}
	if len(assets) == 0 {
		return "", nil
	}
	if len(assets) > maxBundleAssets {
		return "", fmt.Errorf("skill %q lists %d bundled files, over the %d-file bundle cap: %w", skill, len(assets), maxBundleAssets, errBundleRejected)
	}

	dir := filepath.Join(m.base, skill)
	// Post-Clean containment of the per-skill dir itself (defence in depth on
	// top of validSkillDirSegment).
	if !containedIn(m.base, dir) {
		return "", fmt.Errorf("skill name %q escapes the asset cache: %w", skill, errBundleRejected)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating asset dir for skill %q: %w", skill, err)
	}

	var total int64
	for _, a := range assets {
		if err := m.writeAsset(ctx, dir, skill, a, &total); err != nil {
			// NEVER partial: a failed bundle leaves nothing behind.
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// writeAsset validates, reads, cap-checks, and writes one payload. Validation
// and cap failures carry errBundleRejected (deterministic — latched by
// Provision); read/write faults do not (transient — retried).
func (m *AssetMaterializer) writeAsset(ctx context.Context, dir, skill string, a tool.SkillAsset, total *int64) error {
	if !tool.ValidSkillAssetName(a.Name) {
		return fmt.Errorf("skill %q has an invalid bundled-file name %q: %w", skill, a.Name, errBundleRejected)
	}
	if depth := strings.Count(a.Name, "/") + 1; depth > maxAssetNameDepth {
		return fmt.Errorf("skill %q bundled-file name %q is %d segments deep, over the %d-segment cap: %w", skill, a.Name, depth, maxAssetNameDepth, errBundleRejected)
	}
	dst := filepath.Join(dir, filepath.FromSlash(a.Name))
	if !containedIn(dir, dst) {
		return fmt.Errorf("skill %q bundled file %q escapes the skill directory: %w", skill, a.Name, errBundleRejected)
	}
	data, err := m.src.ReadSkillAsset(ctx, skill, a.Name)
	if err != nil {
		return fmt.Errorf("reading bundled file %q of skill %q: %w", a.Name, skill, err)
	}
	if int64(len(data)) > maxAssetBytes {
		return fmt.Errorf("bundled file %q of skill %q is %d bytes, over the %d-byte per-file cap: %w", a.Name, skill, len(data), int64(maxAssetBytes), errBundleRejected)
	}
	*total += int64(len(data))
	if *total > maxBundleBytes {
		return fmt.Errorf("skill %q bundled files exceed the %d-byte bundle cap: %w", skill, int64(maxBundleBytes), errBundleRejected)
	}
	if parent := filepath.Dir(dst); parent != dir {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("creating bundled-file dir for skill %q: %w", skill, err)
		}
	}
	mode := os.FileMode(0o644)
	if a.Executable {
		mode = 0o755
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("writing bundled file %q of skill %q: %w", a.Name, skill, err)
	}
	return nil
}

// validSkillDirSegment reports whether a skill name is usable as ONE cache
// path segment: non-empty, no separators/NUL/control/line-separator runes,
// not "."/"..". Skill names come from a remote driver, so this is enforced
// here rather than assumed.
func validSkillDirSegment(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// containedIn reports whether child (post-Clean) stays at or under parent.
func containedIn(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
