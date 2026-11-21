package definitionloader

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/agentdef"
)

// TestFileRevision pins the revision to UTC whatever zone the time carries.
// os.Stat reports a modification time in the process's zone, so a broker run
// with TZ set would otherwise print offsets. Setting time.Local in a test
// would race with the parallel tests, so the zone is set on the value.
func TestFileRevision(t *testing.T) {
	t.Parallel()
	modTime := time.Date(2026, 11, 17, 9, 12, 44, 123456789, time.FixedZone("CET", 3600))
	if got, want := fileRevision(modTime), "2026-11-17T08:12:44.123456789Z"; got != want {
		t.Errorf("fileRevision = %q, want %q", got, want)
	}
}

// TestBuiltinRecordRefusesToolFields fails a built-in that sets tool fields,
// which builtinCeiling would ignore, rather than giving it every server.
func TestBuiltinRecordRefusesToolFields(t *testing.T) {
	t.Parallel()
	servers := map[string]bool{"github": true}
	for name, def := range map[string]agentdef.Definition{
		"tools":           {Tools: []string{"Read"}},
		"disallowedTools": {DisallowedTools: []string{"mcp__github__merge"}},
		"mcpServers":      {MCPServers: []agentdef.MCPServer{{Name: "github"}}},
	} {
		def.Name, def.Description = "system-test", "A built-in."
		if _, err := builtinRecord(def, "rev", servers); err == nil {
			t.Errorf("%s: a built-in setting it was accepted", name)
		}
	}
	if _, err := builtinRecord(agentdef.Definition{Name: "system-test", Description: "A built-in."}, "rev", servers); err != nil {
		t.Fatalf("a built-in with no tool fields: %v", err)
	}
}
