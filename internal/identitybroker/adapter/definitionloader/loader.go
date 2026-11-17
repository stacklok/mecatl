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
// definition has no file bytes to digest. The harness gives a user-tier
// definition its parent's authority rather than its own, so the broker does
// not record it as global either; an operator who mounts definitions under an
// XDG configuration root puts them in the user tier, where the broker does not
// see them.
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
// definition. These revisions must not back durable broker state: in
// Kubernetes, every new pod rewrites mounted files with a new modification
// time, so restored registrations would fail closed after each rollout.
package definitionloader

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	// ProfileFile is the provenance profile of a global definition read from a
	// file. Its metadata holds the file's path under the key "path"; its
	// revision is the file's modification time in UTC, RFC 3339 with
	// nanoseconds.
	ProfileFile = "file/v1"
	// ProfileRelease is the provenance profile of a built-in definition. It
	// has no metadata; its revision is BuiltinRevision of the build.
	ProfileRelease = "mecatl-release/v1"

	// DevRevision is the built-in definitions' revision in any build that is
	// not published, such as a local, dirty, or untagged build.
	DevRevision = "dev"
)

// publishedVersion matches the build stamp of a published build: a release
// tag, such as v0.0.51, or a published main build, such as
// v0.0.52-main.812.1.gabcdef12 (.github/workflows/continuous.yml). A git
// describe stamp of an untagged commit (v0.0.51-3-gabc1234), a dirty tree, and
// dev builds do not match: they never back a deployment.
var publishedVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-main\.[0-9]+\.[0-9]+\.g[0-9a-f]{8})?$`)

// BuiltinRevision returns the built-in definitions' revision for a Mecatl
// build stamp (buildinfo.BuildID): the stamp itself for a published build, a
// release or a main build, so no two published builds share a revision;
// otherwise DevRevision.
func BuiltinRevision(buildID string) string {
	if publishedVersion.MatchString(buildID) {
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
		{Name: identitybroker.BuiltinMain, Description: "The main agent of a session started without a named definition."},
		{Name: identitybroker.BuiltinExplorer, Description: "The child agent of a subagent call that names no definition."},
	}
}

// Config selects what Load reads.
type Config struct {
	// Dirs are the operator's explicit agent definition directories, highest
	// precedence first: the same list the harness reads (its --agents-dir
	// flag). Unlike the harness, Load fails on a configured directory that
	// does not exist, so a mistyped path or an unmounted volume shows at
	// startup rather than as a refused registration later. An empty Dirs loads
	// the built-ins only.
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
	// Def is the accepted definition: the parsed value Record's
	// DefinitionDigest covers, and the one the harness builds the agent from.
	// The digest does not cover what the harness resolves later from other
	// inputs, such as model aliases, the available tools, skill bodies, or
	// memory. Building from a separate read of the source could build a
	// definition the broker never recorded.
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
// identitybroker.DefinitionRecord.Validate, or whose file is not valid UTF-8,
// is left out and loading continues. When two directories define the same
// name, the earlier directory wins and the later definition is reported. Load
// fails when a configured directory is missing, is not a directory, or cannot
// be read; it then returns no definitions, built-ins included, and the
// problems found before the failure.
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
			return nil, nil, fmt.Errorf("built-in definition %s/%s: %w", identitybroker.SourceSystem, def.Name, err)
		}
		defs = append(defs, Definition{Record: rec, Def: def})
	}

	for _, dir := range cfg.Dirs {
		if dir == "" {
			// ResolveSources skips an empty entry too.
			continue
		}
		// A path that is not a directory fails when it is read below.
		if _, err := os.Stat(dir); err != nil {
			return nil, nil, fmt.Errorf("configured agent definition directory: %w", err)
		}
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
		Ref: identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: def.Name, Revision: revision},
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
	// The parser does not check UTF-8, and the JSON encoding of the accepted
	// form replaces each invalid byte with U+FFFD, so two different files
	// could share a DefinitionDigest.
	if !utf8.Valid(d.File.Content) {
		return identitybroker.DefinitionRecord{}, fmt.Errorf("definition %q is not valid UTF-8", d.Def.Name)
	}
	accepted, err := acceptedForm(d.Def)
	if err != nil {
		return identitybroker.DefinitionRecord{}, err
	}
	rec := identitybroker.DefinitionRecord{
		Ref: identitybroker.DefinitionRef{
			Source:   identitybroker.SourceGlobal,
			Name:     d.Def.Name,
			Revision: fileRevision(d.File.ModTime),
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

// fileRevision is a global definition's revision: its file's modification
// time in UTC, RFC 3339 with nanoseconds, so it does not depend on the
// process's time zone.
func fileRevision(modTime time.Time) string {
	return modTime.UTC().Format(time.RFC3339Nano)
}

// acceptedForm returns the canonical bytes of the definition the harness
// builds an agent from: its JSON encoding, which has a fixed field order and
// sorted map keys. Origin is cleared because it records how the definition was
// admitted, not what it contains. tool.AgentDef has no JSON tags, so every
// field is always encoded: a field added to or renamed in tool.AgentDef changes
// every definition's digest, not only those that set it. The form is Go's
// encoding/json output of tool.AgentDef (Go field names, a trailing newline,
// null for nil and [] for empty): internal to Go and versioned with that
// struct, not a portable hashing rule.
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
