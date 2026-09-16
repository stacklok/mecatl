package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPLifecycleListAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	data := "# operator note\nmcp:\n  servers:\n    - name: calendar\n      url: https://mcp.example/mcp\n      auth: {mode: none}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	canonical, err := mcpSettingsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "calendar\thttps://mcp.example/mcp\tnone\tready\t"+canonical) {
		t.Fatalf("list output = %q", output.String())
	}
	if !strings.Contains(output.String(), "MCP settings source: "+canonical) || !strings.Contains(output.String(), "MCP settings winner: "+canonical) {
		t.Fatalf("list provenance = %q", output.String())
	}
	output.Reset()
	if err := runMCPRemove([]string{"calendar", "--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	remaining, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "calendar") || !strings.Contains(string(remaining), "# operator note") {
		t.Fatalf("settings after remove:\n%s", remaining)
	}
	if !strings.Contains(output.String(), "no upstream client was revoked") {
		t.Fatalf("remove output = %q", output.String())
	}
}

func TestMCPSettingsLockSerializesSafeLock(t *testing.T) {
	path, err := mcpSettingsFile(filepath.Join(t.TempDir(), "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock, err = lockMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestMCPSettingsLockRejectsNonOwnerOnlyFile(t *testing.T) {
	path, err := mcpSettingsFile(filepath.Join(t.TempDir(), "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockMCPSettings(path); err == nil {
		t.Fatal("lockMCPSettings accepted a non-owner-only lock")
	}
}

func TestMCPSettingsRejectsExternalMCPSourcesForMutation(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	defaultPath := filepath.Join(config, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte("mcp: {servers: []}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(target, []byte("permissions: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := mcpMutationTarget(target)
	if err == nil || !strings.Contains(err.Error(), defaultPath) || !strings.Contains(err.Error(), target) {
		t.Fatalf("mcpMutationTarget error = %v", err)
	}
}

func TestMCPLoginFileAndPermissionConfigAreMutuallyExclusive(t *testing.T) {
	if _, err := parseMCPLoginArgs([]string{"server", "--file", "settings.yaml", "--permission-config", "other.yaml"}, io.Discard); err == nil {
		t.Fatal("accepted mutually exclusive settings selectors")
	}
	parsed, err := parseMCPLoginArgs([]string{"server", "--file=settings.yaml"}, io.Discard)
	if err != nil || parsed.file != "settings.yaml" {
		t.Fatalf("parseMCPLoginArgs = %#v, %v", parsed, err)
	}
}

func TestMCPSettingsRejectsSymlinkAndStalePublication(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readMCPSettings(link); err == nil {
		t.Fatal("read accepted symlink")
	}

	path := filepath.Join(dir, "safe.yaml")
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := readMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mcp:\n  servers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPSettings(path, before, []byte("mcp:\n  servers:\n    - name: replacement\n")); err == nil {
		t.Fatal("write accepted stale settings")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mcp:\n  servers: []\n" {
		t.Fatalf("stale write replaced settings: %q", got)
	}
}

func TestMCPSettingsRejectsSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linkDir, "settings.yaml")
	if _, err := readMCPSettings(path); err == nil {
		t.Fatal("read accepted symlinked parent")
	}
	if err := writeMCPSettings(path, mcpSettingsSnapshot{data: []byte("{}\n")}, []byte("mcp: {}\n")); err == nil {
		t.Fatal("write accepted symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(realDir, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatal("write followed symlinked parent")
	}
}

// TestMCPAddDefaultSettingsPathFollowsXDGNotOSNative pins the exact bug this
// fixed: mecated's own xdgconfig convention (XDG_CONFIG_HOME, or ~/.config on
// every OS including macOS) is the ONE location every mecatl command must
// agree on. The stdlib os.UserConfigDir() resolves somewhere else entirely on
// macOS (~/Library/Application Support) — using it here would silently split
// "mcp add"'s default settings file from every other command's, per
// test-isolation.md's default-discovery-test discipline this is
// nonparallel and uses a synthetic HOME/XDG_CONFIG_HOME.
func TestMCPAddDefaultSettingsPathFollowsXDGNotOSNative(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path, err := defaultMCPSettingsFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "mecatl", "settings.yaml")
	if path != want {
		t.Fatalf("defaultMCPSettingsFile() = %q, want %q (an OS-native os.UserConfigDir() path would diverge from every other mecatl command's default)", path, want)
	}
}
