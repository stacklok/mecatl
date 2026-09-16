package main

import (
	"bytes"
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
	if !strings.Contains(output.String(), "calendar\thttps://mcp.example/mcp\tnone\t"+canonical) {
		t.Fatalf("list output = %q", output.String())
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

