// Package definitionloader loads the agent definitions the identity broker
// knows: the operator's global definitions and the built-in definitions each
// Mecatl release ships. It returns the record identitybroker.New takes for
// each. It parses definitions with the shared agentdef format and imports
// nothing else from Mecatl but the broker core, so the broker can move into its
// own service without the harness. The broker core stays free of file access.
//
// Global definitions come only from the operator's explicit directories (the
// harness's --agents-dir). The harness also reads project and user directories
// and remote drivers; the broker never loads those. Only the explicit
// directories are operator-managed, and a driver definition has no file bytes
// to digest. The harness gives a user-tier definition its parent's authority
// rather than its own, so the broker does not record it as global either; an
// operator who mounts definitions under an XDG configuration root puts them in
// the user tier, where the broker does not see them.
//
// Until the harness builds agents from the broker's records (#2548), the
// harness reads the same files separately, with the same agentdef parser
// through the engine's agentfs adapter.
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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/agentdef"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	// ProfileFile is the provenance profile of a global definition read from a
	// file. Its metadata holds the file's path under the key "path" and the
	// canonical form version under MetadataDefinitionForm; its revision is the
	// file's modification time in UTC, RFC 3339 with nanoseconds.
	ProfileFile = "file/v1"
	// ProfileRelease is the provenance profile of a built-in definition. Its
	// metadata holds the canonical form version under MetadataDefinitionForm;
	// its revision is BuiltinRevision of the build.
	ProfileRelease = "mecatl-release/v1"

	// MetadataDefinitionForm is the provenance metadata key that names the
	// canonical form DefinitionDigest covers (agentdef.FormVersion), so a
	// change of form is visible in the record rather than only as a new
	// digest.
	MetadataDefinitionForm = "definition-form"

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
// builds system/main and its def-less helpers from these definitions).
func builtins() []agentdef.Definition {
	return []agentdef.Definition{
		{Name: identitybroker.BuiltinMain, Description: "The main agent of a session started without a named definition."},
		{Name: identitybroker.BuiltinExplorer, Description: "The child agent of a subagent call that names no definition."},
		{Name: identitybroker.BuiltinBranch, Description: "A parallel branch."},
		{Name: identitybroker.BuiltinMember, Description: "A team member that names no agent type."},
	}
}

// Config selects what Load reads.
type Config struct {
	// Dirs are the operator's explicit agent definition directories, highest
	// precedence first: the same list the harness reads (its --agents-dir
	// flag). Unlike the harness, Load fails on a configured directory that
	// does not exist, so a mistyped path or an unmounted volume shows at
	// startup rather than as a refused registration later. A blank entry is
	// skipped, as the harness skips it. An empty Dirs loads the built-ins only.
	Dirs []string
	// BuildID is the Mecatl build stamp (buildinfo.BuildID); see
	// BuiltinRevision.
	BuildID string
	// Servers are the gateway servers, by name: the registered MCP servers
	// each definition's ceiling is resolved against (see resolveCeiling).
	// With none, every ceiling allows no call.
	Servers []string
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

// Load returns the record of each built-in definition and each valid global
// definition, sorted by source and name, with a Problem for each definition it
// left out or adjusted.
//
// It reads every <name>.md file directly in each configured directory and
// parses it with agentdef.Parse. A file that cannot be read or parsed, that is
// not valid UTF-8, whose definition has no digest (agentdef.Digest), or whose
// record fails identitybroker.DefinitionRecord.Validate is left out and
// loading continues.
// The first file to define a name wins it: directories in the order given, and
// files in name order within one. Each later file is reported. A winner left
// out for a bad record still holds the name, as it does in the harness, so no
// later definition is recorded under it. Load
// fails when a configured directory is missing, is not a directory, or cannot
// be read; it then returns no records, built-ins included, and the problems
// found before the failure.
//
// The digests are plain SHA-256 over the definition, which can include an
// inline MCP server's secret-shaped headers. They reveal no secret that has
// enough randomness, but anyone who sees a digest and knows the rest of the
// file can test guesses at a short one. Keep them out of logs and records that
// people who know the rest of the file can read.
func Load(ctx context.Context, cfg Config) ([]identitybroker.DefinitionRecord, []Problem, error) {
	var (
		records  []identitybroker.DefinitionRecord
		problems []Problem
	)
	revision := BuiltinRevision(cfg.BuildID)
	servers := serverSet(cfg.Servers)
	for _, def := range builtins() {
		rec, err := builtinRecord(def, revision, servers)
		if err != nil {
			// The built-ins are fixed; a failure here is a programming error
			// that every test run would catch.
			return nil, nil, fmt.Errorf("built-in definition %s/%s: %w", identitybroker.SourceSystem, def.Name, err)
		}
		records = append(records, rec)
	}

	kept := map[string]string{} // definition name -> path of the file that won it
	for _, dir := range cfg.Dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, problems, err
		}
		files, dirProblems, err := readDir(dir)
		problems = append(problems, dirProblems...)
		if err != nil {
			return nil, problems, err
		}
		for _, f := range files {
			if prev, taken := kept[f.def.Name]; taken {
				problems = append(problems, Problem{
					Location: f.path,
					Reason:   fmt.Sprintf("duplicate agent name %q (already defined at %q)", f.def.Name, prev),
					Dropped:  true,
				})
				continue
			}
			// The name is taken even when the record below fails: the harness
			// runs this file's definition under the name, so the broker must
			// not record a later file's definition for it.
			kept[f.def.Name] = f.path
			rec, err := globalRecord(f, servers)
			if err != nil {
				problems = append(problems, Problem{Location: f.path, Reason: err.Error(), Dropped: true})
				continue
			}
			records = append(records, rec)
		}
	}

	slices.SortFunc(records, func(a, b identitybroker.DefinitionRecord) int {
		return cmp.Or(
			strings.Compare(a.Ref.Source, b.Ref.Source),
			strings.Compare(a.Ref.Name, b.Ref.Name),
		)
	})
	return records, problems, nil
}

