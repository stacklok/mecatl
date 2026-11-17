// Package definitionloader loads the agent definitions the identity broker
// knows: the operator's global definitions, parsed with the harness's own
// definition parser, and the built-in definitions each Mecatl release ships.
// Each loaded Definition pairs the record identitybroker.New takes with the
// accepted definition the harness builds the agent from, so the broker and
// the harness work from one read. The broker core stays free of file access.
//
// Global definitions come only from the operator's explicit directories (the
// harness's explicit origin). The project and user tiers and remote drivers are
// never loaded: only the explicit origin is operator-managed, and a driver
// definition has no file bytes to digest.
//
// Shortcut: a global definition's revision is its file's modification time,
// because a plain file has no revision of its own, and there is no provenance
// profile allowlist and no root eligibility; removed by #2551 (revisions from
// each source's own revision, such as a Git commit or object revision). Until
// then the revision does not follow the content: a copy that keeps
// modification times (rsync -a, cp -p, tar) can change the bytes under the same
// revision, which only the digests reveal; and unchanged bytes get a new
// revision whenever the file is rewritten, remounted, or checked out again, or
// differ between replicas, which makes the broker treat them as a different
// definition.
package definitionloader

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	// SourceGlobal is the source of the operator's global definitions.
	SourceGlobal = "global"
	// SourceSystem is the source of the built-in definitions.
	SourceSystem = "system"

	// BuiltinMain names the built-in definition of the main agent of a session
	// started without a named definition: system/main.
	BuiltinMain = "main"
	// BuiltinExplorer names the built-in definition of the child agent of a
	// subagent call that names no definition: system/explorer.
	BuiltinExplorer = "explorer"

	// ProfileFile is the provenance profile of a global definition read from a
	// file. Its metadata holds the file's path under the key "path"; its
	// revision is the file's modification time in UTC, RFC 3339 with
	// nanoseconds.
	ProfileFile = "file/v1"
	// ProfileRelease is the provenance profile of a built-in definition. It
	// has no metadata; its revision is BuiltinRevision of the build.
	ProfileRelease = "mecatl-release/v1"

	// DevRevision is the built-in definitions' revision in any build that is
	// not a release, such as a local or snapshot build.
	DevRevision = "dev"
)

// releaseVersion matches the build stamp of a release: the release tag, such
// as v0.0.51. A git describe stamp of an untagged commit (v0.0.51-3-gabc1234),
// a dirty tree, and dev builds do not match.
var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// BuiltinRevision returns the built-in definitions' revision for a Mecatl
// build stamp (buildinfo.BuildID): the release version for a release build,
// otherwise DevRevision.
func BuiltinRevision(buildID string) string {
	if releaseVersion.MatchString(buildID) {
		return buildID
	}
	return DevRevision
}

// builtins returns the built-in definitions. It builds them on each call so
// no caller can change another's copy.
//
// Shortcut: each built-in is a name and a description, and the harness builds
// its default main agent and anonymous child without them, so their digests
// cover these stand-ins rather than what runs; removed by #2553 (the harness
// builds system/main and system/explorer from these definitions).
func builtins() []tool.AgentDef {
	return []tool.AgentDef{
		{Name: BuiltinMain, Description: "The main agent of a session started without a named definition."},
		{Name: BuiltinExplorer, Description: "The child agent of a subagent call that names no definition."},
	}
}

// Config selects what Load reads.
type Config struct {
	// Dirs are the operator's explicit agent definition directories, highest
	// precedence first: the same list the harness reads (its --agents-dir
	// flag). A missing directory contributes nothing.
	Dirs []string
	// BuildID is the Mecatl build stamp (buildinfo.BuildID); see
	// BuiltinRevision.
	BuildID string
}

// Definition is one loaded definition.
//
// Its digests are plain SHA-256 over the definition, which can include an
// inline MCP server's secret-shaped headers. They reveal no secret that has
// enough randomness, but anyone who sees a digest and knows the rest of the
// file can test guesses at a short one. Keep them out of logs and records
// that people who know the rest of the file can read.
type Definition struct {
	// Record is what the broker checks registrations against; pass it to
	// identitybroker.New.
	Record identitybroker.DefinitionRecord
	// Def is the accepted definition: the value Record's DefinitionDigest
	// covers, and the one the harness builds the agent from. Building from a
	// separate read of the source could build a definition the broker never
	// recorded.
	Def tool.AgentDef
}

// Problem reports one definition that was left out or adjusted while loading.
type Problem struct {
	// Location is where the definition came from, such as a file path.
	Location string
	// Reason says what was wrong.
	Reason string
	// Dropped reports whether the definition was left out (true) or loaded
	// with an adjustment, such as a truncated description (false).
	Dropped bool
}

