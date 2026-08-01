package skillfs

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/tool"
)

// Activation is the load-on-activation payload the Skill tool renders: the
// skill's full instruction body plus the BASE DIRECTORY its bundled files are
// readable under. BaseDir "" means the skill has no on-disk payloads — the
// activation header then omits the Base-directory block entirely (the
// pre-existing dir=="" rendering branch).
type Activation struct {
	Body    string
	BaseDir string
}

// Activator is the seam the Skill tool loads a skill through on activation.
// Implementations own WHERE the body/payloads come from: the snapshot
// activator serves an FS source's skills in place (zero copy), the source
// activator serves a remote driver's skills by materializing payloads into a
// build-scoped cache on first activation. An Activate error is surfaced to the
// MODEL as an addressable tool error (never a harness fault), so a failed
// driver materialization is recoverable in-conversation.
type Activator interface {
	Activate(ctx context.Context, name string) (Activation, error)
}

// NewSnapshotActivator returns the FS activator: bodies and base directories
// come straight from the snapshot source, byte-identical to the pre-seam
// rendering and zero-copy (FS skills are served IN PLACE through the existing
// read-roots contract; nothing is materialized).
func NewSnapshotActivator(src *FSSource) Activator {
	return snapshotActivator{src: src}
}

type snapshotActivator struct {
	src *FSSource
}

// Activate loads the named skill from the snapshot. The base directory is the
// canonical per-skill dir (AssetDir) — exactly the value the read-root
// allowlist is keyed on — or "" for a skill with no source path.
func (a snapshotActivator) Activate(ctx context.Context, name string) (Activation, error) {
	body, err := a.src.SkillBody(ctx, name)
	if err != nil {
		return Activation{}, err
	}
	dir, _ := a.src.AssetDir(name)
	return Activation{Body: body, BaseDir: dir}, nil
}

// assetProvisioner is the consumer-side seam for the remote-driver asset
// materializer, which stays in the root module (internal/adapter/skills,
// out of scope for #328). Root *AssetMaterializer satisfies it implicitly.
type assetProvisioner interface {
	Provision(ctx context.Context, skill string) (string, error)
}

// NewSourceActivator returns the driver activator over the PORT only: the body
// loads via SkillBody and the payloads materialize through mat (lazily, on
// FIRST activation of each skill — a never-activated skill transfers zero
// bytes). A successful activation is cached (body + BaseDir), so repeat
// activations in one process re-render from memory. mat may be nil for a
// payload-less deployment; every activation then has BaseDir "".
func NewSourceActivator(src tool.SkillSource, mat assetProvisioner) Activator {
	return &sourceActivator{src: src, mat: mat, cache: make(map[string]Activation)}
}

type sourceActivator struct {
	src tool.SkillSource
	mat assetProvisioner

	mu    sync.Mutex
	cache map[string]Activation // by skill name, successes only
}

// Activate loads the body and provisions the payloads, caching after the
// first success. Failures are NOT cached here, and the materializer beneath
// latches only SUCCESS and DETERMINISTIC bundle rejections (over-cap/invalid
// names — errBundleRejected): a transient transport/ctx fault on one
// activation is retried in full on the next.
func (a *sourceActivator) Activate(ctx context.Context, name string) (Activation, error) {
	a.mu.Lock()
	got, ok := a.cache[name]
	a.mu.Unlock()
	if ok {
		return got, nil
	}

	body, err := a.src.SkillBody(ctx, name)
	if err != nil {
		return Activation{}, err
	}
	var dir string
	if a.mat != nil {
		dir, err = a.mat.Provision(ctx, name)
		if err != nil {
			return Activation{}, fmt.Errorf("provisioning bundled files: %w", err)
		}
	}
	act := Activation{Body: body, BaseDir: dir}
	a.mu.Lock()
	a.cache[name] = act
	a.mu.Unlock()
	return act, nil
}
