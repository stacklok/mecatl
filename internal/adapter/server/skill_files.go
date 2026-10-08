package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/tool"
)

// Skill files: read-only client access to a skill's instruction body and bundled
// assets, so a client (Studio's skill page) can show what a skill contains.
//
// TERM: skill file — one readable member of a skill bundle, addressed by a LOGICAL
// name (tool.ValidSkillAssetName): slash-separated, relative, never a filesystem path.
// The instruction body is the distinguished file named "SKILL.md"; every other file is
// an asset. Avoid: "asset" in the client API (the engine's word for the non-body
// payloads only), "attachment", "path", "document".
//
// TERM: skill view — the set of skills one caller can see: the deployment's configured
// skills plus that caller's own published learned skills (a learned skill is body-only:
// it has "SKILL.md" and no assets). It is the same view ListSkills and the Skill tool
// use. Avoid: "catalog" (the whole process-wide store), "inventory" (metadata only).
//
// DECISION: the API is two read-only RPCs, ListSkillFiles(name) and
// ReadSkillFile(name, file). Reason: a listing stays small and bounded while content is
// fetched one file at a time, mirroring how the Skill tool already loads a body and then
// one asset per call. Rejected: a single GetSkill returning body plus every asset, whose
// response size is unbounded.
//
// DECISION: files resolve through the CALLER'S skill view, never the process-wide
// catalog. Reason: ListSkills publishes the caller's learned skills before reading, so a
// file read that skipped the caller's partition could show another caller's learned
// skill or miss the caller's own. Rejected: reading the AtomicCatalog's default view.
//
// DECISION: the daemon only reads through tool.SkillSource, the seam the Skill tool
// uses. Reason: that is the one place that already enforces logical names, byte caps,
// and the body-only rule for learned skills; the server adapter must not reach into the
// skills adapter's discovery types (see Config.Skills).
//
// SPEC: ListSkillFiles of a name not in the caller's skill view is NotFound, never an
// empty list. It returns "SKILL.md" first, then assets sorted by logical name. The
// filesystem source already rejects a skill with more than 1,024 assets, so this API does not
// truncate a listing.
// SPEC: ReadSkillFile returns at most skillfs.MaxOutputBytes bytes of valid UTF-8; an
// invalid logical name is InvalidArgument and never content; an unknown file is NotFound;
// a non-text file is refused rather than returned as repaired garbage.
// SPEC: a learned skill lists exactly one file, "SKILL.md", and its body is the
// published version's body. Tested through the real daemon wiring in internal/app
// (TestBuildSkillFilesResolveThroughTheCallersOwnView) and the real catalog in
// engine/adapter/skillfs (TestSourceForPartitionsIsTheCallersOwnView).
// SPEC: files resolve through the caller's own skill view: another caller's learned skill is
// NotFound. Tested by the same two tests.
// Also tested here: listing order and sizes, unknown skill, reading SKILL.md and an asset,
// invalid names, unknown file, the cap boundary, non-text (including NUL), body limits, the stable
// error codes over HTTP, and that every read goes through the publication preface.
//
// DECISION: the RPCs are named ListSkillFiles and ReadSkillFile, and the body is exposed as the
// file "SKILL.md" rather than through a separate GetSkillBody. Reason: a client lists and shows
// one uniform set of files. Rejected: an engine-aligned GetSkill plus ReadSkillAsset, which makes
// every client special-case the body.
// DECISION: SkillFileInfo.instructions marks the body, and clients key off it, not off the name
// "SKILL.md". Reason: the listed "SKILL.md" is the body WITHOUT its frontmatter, so it is not the
// raw file; the name would otherwise permanently claim content it does not hold, and a future
// raw-file accessor needs that name. Rejected: the name alone as the marker.
// DECISION: access class KindCallerOwned: the boundary resolves the skill through the verified
// caller's own view and treats a skill outside it as absent, like ListLearnedSkills. Rejected:
// KindDerived (the name is not an unforgeable handle, a caller can ask for any name) and
// KindSharedInfrastructure (the live view is not identical for every caller; ListSkills' own entry
// now says it adds the caller's learned skills, and its kind is left to a separate change). The two
// entries sit in classification.go beside ListSkills.
// DECISION: an oversize file is an error (FailedPrecondition, HTTP 422), not a truncated success
// with a flag. Reason: a truncated file would look complete to a client that ignores the flag.
// Rejected: 413 and ResourceExhausted, which the repo already uses for an oversized REQUEST body.
// DECISION: only name and size are exposed per file; the engine's advisory "executable" bit is
// dropped so a client never implies a skill file can be run.
// DECISION: both RPCs also have HTTP routes, GET /v1/skills/files?name= and
// GET /v1/skills/files/read?name=&file=, with the file name as a query parameter (the precedent is
// GET /v1/mcp/resources/read?server=&uri=). Reason: the SDK's RPC catalog pins the exact set of
// gRPC-only methods (ADR 0304), so a unary read RPC cannot ship without a route; and the natural
// GET /v1/skills/{name}/files panics at registration, because ServeMux sees it as ambiguous with
// GET /v1/skills/learned/{id}. Logical file names also contain slashes, which are awkward in a
// path segment. Rejected: gRPC only, and a {name}/files path.
//
// TERM: SKILL.md — in this API, the skill's instruction BODY: the text after the
// frontmatter. Avoid: "the file on disk"; the frontmatter (name, description, license,
// allowed-tools) is not part of it.
// DECISION: SKILL.md is the frontmatter-stripped body, because tool.SkillSource has no accessor
// for the raw file and the Skill tool itself loads only the body. Accepted cost: the Files view
// cannot show a skill's frontmatter, and the listed size is the body's size, not the file's size
// on disk.
// DECISION: the skill_files feature identifier announces the pair (features.go: every additive
// RPC is announced through features). Reason: the published SDK runs against arbitrary daemons,
// and against one without these routes the call is a generic 404 that looks like skill_not_found.
// The SDK only announces it; gating a call on it is left to a client that needs to.