// file is one definition file read from a directory: its path, the exact
// bytes read, their modification time, and the definition parsed from them.
type file struct {
	path    string
	content []byte
	modTime time.Time
	def     agentdef.Definition
}

// readDir reads and parses every definition file directly in dir, in name
// order. A subdirectory or a file without the agentdef.FileExt extension is
// ignored. It returns an error only when dir itself cannot be read.
func readDir(dir string) ([]file, []Problem, error) {
	// os.ReadDir returns the entries sorted by name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read agent definition directory: %w", err)
	}
	var (
		files    []file
		problems []Problem
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), agentdef.FileExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, modTime, err := readFile(path)
		if err != nil {
			problems = append(problems, Problem{Location: path, Reason: fmt.Sprintf("cannot read: %v", err), Dropped: true})
			continue
		}
		def, notes, err := agentdef.Parse(content)
		if err != nil {
			problems = append(problems, Problem{Location: path, Reason: err.Error(), Dropped: true})
			continue
		}
		for _, n := range notes {
			problems = append(problems, Problem{Location: path, Reason: n})
		}
		files = append(files, file{path: path, content: content, modTime: modTime, def: def})
	}
	return files, problems, nil
}

// readFile reads path and returns its bytes with the modification time of the
// same open file, so a symlink swap between the two reads cannot pair one
// file's bytes with another's time. A symlink is followed, so both describe
// its target.
func readFile(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	return content, info.ModTime(), nil
}

func builtinRecord(def agentdef.Definition, revision string, servers map[string]bool) (identitybroker.DefinitionRecord, error) {
	digest, err := definitionDigest(def)
	if err != nil {
		return identitybroker.DefinitionRecord{}, err
	}
	// A built-in has no source file: the bytes it is built from are its
	// canonical form, so both digests are the same.
	rec := identitybroker.DefinitionRecord{
		Ref: identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: def.Name, Revision: revision},
		Provenance: identitybroker.Provenance{
			Profile:          ProfileRelease,
			Metadata:         map[string]string{MetadataDefinitionForm: agentdef.FormVersion},
			SourceDigest:     digest,
			DefinitionDigest: digest,
		},
		Ceiling: builtinCeiling(servers),
	}
	return rec, rec.Validate()
}

