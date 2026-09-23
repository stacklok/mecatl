package app

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// soulsnapshot.go is the composition-layer PROJECTION of the resolved soul + the
// LIVE user-model lister into the server adapter's wire types (issue #14, Phase 3,
// Item 3 — the read-only /soul + /usermodel TUI inspection panels). It is the one
// place the soul/user-model adapters meet the server's GetSoul/GetUserModel seams,
// so the server adapter never reaches into the soul adapter's loader or the
// composition-layer soulMeta type. It adds NO write path: soulSnapshot READS the
// selected soul, and userModelLister exposes the store's READ-ONLY Index.

// soulProvenanceProto maps the composition-layer soulProvenance to its proto enum
// counterpart, so the wire surface reports provenance without the app type leaking.
func soulProvenanceProto(p soulProvenance) mecatlv1.SoulProvenance {
	switch p {
	case soulUser:
		return mecatlv1.SoulProvenance_SOUL_PROVENANCE_USER
	case soulProject:
		return mecatlv1.SoulProvenance_SOUL_PROVENANCE_PROJECT
	case soulDriver:
		return mecatlv1.SoulProvenance_SOUL_PROVENANCE_DRIVER
	default:
		return mecatlv1.SoulProvenance_SOUL_PROVENANCE_UNSPECIFIED
	}
}

// soulSnapshot projects the resolved soul into the proto SoulInfo the server's
// GetSoul RPC returns. It re-runs the SAME selection policy (selectSoulSource —
// USER-wins precedence, project trust gate, drift check) the engine's
// buildSoulSource consumes, so the snapshot reflects exactly the soul that will be
// contributed this run. The re-read is idempotent and cheap (one capped 20 KiB
// file, read at most twice — the accepted cost already documented on
// buildSoulSourceWith).
//
// It returns nil — making capabilities().Soul false — ONLY when there is no soul
// concept to inspect at all: --no-soul, or no candidate present anywhere. When a
// soul WAS selected it returns the full snapshot (content + meta). When a project
// soul was discovered but DROPPED as untrusted (Provenance==soulProject,
// Trusted==false, Present==false) it STILL returns a (content-less) snapshot so the
// panel can honestly explain the UNTRUSTED-not-loaded state rather than silently
// claiming "no soul".
func soulSnapshot(cfg Config) *mecatlv1.SoulInfo {
	return soulSnapshotWith(cfg, osBaselineIO)
}

// soulSnapshotWith is soulSnapshot with an injectable baseline-IO seam so the
// drift-baseline read is exercised offline (no real ~/.config). It is the testable
// body; soulSnapshot binds the real filesystem.
func soulSnapshotWith(cfg Config, io baselineIO) *mecatlv1.SoulInfo {
	src, meta := selectSoulSource(cfg, io, buildSoulGate(cfg))

	// A soul WAS selected: load its clean body for the panel. Load is fail-soft and
	// re-validates the body (the SAME discipline LoadWithMeta applied during
	// selection), so the content shown is exactly the bytes that reach the prompt.
	if meta.Present && src != nil {
		body, _ := src.Load(context.Background())
		return &mecatlv1.SoulInfo{
			// The body is os.ReadFile→string with NO decoder to launder it (soul
			// store ValidateBody checks size/markers, not encoding), so a Latin-1
			// SOUL.md would fail proto.Marshal and turn GetSoul into codes.Internal
			// — the issue-#402 crash on a sibling surface. SizeBytes/Sha256 stay
			// over the ORIGINAL bytes: they describe the file, not this projection.
			Content:    session.ToValidUTF8(body),
			SizeBytes:  int64(meta.Size),
			Sha256:     meta.SHA256,
			Present:    true,
			Provenance: soulProvenanceProto(meta.Provenance),
			Trusted:    meta.Trusted,
			Drifted:    meta.Drifted,
		}
	}

	// No soul selected. Distinguish a DROPPED untrusted project soul (worth showing,
	// so the operator learns --trust-project would honour it) from genuinely-nothing.
	if meta.Provenance == soulProject {
		return &mecatlv1.SoulInfo{
			SizeBytes:  int64(meta.Size),
			Sha256:     meta.SHA256,
			Present:    false,
			Provenance: mecatlv1.SoulProvenance_SOUL_PROVENANCE_PROJECT,
			Trusted:    meta.Trusted, // false (untrusted) or true-but-strict-dropped
			Drifted:    meta.Drifted,
		}
	}

	// Genuinely no soul to inspect (--no-soul or no candidate present): nil so the
	// capability bit is false and the /soul built-in is gated off.
	return nil
}

// userModelLister adapts the user-model tool.MemoryStore's read-only Index into
// the server's UserModelLister seam, so the server adapter need not import the
// memory adapter. It returns an untyped nil server.UserModelLister when store is
// nil (user model disabled; the interface-nil check is sound under catalogAssets'
// typed-nil discipline), so capabilities().UserModel is honestly false. It exposes
// only the Index (key + description, value omitted) — no write path.
func userModelLister(store tool.MemoryStore) server.UserModelLister {
	if store == nil {
		return nil
	}
	return userModelIndexLister{store: store}
}

// userModelIndexLister is the concrete server.UserModelLister over a user-model
// store. List reads the store's tier-0 Index (the same value-omitted summary view
// the prompt assembler renders) and maps it to the surface-agnostic entries.
type userModelIndexLister struct {
	store tool.MemoryStore
}

// List returns the current user-model entries (key + description, value omitted),
// key-sorted (the store's Index already sorts).
func (l userModelIndexLister) List(ctx context.Context) ([]server.UserModelEntry, error) {
	idx, err := l.store.Index(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.UserModelEntry, 0, len(idx))
	for _, e := range idx {
		out = append(out, server.UserModelEntry{Key: e.Key, Description: e.Description})
	}
	return out, nil
}

func (l userModelIndexLister) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	return l.store.Inspect(ctx, key)
}
