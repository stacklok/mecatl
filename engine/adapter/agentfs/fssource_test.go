package agentfs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestFSSourceSnapshotAndDetail pins the FS source's snapshot + the NON-PORT
// detail channel: ListAgentDefs serves the discovered defs (no locator on the
// port value), while Discovered/Detail carry the "<label>: <path>" locator
// the composition layer's diagnostics read.
func TestFSSourceSnapshotAndDetail(t *testing.T) {
	dir := t.TempDir()
	path := writeDef(t, dir, "rev.md", "---\nname: rev\ndescription: reviews\n---\nbody")

	src, skips, err := NewFSSource(context.Background(), DirSource{Dir: dir, Label: "explicit"})
	if err != nil || len(skips) != 0 {
		t.Fatalf("NewFSSource: err=%v skips=%v", err, skips)
	}
	defs, err := src.ListAgentDefs(context.Background())
	if err != nil || len(defs) != 1 {
		t.Fatalf("ListAgentDefs = %v, %v; want one def", defs, err)
	}
	if defs[0].Name != "rev" || defs[0].Origin != tool.AgentOriginExplicit {
		t.Errorf("def = %+v, want name=rev origin=explicit", defs[0])
	}

	detail, ok := src.Detail("rev")
	if !ok || detail != "explicit: "+path {
		t.Errorf("Detail(rev) = %q, %v; want %q", detail, ok, "explicit: "+path)
	}
	if _, ok := src.Detail("ghost"); ok {
		t.Error("Detail(unknown) must report false")
	}
	disc := src.Discovered()
	if len(disc) != 1 || disc[0].Def.Name != "rev" || disc[0].Detail != "explicit: "+path {
		t.Errorf("Discovered = %+v, want the rev entry with its detail", disc)
	}
}

// TestFSSourceTierStampingViaResolveSources pins the tier labels end to end:
// ResolveSources stamps each conventional location's Tier, DirSource stamps it
// onto every discovered def, and the FS snapshot serves it as the port Origin.
func TestFSSourceTierStampingViaResolveSources(t *testing.T) {
	ws := t.TempDir()
	explicit := t.TempDir()
	writeDef(t, filepath.Join(ws, ".mecatl", "agents"), "proj.md", "---\nname: proj\ndescription: project def\n---\nbody")
	writeDef(t, explicit, "exp.md", "---\nname: exp\ndescription: explicit def\n---\nbody")

	sources := ResolveSources(ResolveOptions{
		Explicit:           []string{explicit},
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: true,
	})
	src, _, err := NewFSSource(context.Background(), sources...)
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	defs, err := src.ListAgentDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	byName := map[string]tool.AgentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if got := byName["exp"].Origin; got != tool.AgentOriginExplicit {
		t.Errorf("explicit def Origin = %q, want explicit", got)
	}
	if got := byName["proj"].Origin; got != tool.AgentOriginProject {
		t.Errorf("project def Origin = %q, want project", got)
	}
	// The detail keeps the label + path (a locator never crosses the port).
	if detail, ok := src.Detail("proj"); !ok || !strings.HasPrefix(detail, "project(.mecatl): ") {
		t.Errorf("Detail(proj) = %q, %v; want a project(.mecatl)-labelled path", detail, ok)
	}
}

// TestFSSourceDefaultsBlankOriginToExplicit pins the hand-constructed-source
// backstop: a Source that stamps no tier yields explicit-origin defs.
func TestFSSourceDefaultsBlankOriginToExplicit(t *testing.T) {
	src, _, err := NewFSSource(context.Background(), staticSource{discovered: []Discovered{
		{Def: AgentDef{Name: "bare", Description: "no tier"}},
	}})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	defs, _ := src.ListAgentDefs(context.Background())
	if len(defs) != 1 || defs[0].Origin != tool.AgentOriginExplicit {
		t.Errorf("defs = %+v, want a single explicit-origin def", defs)
	}
}