// globalRecord builds the record of a definition read from a file. Its
// SourceDigest covers the bytes read; its DefinitionDigest covers the
// definition (agentdef.Digest), so reformatting the file changes only
// SourceDigest.
func globalRecord(f file, servers map[string]bool) (identitybroker.DefinitionRecord, error) {
	// The YAML parser reads an invalid byte in the frontmatter as U+FFFD, so
	// two different files could parse to one definition. The parser itself
	// accepts them, as the harness's copy does, so the whole file is checked
	// here.
	if !utf8.Valid(f.content) {
		return identitybroker.DefinitionRecord{}, fmt.Errorf("definition %q is not valid UTF-8", f.def.Name)
	}
	digest, err := definitionDigest(f.def)
	if err != nil {
		return identitybroker.DefinitionRecord{}, fmt.Errorf("definition %q: %w", f.def.Name, err)
	}
	rec := identitybroker.DefinitionRecord{
		Ref: identitybroker.DefinitionRef{
			Source:   identitybroker.SourceGlobal,
			Name:     f.def.Name,
			Revision: fileRevision(f.modTime),
		},
		Provenance: identitybroker.Provenance{
			Profile:          ProfileFile,
			Metadata:         map[string]string{"path": f.path, MetadataDefinitionForm: agentdef.FormVersion},
			SourceDigest:     sha256Digest(f.content),
			DefinitionDigest: digest,
		},
		Ceiling: resolveCeiling(f.def, servers),
	}
	return rec, rec.Validate()
}

// serverSet returns the non-blank names in servers as a set.
func serverSet(servers []string) map[string]bool {
	set := make(map[string]bool, len(servers))
	for _, s := range servers {
		if s = strings.TrimSpace(s); s != "" {
			set[s] = true
		}
	}
	return set
}

// builtinCeiling is a built-in definition's ceiling: every gateway server,
// whole. The main agent of a session started without a named definition
// calls any of them, as the main conversation does; a built-in helper's
// parent's ceiling narrows it at registration.
func builtinCeiling(servers map[string]bool) identitybroker.Ceiling {
	grants := make([]identitybroker.CeilingServer, 0, len(servers))
	for s := range servers {
		grants = append(grants, identitybroker.CeilingServer{Server: s})
	}
	return identitybroker.NewCeiling(grants...)
}

// resolveCeiling resolves a global definition's ceiling against the gateway
// servers, with the harness's meaning (internal/app/agentdefs.go): each
// server the definition's mcpServers names by reference is granted with all
// its tools, except each tool its disallowedTools names by its catalog name,
// mcp__<server>__<tool>. tools cannot select MCP tools in the harness, so it
// grants none here. An inline server, or a reference to a server the gateway
// does not route, grants nothing. A disallowed name is matched against every
// granted server, so one that could belong to two servers is excluded from
// both: an exclusion only ever narrows.
func resolveCeiling(def agentdef.Definition, servers map[string]bool) identitybroker.Ceiling {
	var grants []identitybroker.CeilingServer
	for _, ref := range def.MCPServers {
		name := strings.TrimSpace(ref.Name)
		if !ref.IsReference() || !servers[name] {
			continue
		}
		grant := identitybroker.CeilingServer{Server: name}
		for _, disallowed := range def.DisallowedTools {
			if tool, ok := strings.CutPrefix(disallowed, "mcp__"+name+"__"); ok {
				grant.ExceptTools = append(grant.ExceptTools, tool)
			}
		}
		grants = append(grants, grant)
	}
	return identitybroker.NewCeiling(grants...)
}

// definitionDigest returns the agentdef digest of d as a broker digest.
func definitionDigest(d agentdef.Definition) (identitybroker.Digest, error) {
	value, err := agentdef.Digest(d)
	if err != nil {
		return identitybroker.Digest{}, err
	}
	return identitybroker.Digest{Algorithm: agentdef.DigestAlgorithm, Value: value}, nil
}

// fileRevision is a global definition's revision: its file's modification
// time in UTC, RFC 3339 with nanoseconds, so it does not depend on the
// process's time zone.
func fileRevision(modTime time.Time) string {
	return modTime.UTC().Format(time.RFC3339Nano)
}

func sha256Digest(b []byte) identitybroker.Digest {
	sum := sha256.Sum256(b)
	return identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: hex.EncodeToString(sum[:])}
}