// Load returns each built-in definition and each valid global definition,
// sorted by source and name, with a Problem for each definition it left out or
// adjusted. A definition the parser rejects, or whose record fails
// identitybroker.DefinitionRecord.Validate, is left out and loading continues.
// When two directories define the same name, the earlier directory wins and
// the later definition is reported. Load fails only when a directory exists
// but cannot be read; it then returns no definitions, built-ins included, and
// the problems found before the failure.
func Load(ctx context.Context, cfg Config) ([]Definition, []Problem, error) {
	var (
		defs     []Definition
		problems []Problem
	)
	revision := BuiltinRevision(cfg.BuildID)
	for _, def := range builtins() {
		rec, err := builtinRecord(def, revision)
		if err != nil {
			// The built-ins are fixed; a failure here is a programming error
			// that every test run would catch.
			return nil, nil, fmt.Errorf("built-in definition %s/%s: %w", SourceSystem, def.Name, err)
		}
		defs = append(defs, Definition{Record: rec, Def: def})
	}

	// Conventional stays false, so only the explicit directories are resolved:
	// never the project or user tiers.
	sources := agentfs.ResolveSources(agentfs.ResolveOptions{Explicit: cfg.Dirs})
	discovered, skips, err := agentfs.NewMultiSource(sources...).Agents(ctx)
	for _, s := range skips {
		problems = append(problems, Problem{Location: s.Path, Reason: s.Reason, Dropped: s.Fatal})
	}
	if err != nil {
		return nil, problems, fmt.Errorf("load global agent definitions: %w", err)
	}
	for _, d := range discovered {
		rec, err := globalRecord(d)
		if err != nil {
			location := d.Detail
			if d.File != nil {
				location = d.File.Path
			}
			problems = append(problems, Problem{Location: location, Reason: err.Error(), Dropped: true})
			continue
		}
		defs = append(defs, Definition{Record: rec, Def: d.Def})
	}

	slices.SortFunc(defs, func(a, b Definition) int {
		return cmp.Or(
			strings.Compare(a.Record.Ref.Source, b.Record.Ref.Source),
			strings.Compare(a.Record.Ref.Name, b.Record.Ref.Name),
		)
	})
	return defs, problems, nil
}

func builtinRecord(def tool.AgentDef, revision string) (identitybroker.DefinitionRecord, error) {
	accepted, err := acceptedForm(def)
	if err != nil {
		return identitybroker.DefinitionRecord{}, err
	}
	// A built-in has no source file: the bytes it is built from are its
	// accepted form, so both digests are the same.
	digest := sha256Digest(accepted)
	rec := identitybroker.DefinitionRecord{
		Ref: identitybroker.DefinitionRef{Source: SourceSystem, Name: def.Name, Revision: revision},
		Provenance: identitybroker.Provenance{
			Profile:          ProfileRelease,
			SourceDigest:     digest,
			DefinitionDigest: digest,
		},
	}
	return rec, rec.Validate()
}

func globalRecord(d agentfs.Discovered) (identitybroker.DefinitionRecord, error) {
	if d.File == nil {
		return identitybroker.DefinitionRecord{}, fmt.Errorf("definition %q has no source file", d.Def.Name)
	}
	accepted, err := acceptedForm(d.Def)
	if err != nil {
		return identitybroker.DefinitionRecord{}, err
	}
	rec := identitybroker.DefinitionRecord{
		Ref: identitybroker.DefinitionRef{
			Source:   SourceGlobal,
			Name:     d.Def.Name,
			Revision: d.File.ModTime.UTC().Format(time.RFC3339Nano),
		},
		Provenance: identitybroker.Provenance{
			Profile:          ProfileFile,
			Metadata:         map[string]string{"path": d.File.Path},
			SourceDigest:     sha256Digest(d.File.Content),
			DefinitionDigest: sha256Digest(accepted),
		},
	}
	return rec, rec.Validate()
}

// acceptedForm returns the canonical bytes of the definition the harness
// builds an agent from: its JSON encoding, which has a fixed field order and
// sorted map keys. Origin is cleared because it records how the definition was
// admitted, not what it contains. tool.AgentDef has no JSON tags, so every
// field is always encoded: a field added to or renamed in tool.AgentDef changes
// every definition's digest, not only those that set it.
func acceptedForm(def tool.AgentDef) ([]byte, error) {
	def.Origin = ""
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(def); err != nil {
		return nil, fmt.Errorf("encode accepted definition: %w", err)
	}
	return buf.Bytes(), nil
}

func sha256Digest(b []byte) identitybroker.Digest {
	sum := sha256.Sum256(b)
	return identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: hex.EncodeToString(sum[:])}
}