const (
	// skillBodyFile is the logical name under which the instruction body is listed and read.
	skillBodyFile = "SKILL.md"
	// maxSkillFileBytes is the most text ReadSkillFile returns: the Skill tool's own output cap.
	maxSkillFileBytes = skillfs.MaxOutputBytes
)

var (
	// ErrSkillFileTooLarge reports a file over maxSkillFileBytes. It is refused, not truncated.
	ErrSkillFileTooLarge = errors.New("server: skill file is too large to read")
	// ErrSkillFileNotText reports a file that is not text: invalid UTF-8, or containing a NUL byte.
	ErrSkillFileNotText = errors.New("server: skill file is not valid text")
)

// beginSkillRead prepares one read of the caller's skill view the way ListSkills does: it takes the
// caller partition's publication lock and publishes that caller's learned skills first, so the view
// is current. The returned function releases the lock and must always be called.
func (s *Service) beginSkillRead(ctx context.Context) func() {
	release := func() {}
	partition, err := s.skillPartition(ctx, "")
	if err != nil {
		return release
	}
	if s.cfg.BeginSkillPublication != nil {
		release = s.cfg.BeginSkillPublication(partition)
	}
	if s.cfg.PublishLearnedSkills != nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillPublicationTimeout)
		_ = s.cfg.PublishLearnedSkills(publishCtx, partition)
		cancel()
	}
	return release
}

// skillSource returns the caller's read-only skill source, or nil when skills are disabled.
func (s *Service) skillSource(ctx context.Context) tool.SkillSource {
	if s.cfg.SkillSourceFor == nil {
		return nil
	}
	return s.cfg.SkillSourceFor(ctx)
}

// ListSkillFiles lists the readable files of one skill in the caller's skill view: SKILL.md first,
// then the bundled assets sorted by logical name.
func (s *Service) ListSkillFiles(ctx context.Context, name string) ([]*mecatlv1.SkillFileInfo, error) {
	defer s.beginSkillRead(ctx)()
	source := s.skillSource(ctx)
	if source == nil {
		return nil, fmt.Errorf("skill %q: %w", name, tool.ErrSkillNotFound)
	}
	body, err := source.SkillBody(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	assets, err := source.ListSkillAssets(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	files := make([]*mecatlv1.SkillFileInfo, 0, len(assets)+1)
	files = append(files, &mecatlv1.SkillFileInfo{Name: skillBodyFile, Size: int64(len(body)), Instructions: true})
	for _, asset := range assets {
		files = append(files, &mecatlv1.SkillFileInfo{Name: asset.Name, Size: asset.Size})
	}
	return files, nil
}

// ReadSkillFile returns the text of one file of a skill in the caller's skill view.
func (s *Service) ReadSkillFile(ctx context.Context, name, file string) (string, error) {
	defer s.beginSkillRead(ctx)()
	source := s.skillSource(ctx)
	if source == nil {
		return "", fmt.Errorf("skill %q: %w", name, tool.ErrSkillNotFound)
	}
	var data []byte
	if file == skillBodyFile {
		body, err := source.SkillBody(ctx, name)
		if err != nil {
			return "", fmt.Errorf("skill %q: %w", name, err)
		}
		data = []byte(body)
	} else {
		if !tool.ValidSkillAssetName(file) {
			return "", fmt.Errorf("%w: %q is not a valid skill file name", ErrInvalidArgument, file)
		}
		// Look the file up first so an oversize or missing file gets its own error before any
		// bytes are read, whichever the source's own cap would say.
		assets, err := source.ListSkillAssets(ctx, name)
		if err != nil {
			return "", fmt.Errorf("skill %q: %w", name, err)
		}
		var size int64 = -1
		for _, asset := range assets {
			if asset.Name == file {
				size = asset.Size
				break
			}
		}
		if size < 0 {
			return "", fmt.Errorf("skill %q file %q: %w", name, file, tool.ErrSkillAssetNotFound)
		}
		if size > maxSkillFileBytes {
			return "", fmt.Errorf("skill %q file %q is %d bytes, over the %d byte limit: %w", name, file, size, maxSkillFileBytes, ErrSkillFileTooLarge)
		}
		data, err = source.ReadSkillAsset(ctx, name, file)
		if err != nil {
			return "", fmt.Errorf("skill %q file %q: %w", name, file, err)
		}
	}
	if len(data) > maxSkillFileBytes {
		return "", fmt.Errorf("skill %q file %q is over the %d byte limit: %w", name, file, maxSkillFileBytes, ErrSkillFileTooLarge)
	}
	// A NUL byte makes a file binary even when it is valid UTF-8; the Skill tool refuses it too.
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("skill %q file %q: %w", name, file, ErrSkillFileNotText)
	}
	return string(data), nil
}
