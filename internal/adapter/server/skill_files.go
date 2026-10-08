package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
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
// empty list. It returns "SKILL.md" first, then assets sorted by logical name, at most
// 1,024 entries (the inventory bound the skills adapter and the remote driver share).
// SPEC: ReadSkillFile returns at most skillfs.MaxOutputBytes bytes of valid UTF-8; an
// invalid logical name is InvalidArgument and never content; an unknown file is NotFound;
// a non-text file is refused rather than returned as repaired garbage.
// SPEC: a learned skill lists exactly one file, "SKILL.md", and its body is the
// published version's body. (No test yet: needs the real catalog, not a fake source.)
// SPEC: files resolve through the caller's own skill view. (No test yet; same reason.)
// Tested already: listing order and sizes, unknown skill, reading SKILL.md and an asset,
// invalid names, unknown file, oversize, non-text, the HTTP mirror, and the cap constant.
//
// ASSUMPTION: the RPC names, and calling the body "SKILL.md" rather than exposing it
// through a separate GetSkillBody.
// ASSUMPTION: access class KindDerived (the skill name can only come from ListSkills,
// which is already classified), not KindSharedInfrastructure. Do not copy ListSkills'
// classification rationale, "identical for every caller": it predates caller-published
// learned skills and is no longer accurate for the live view. The two entries go in
// classification.go's serviceAccessTable, beside ListSkills.
// ASSUMPTION: an oversize file is an error (ResourceExhausted), not a truncated success
// with a flag.
// ASSUMPTION: only name and size are exposed per file; the engine's advisory
// "executable" bit is dropped so a client never implies a skill file can be run.
// DECISION: both RPCs also have HTTP routes, GET /v1/skills/files?name= and
// GET /v1/skills/files/content?name=&file=, with the file name as a query parameter.
// Reason: the SDK's RPC catalog pins the exact set of gRPC-only methods (ADR 0304), so a
// unary read RPC cannot ship without a route; and the natural GET /v1/skills/{name}/files
// panics at registration, because ServeMux sees it as ambiguous with
// GET /v1/skills/learned/{id}. Logical file names also contain slashes, which are awkward
// in a path segment. Rejected: gRPC only, and a {name}/files path.
//
// ASSUMPTION: the HTTP shape above (literal "files" segment, query parameters) is the one
// to keep.
//
// TERM: SKILL.md — in this API, the skill's instruction BODY: the text after the
// frontmatter. Avoid: "the file on disk"; the frontmatter (name, description, license,
// allowed-tools) is not part of it.
// ASSUMPTION: SKILL.md should be the frontmatter-stripped body, because tool.SkillSource
// has no accessor for the raw file and the Skill tool itself loads only the body. The
// cost: the Files view cannot show a skill's frontmatter, and the listed size is the
// body's size, not the file's size on disk.
// ASSUMPTION: a new capability flag (skill_files) gates the feature, rather than
// reusing the skills flag, so a client can tell "skills exist" from "files are readable"
// on an older daemon.
//

const (
	// skillBodyFile is the logical name under which the instruction body is listed and read.
	skillBodyFile = "SKILL.md"
	// maxSkillFileEntries bounds a listing, matching the skills adapter's and the remote
	// driver's inventory bound.
	maxSkillFileEntries = 1_024
	// maxSkillFileBytes is the most text ReadSkillFile returns. It equals the Skill tool's
	// output cap (skillfs.MaxOutputBytes); a test keeps the two equal, because the server
	// adapter does not import the skills adapter.
	maxSkillFileBytes = 25_000
)

var (
	// ErrSkillFileTooLarge reports a file over maxSkillFileBytes. It is refused, not truncated.
	ErrSkillFileTooLarge = errors.New("server: skill file is too large to read")
	// ErrSkillFileNotText reports a file that is not valid UTF-8 text.
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
	if len(assets) > maxSkillFileEntries-1 {
		assets = assets[:maxSkillFileEntries-1]
	}
	files := make([]*mecatlv1.SkillFileInfo, 0, len(assets)+1)
	files = append(files, &mecatlv1.SkillFileInfo{Name: skillBodyFile, Size: int64(len(body))})
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
	if !utf8.Valid(data) {
		return "", fmt.Errorf("skill %q file %q: %w", name, file, ErrSkillFileNotText)
	}
	return string(data), nil
}
