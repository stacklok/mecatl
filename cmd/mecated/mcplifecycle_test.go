package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestDirectMCPOnboarding_Scenario7_ResetRequiresAttendedTerminal(t *testing.T) {
	previous := mcpResetIsTerminal
	mcpResetIsTerminal = func() bool { return false }
	t.Cleanup(func() { mcpResetIsTerminal = previous })
	if err := runMCPResetCustody([]string{"server", "--file", filepath.Join(t.TempDir(), "settings.yaml")}, strings.NewReader("server\n"), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "attended terminal") {
		t.Fatalf("reset error = %v", err)
	}
}

// mcpResetSettingsYAML builds a strictly valid settings document with a
// single native local-key MCP profile rooted at root, so runMCPResetCustody
// can be exercised end to end through real settings parsing.
type mcpResetProfileConfig struct {
	name, root string
}

func mcpResetSettingsYAML(name, root, keyPath string) string {
	return mcpResetSettingsYAMLProfiles([]mcpResetProfileConfig{{name: name, root: root}}, keyPath)
}

func mcpResetSettingsYAMLProfiles(profiles []mcpResetProfileConfig, keyPath string) string {
	var servers strings.Builder
	for _, profile := range profiles {
		_, _ = fmt.Fprintf(&servers, `    - name: %s
      url: "https://mcp.example/mcp"
      auth:
        mode: oauth
        oauth:
          profile: work
          principal: operator
          issuer: "https://mcp.example"
          client: {mode: dcr, dcr: {}}
          scopes: [openid]
          request_refresh_token: false
          credentials:
            mode: local
            local: {root: %q, key: {mode: file, file: {path: %q}}}
          network: {additional_origins: [], private_origins: ["https://mcp.example"], max_redirects: 0}
`, profile.name, profile.root, keyPath)
	}
	return "mcp:\n  mode: global\n  servers:\n" + servers.String()
}

type mcpResetConfirmationReader func([]byte) (int, error)

func (r mcpResetConfirmationReader) Read(p []byte) (int, error) { return r(p) }

func TestDirectMCPOnboarding_Scenario7_ResetCustodyConfirmationFlow(t *testing.T) {
	previous := mcpResetIsTerminal
	mcpResetIsTerminal = func() bool { return true }
	t.Cleanup(func() { mcpResetIsTerminal = previous })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "key")
	first, err := mcpcredential.Resolve(context.Background(), root, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := string(first.Key)

	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte(mcpResetSettingsYAMLProfiles([]mcpResetProfileConfig{{name: "server", root: root}, {name: "backup", root: root}}, keyPath))
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("wrong answer cancels and leaves custody and settings untouched", func(t *testing.T) {
		var output bytes.Buffer
		err := runMCPResetCustody([]string{"server", "--file", path}, strings.NewReader("not-server\n"), &output, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("declined reset error = %v", err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(original) {
			t.Fatalf("declined reset mutated settings:\n%s", got)
		}
		second, openErr := mcpcredential.Open(context.Background(), root, mcpcredential.BackendFile, keyPath, nil)
		if openErr != nil {
			t.Fatalf("declined reset broke existing custody: %v", openErr)
		}
		if string(second.Key) != firstKey {
			t.Fatal("declined reset rotated the custody key")
		}
	})

	var output bytes.Buffer
	resolution := resolveCommand([]string{"mecated", "mcp", "reset-local-credentials", "server", "--file", path})
	if !resolution.handled || resolution.err != nil || resolution.run == nil {
		t.Fatalf("reset command resolution = %#v", resolution)
	}
	if err := resolution.run(strings.NewReader("server\n"), &output, io.Discard); err != nil {
		t.Fatalf("confirmed reset error = %v", err)
	}
	if !strings.Contains(output.String(), "MCP custody reset complete") || !strings.Contains(output.String(), "server, backup") || !strings.Contains(output.String(), "Log in again for each profile") {
		t.Fatalf("confirmed reset output omitted its effect or profile scope: %q", output.String())
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("confirmed reset unexpectedly mutated settings:\n%s", got)
	}
	second, resolveErr := mcpcredential.Resolve(context.Background(), root, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: keyPath})
	if resolveErr != nil {
		t.Fatalf("confirmed reset did not leave usable custody: %v", resolveErr)
	}
	defer clear(second.Key)
	if string(second.Key) == firstKey {
		t.Fatal("confirmed reset retained the old wrapping key")
	}
}

func TestMCPResetPreservesFileKeySharedAcrossRoots(t *testing.T) {
	previous := mcpResetIsTerminal
	mcpResetIsTerminal = func() bool { return true }
	t.Cleanup(func() { mcpResetIsTerminal = previous })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rootA := filepath.Join(t.TempDir(), "credentials-a")
	rootB := filepath.Join(t.TempDir(), "credentials-b")
	sharedKeyPath := filepath.Join(t.TempDir(), "shared-key")
	owner, err := mcpcredential.Resolve(context.Background(), rootA, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: sharedKeyPath})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := string(owner.Key)
	clear(owner.Key)
	if _, err := mcpcredential.Resolve(context.Background(), rootB, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: sharedKeyPath}); err == nil {
		t.Fatal("second root unexpectedly adopted the shared file key")
	}

	path := filepath.Join(t.TempDir(), "settings.yaml")
	settings := mcpResetSettingsYAMLProfiles([]mcpResetProfileConfig{
		{name: "owner", root: rootA}, {name: "other", root: rootB},
	}, sharedKeyPath)
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runMCPResetCustody([]string{"other", "--file", path}, strings.NewReader("other\n"), &output, io.Discard); err != nil {
		t.Fatalf("reset second root: %v", err)
	}
	if !strings.Contains(output.String(), "file key is also configured under another root for profiles: owner") || !strings.Contains(output.String(), "distinct local key path") {
		t.Fatalf("reset did not explain the preserved shared key: %q", output.String())
	}
	if _, err := os.Stat(sharedKeyPath); err != nil {
		t.Fatalf("reset deleted the first root's file key: %v", err)
	}
	stillOwned, err := mcpcredential.Open(context.Background(), rootA, mcpcredential.BackendFile, sharedKeyPath, nil)
	if err != nil {
		t.Fatalf("first root custody was damaged: %v", err)
	}
	defer clear(stillOwned.Key)
	if string(stillOwned.Key) != ownerKey {
		t.Fatal("first root key changed after resetting the other root")
	}
}

func TestMCPResetFailsClosedForUnavailableRootSharingFileKey(t *testing.T) {
	previous := mcpResetIsTerminal
	mcpResetIsTerminal = func() bool { return true }
	t.Cleanup(func() { mcpResetIsTerminal = previous })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := filepath.Join(t.TempDir(), "credentials")
	keyPath := filepath.Join(t.TempDir(), "shared-key")
	owner, err := mcpcredential.Resolve(context.Background(), root, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := string(owner.Key)
	store, err := credentialstore.NewEncryptedFile(root, mcpcredential.NativeNamespace, owner.Key)
	clear(owner.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, "mcp-credential-backend.json")
	beforeMarker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(root, credentialstore.NamespacePhysicalName(mcpcredential.NativeNamespace))
	brokenRoot := filepath.Join(t.TempDir(), "broken-root")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-root"), brokenRoot); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := mcpResetSettingsYAMLProfiles([]mcpResetProfileConfig{
		{name: "owner", root: root}, {name: "unavailable", root: brokenRoot},
	}, keyPath)
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = runMCPResetCustody([]string{"owner", "--file", settings}, strings.NewReader("owner\n"), &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot verify another profile's root") || output.Len() != 0 {
		t.Fatalf("reset with unresolved shared-key root error=%v output=%q", err, output.String())
	}
	afterMarker, err := os.ReadFile(markerPath)
	if err != nil || string(afterMarker) != string(beforeMarker) {
		t.Fatalf("refused reset changed marker: %q, err=%v", afterMarker, err)
	}
	if _, err := os.Stat(nativePath); err != nil {
		t.Fatalf("refused reset changed native namespace: %v", err)
	}
	stillOwned, err := mcpcredential.Open(context.Background(), root, mcpcredential.BackendFile, keyPath, nil)
	if err != nil {
		t.Fatalf("refused reset damaged the selected key: %v", err)
	}
	defer clear(stillOwned.Key)
	if string(stillOwned.Key) != ownerKey {
		t.Fatal("refused reset changed the selected root's key")
	}
}

func TestMCPResetRevalidatesConfirmedSettingsAndRoot(t *testing.T) {
	previous := mcpResetIsTerminal
	mcpResetIsTerminal = func() bool { return true }
	t.Cleanup(func() { mcpResetIsTerminal = previous })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Run("settings changed while awaiting confirmation", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "credentials")
		keyPath := filepath.Join(t.TempDir(), "key")
		first, err := mcpcredential.Resolve(context.Background(), root, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: keyPath})
		if err != nil {
			t.Fatal(err)
		}
		firstKey := string(first.Key)
		clear(first.Key)
		otherRoot := filepath.Join(t.TempDir(), "other-credentials")
		otherKey := filepath.Join(t.TempDir(), "other-key")
		if err := os.MkdirAll(otherRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "settings.yaml")
		before := []byte(mcpResetSettingsYAML("server", root, keyPath))
		after := []byte(mcpResetSettingsYAML("server", otherRoot, otherKey))
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		input := mcpResetConfirmationReader(func(p []byte) (int, error) {
			if err := os.WriteFile(path, after, 0o600); err != nil {
				return 0, err
			}
			return copy(p, []byte("server\n")), nil
		})
		var output bytes.Buffer
		err = runMCPResetCustody([]string{"server", "--file", path}, input, &output, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "scope changed during confirmation") {
			t.Fatalf("changed-settings reset error = %v", err)
		}
		stillOwned, err := mcpcredential.Open(context.Background(), root, mcpcredential.BackendFile, keyPath, nil)
		if err != nil {
			t.Fatalf("settings change damaged original custody: %v", err)
		}
		defer clear(stillOwned.Key)
		if string(stillOwned.Key) != firstKey {
			t.Fatal("settings change rotated the original custody key")
		}
	})

	t.Run("root ancestor symlink changed while awaiting confirmation", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "credentials")
		otherRoot := filepath.Join(base, "other-credentials")
		keyPath := filepath.Join(t.TempDir(), "key")
		if err := os.MkdirAll(otherRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		first, err := mcpcredential.Resolve(context.Background(), root, mcpcredential.Options{Requested: mcpcredential.BackendFile, FilePath: keyPath})
		if err != nil {
			t.Fatal(err)
		}
		firstKey := string(first.Key)
		clear(first.Key)
		alias := filepath.Join(base, "selected-root")
		if err := os.Symlink(root, alias); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(path, []byte(mcpResetSettingsYAML("server", alias, keyPath)), 0o600); err != nil {
			t.Fatal(err)
		}
		input := mcpResetConfirmationReader(func(p []byte) (int, error) {
			if err := os.Remove(alias); err != nil {
				return 0, err
			}
			if err := os.Symlink(otherRoot, alias); err != nil {
				return 0, err
			}
			return copy(p, []byte("server\n")), nil
		})
		if err := runMCPResetCustody([]string{"server", "--file", path}, input, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "scope changed during confirmation") {
			t.Fatalf("changed-root reset error = %v", err)
		}
		stillOwned, err := mcpcredential.Open(context.Background(), root, mcpcredential.BackendFile, keyPath, nil)
		if err != nil {
			t.Fatalf("symlink change damaged original custody: %v", err)
		}
		defer clear(stillOwned.Key)
		if string(stillOwned.Key) != firstKey {
			t.Fatal("symlink change rotated the original custody key")
		}
	})
}

func TestMCPResetHelpDescribesRemovalAndListsTopLevel(t *testing.T) {
	var topLevel, group strings.Builder
	writeTopLevelCommands(&topLevel)
	writeMCPHelp(&group)
	if !strings.Contains(topLevel.String(), "mcp login SERVER") || !strings.Contains(topLevel.String(), "mcp reset-local-credentials NAME") {
		t.Fatalf("top-level commands omit MCP login or reset: %q", topLevel.String())
	}
	if !strings.Contains(group.String(), "reset-local-credentials NAME [--file PATH]") || !strings.Contains(group.String(), "discard saved local MCP sign-in credentials") || !strings.Contains(group.String(), "Log in again for every affected profile") || strings.Contains(group.String(), "recreate") || strings.Contains(group.String(), "reset-custody") {
		t.Fatalf("MCP help misstates reset behavior: %q", group.String())
	}
}

func TestFindMCPResetTarget_SharedProfilesAndKeyEnvSharing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	nativeKey := &permconfig.MCPNativeCredentialKey{Mode: "file", File: &permconfig.MCPFileCredentialKey{Path: filepath.Join(t.TempDir(), "key")}}
	target := permconfig.MCPServerProfile{Name: "primary", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, Key: nativeKey}}}}}
	shared := permconfig.MCPServerProfile{Name: "secondary", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, Key: nativeKey}}}}}

	t.Run("shared profiles sharing the same root are reported", func(t *testing.T) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := permconfig.Config{MCP: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{target, shared}}}
		got, err := findMCPResetTarget(cfg, "primary")
		if err != nil {
			t.Fatal(err)
		}
		// The target profile itself is reported alongside other native
		// profiles that share its root.
		if len(got.shared) != 2 || got.shared[0] != "primary" || got.shared[1] != "secondary" {
			t.Fatalf("shared profiles = %#v, want [primary secondary]", got.shared)
		}
	})

	t.Run("different roots cannot share a file key", func(t *testing.T) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		otherRoot := filepath.Join(t.TempDir(), "other-credentials")
		if err := os.MkdirAll(otherRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		other := permconfig.MCPServerProfile{Name: "other-root", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: otherRoot, Key: nativeKey}}}}}
		cfg := permconfig.Config{MCP: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{target, other}}}
		got, err := findMCPResetTarget(cfg, "primary")
		if err != nil || !got.sharedFileKey || len(got.externalFileProfiles) != 1 || got.externalFileProfiles[0] != "other-root" {
			t.Fatalf("cross-root shared key target = %#v, error = %v, want preserved artifact and profile list", got, err)
		}
	})

	t.Run("unavailable root with the same file key blocks reset", func(t *testing.T) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		brokenRoot := filepath.Join(t.TempDir(), "broken-root")
		if err := os.Symlink(filepath.Join(t.TempDir(), "missing-root"), brokenRoot); err != nil {
			t.Fatal(err)
		}
		other := permconfig.MCPServerProfile{Name: "unavailable-root", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: brokenRoot, Key: nativeKey}}}}}
		cfg := permconfig.Config{MCP: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{target, other}}}
		if _, err := findMCPResetTarget(cfg, "primary"); err == nil || !strings.Contains(err.Error(), "cannot verify another profile's root") {
			t.Fatalf("reset with an unavailable profile sharing its key = %v, want fail-closed error", err)
		}
	})

	t.Run("legacy key_env profile sharing a prototype-marker root is simply skipped, not ambiguous", func(t *testing.T) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "mcp-credential-backend.json"), []byte(`{"version":1,"backend":"file","locator_sha256":"`+strings.Repeat("a", 64)+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		legacy := permconfig.MCPServerProfile{Name: "legacy", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, KeyEnv: "MECATL_LEGACY_KEY"}}}}}
		cfg := permconfig.Config{MCP: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{target, legacy}}}
		got, err := findMCPResetTarget(cfg, "primary")
		if err != nil {
			t.Fatalf("reset target with a shared key_env profile refused: %v", err)
		}
		// The key_env profile uses the legacy namespace with its own
		// independently supplied key, which reset never touches, so it is
		// omitted from the shared list rather than blocking the reset.
		if len(got.shared) != 1 || got.shared[0] != "primary" {
			t.Fatalf("shared profiles = %#v, want [primary]", got.shared)
		}
	})
}

func TestDirectMCPOnboarding_Scenario3_AddOrderingAndResiduals(t *testing.T) {
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
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	if err := runMCPAdd([]string{"calendar", server.URL, "--file", target, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("add accepted a target shadowed by another MCP source")
	}
	if requests != 0 {
		t.Fatalf("discovery requests = %d, want zero after target validation failed", requests)
	}
}

func TestDirectMCPOnboarding_Scenario3_BrokerModeRejectedBeforeDiscoveryOrCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  mode: broker\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mcp.mode is broker") {
		t.Fatalf("broker add error = %v, want explicit broker rejection", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("broker add mutated settings:\n%s", got)
	}
	if strings.Contains(output.String(), "discovering protected resource") || strings.Contains(output.String(), "selecting credential custody") {
		t.Fatalf("broker add performed a later onboarding stage: %q", output.String())
	}
}
func TestMCPCredentialStatusProjection(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "credentials")
	server := permconfig.MCPServerProfile{Name: "oauth", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: localRoot, Key: &permconfig.MCPNativeCredentialKey{Mode: "file"}}}}}}
	if got := mcpCredentialStatus(server); got != "login required" {
		t.Fatalf("missing local custody status = %q, want login required", got)
	}
	if err := os.MkdirAll(localRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "mcp-credential-backend.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mcpCredentialStatus(server); got != "recovery required" {
		t.Fatalf("invalid marker status = %q, want recovery required", got)
	}
}
func TestDirectMCPOnboarding_Scenario5_BoundedTruthfulStatus(t *testing.T) {
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

func TestDirectMCPOnboarding_ListProhibitsNetworkCustodyBrowserAndMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { requests++ }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	data := strings.Replace(mcpLoginOAuthYAML("local", "local", root), "https://mcp.example/mcp", server.URL, 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_LIST_CANARY", "must-not-be-read")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || strings.Contains(output.String(), "must-not-be-read") {
		t.Fatalf("list performed network/custody access: requests=%d output=%q", requests, output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("list mutated settings")
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created mutation lock: %v", err)
	}
	if strings.Contains(output.String(), "authorize") || strings.Contains(output.String(), "browser") {
		t.Fatalf("list presented browser flow: %q", output.String())
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

func TestDirectMCPOnboarding_Scenario3_ReadOriginsAndWriteTarget(t *testing.T) {
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
	_, _, err := mcpMutationTarget(target)
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

func TestDirectMCPOnboarding_Scenario3_AtomicNarrowMutation(t *testing.T) {
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
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
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

func TestDirectMCPOnboarding_Scenario3_ConfigErrorsNameBrokenLayer(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "malformed", data: []byte("mcp:\n  servers: [\n")},
		{name: "wrong schema", data: []byte("mcp: nope\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runMCPList([]string{"--file", path}, io.Discard); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("list error = %v, want selected path", err)
			} else if strings.Contains(err.Error(), "server is not configured") {
				t.Fatalf("list degraded broken config to absence: %v", err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runMCPList([]string{"--file", missing}, io.Discard); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("unreadable list error = %v, want selected path", err)
	}
}

func TestDirectMCPOnboarding_Scenario2_ExternalAndSharedCustody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	server := permconfig.MCPServerProfile{Name: "external", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "environment"}}}}
	if got := mcpCredentialStatus(server); got != "unknown" {
		t.Fatalf("external-key status = %q, want unknown without reading the key", got)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external-key inspection created local custody: %v", err)
	}
}

func TestDirectMCPOnboarding_Scenario4_CleanupEveryTerminalPath(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("{}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
		return app.ErrMCPLoginConnect
	}
	if err := runMCPAdd([]string{"cleanup", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("login failure was ignored")
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "name: 'cleanup'") {
		t.Fatalf("failed login lost published retry profile: %s", settings)
	}
	if strings.Contains(string(settings), "access-token") {
		t.Fatal("failed login persisted an access token in settings")
	}
}

func TestDirectMCPAddRejectsIllegalNameBeforeDiscovery(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  mode: global\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		t.Fatal("discovery ran for an illegal name")
		return mcp.DirectIssuerDiscovery{}, nil
	}
	var stdout bytes.Buffer
	err := runMCPAdd([]string{"connector-gateway", "https://mcp.example/mcp", "--file", path}, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `(try "connector_gateway")`) {
		t.Fatalf("runMCPAdd error = %v, want name rejection with suggestion", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("progress written before name rejection: %q", stdout.String())
	}
	if settings, err := os.ReadFile(path); err != nil || !bytes.Equal(settings, original) {
		t.Fatalf("settings changed after name rejection: %q, %v", settings, err)
	}
}

func TestDirectMCPOnboarding_Scenario4_ProgressAndVerification(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	loginCalls := 0
	executeMCPLogin = func(_ context.Context, server mcp.ServerConfig, _ oauthlogin.Options, _ app.MCPLoginOptions) error {
		loginCalls++
		if server.Name != "Calendar" || server.OAuth == nil {
			t.Fatalf("login server = %#v", server)
		}
		published, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(published), "name: 'Calendar'") {
			t.Fatalf("login handoff occurred before settings publication: %q, %v", published, err)
		}
		return nil
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"Calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"writable target:", "discovering protected resource", "selecting credential custody", "settings saved", "succeeded for Calendar"} {
		if !strings.Contains(output.String(), stage) {
			t.Fatalf("add output = %q, missing %q", output.String(), stage)
		}
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want one", loginCalls)
	}
	for _, canary := range []string{"access-token", "client-secret", "https://issuer.example/oauth/authorize"} {
		if strings.Contains(output.String(), canary) {
			t.Fatalf("add output leaked %q: %q", canary, output.String())
		}
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: 'Calendar'", "profile: 'calendar'", "principal: 'local-user'", "issuer: 'https://issuer.example'"} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("published settings = %q, missing %q", settings, want)
		}
	}
}

func TestDirectMCPOnboarding_Scenario5_ListIsOfflineAndNonPresenting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(mcpLoginOAuthYAML("secret", "environment", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_LOGIN_CREDENTIAL", "must-not-be-read")
	var output strings.Builder
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "secret\thttps://mcp.example/mcp\toauth/cimd\tunknown") || strings.Contains(got, "must-not-be-read") {
		t.Fatalf("offline list output = %q", got)
	}
	if strings.Contains(got, "https://issuer.example") || strings.Contains(got, "authorize") {
		t.Fatalf("list presented OAuth details: %q", got)
	}
}

func TestDirectMCPOnboarding_Scenario6_MissingLifecycleRemovesProfileOnly(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(mcpLoginDCRYAML(fixture, filepath.Join(t.TempDir(), "credentials"))), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	calls := 0
	removeMCPOAuthDCR = func(context.Context, string, mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		calls++
		return mcp.OAuthDCRRemovalResult{}, nil
	}
	var output strings.Builder
	if err := runMCPRemove([]string{"connector", "--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(output.String(), "lifecycle record not found") {
		t.Fatalf("remove calls=%d output=%q", calls, output.String())
	}
	remaining, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "connector") {
		t.Fatalf("profile remained after missing-lifecycle removal: %s", remaining)
	}
}

func TestDirectMCPOnboarding_Scenario6_RemovalStateMachine(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := loadMCPLoginProfiles([]string{settings})
	if err != nil {
		t.Fatal(err)
	}
	server, ok := profiles.OAuthServer("connector")
	if !ok {
		profiles.Close()
		t.Fatal("connector seed profile missing")
	}
	mcp.AllowOAuthLoopbackForTest(t, server.OAuth)
	mcp.TrustOAuthCertificateForTest(t, server.OAuth, fixture.server.Certificate())
	prepared, callbackPath, err := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, mcp.OAuthDCRLoginReuse)
	if err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + callbackPath
	prepared.Presenter = mcp.OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})
	controller, err := mcp.NewOAuthController(context.Background(), fixture.resource(), prepared)
	if err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, fixture.resource(), nil)
	if err != nil {
		controller.Close()
		profiles.Close()
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"WWW-Authenticate": {`Bearer scope="openid"`}}, Body: io.NopCloser(strings.NewReader(""))}
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		controller.Close()
		profiles.Close()
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	if err := runMCPRemove([]string{"connector", "--file", settings}, io.Discard); err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	key := dcrLoginRecordKey("mecatl/mcp/oauth-dcr-lifecycle-key/v1", "connector")
	record, err := server.OAuth.CredentialStore.Get(context.Background(), key)
	profiles.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Value), `"state":"removed"`) {
		t.Fatalf("lifecycle record = %s, want removed tombstone", record.Value)
	}
	remaining, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "connector") {
		t.Fatalf("settings retained removed profile: %s", remaining)
	}
}
func TestDirectMCPOnboarding_Scenario1_FailureHasNoSideEffects(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{}, errors.New("discovery fixture failed")
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err == nil {
		t.Fatal("discovery failure was ignored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("discovery failure changed settings: %q", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "mecatl", "mcp-credentials")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery failure created custody state: %v", err)
	}
	if strings.Contains(output.String(), "selecting credential custody") || strings.Contains(output.String(), "settings saved") {
		t.Fatalf("discovery failure continued onboarding: %q", output.String())
	}
}

func TestDirectMCPOnboarding_AddCustodyFailureLeavesSettingsUnpublished(t *testing.T) {
	configBase, stateHome := t.TempDir(), t.TempDir()
	configHome := filepath.Join(configBase, "config")
	// publishMCPAdd derives the file-custody key path from XDG_CONFIG_HOME.
	// Make that path a regular file so custody fails before the settings CAS.
	if err := os.WriteFile(configHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err == nil {
		t.Fatal("custody failure was ignored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("custody failure published settings: %q", got)
	}
	if strings.Contains(output.String(), "settings saved") {
		t.Fatalf("custody failure reached publication: %q", output.String())
	}
}

func TestDirectMCPOnboarding_AddLoginFailureLeavesPublishedProfileForRetry(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	loginCalls := 0
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
		loginCalls++
		return app.ErrMCPLoginConnect
	}
	if err := runMCPAdd([]string{"Calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("login failure was ignored")
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want one", loginCalls)
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "name: 'Calendar'") {
		t.Fatalf("login failure removed published retry profile: %s", settings)
	}
}

func TestDirectMCPOnboarding_RemovePreservesWrappingKeyAndSettingsOnRecoveryFailure(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	removeMCPOAuthDCR = func(context.Context, string, mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		return mcp.OAuthDCRRemovalResult{}, mcp.NewOAuthDCRRecoveryError(mcp.OAuthDCRRecoveryPending)
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMCPRemove([]string{"connector", "--file", settings}, io.Discard); err == nil {
		t.Fatal("recovery-required removal succeeded")
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("recovery-required removal changed settings")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("recovery-required removal deleted wrapping-key custody root: %v", err)
	}
}

func TestDirectMCPOnboarding_CancellationBeforeDiscoveryDoesNotStartWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		called = true
		return mcp.DirectIssuerDiscovery{}, nil
	}
	if err := runMCPAddContext(ctx, []string{"calendar", "https://mcp.example/mcp", "--file", filepath.Join(t.TempDir(), "settings.yaml"), "--credential-store", "file"}, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled add error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("cancelled add started discovery")
	}
}

func TestDirectMCPOnboarding_CancellationDuringDiscoveryDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(ctx context.Context, _ string) (mcp.DirectIssuerDiscovery, error) {
		cancel()
		<-ctx.Done()
		return mcp.DirectIssuerDiscovery{}, ctx.Err()
	}
	if err := runMCPAddContext(ctx, []string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery-cancelled add error = %v, want context.Canceled", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("cancelled add changed settings: %q", got)
	}
}

func TestDirectMCPOnboarding_CancellationDuringRemovalDoesNotPublish(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, filepath.Join(t.TempDir(), "credentials"))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	removeMCPOAuthDCR = func(ctx context.Context, _ string, _ mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		cancel()
		<-ctx.Done()
		return mcp.OAuthDCRRemovalResult{}, ctx.Err()
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMCPRemoveContext(ctx, []string{"connector", "--file", settings}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled remove error = %v, want context.Canceled", err)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("cancelled remove changed settings")
	}
}

func forcedDirectDCRSettings(t *testing.T, root string) []byte {
	t.Helper()
	data := mcpLoginOAuthYAML("connector", "local", root)
	data = strings.Replace(data, "mode: cimd\n            cimd: {document_url: https://client.example/metadata.json}", "mode: dcr\n            dcr: {}", 1)
	data = strings.Replace(data, "scopes: [read]", "scopes: [openid]", 1)
	return []byte(data)
}

func TestMCPAbandonUnavailable_Scenario1_Eligibility(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	original := append([]byte("# keep this note\n"), forcedDirectDCRSettings(t, filepath.Join(t.TempDir(), "credentials"))...)
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldLookup := lookupMCPEnvironment
	t.Cleanup(func() { lookupMCPEnvironment = oldLookup })
	lookupMCPEnvironment = func(string) (string, bool) { return "", false }
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	removeMCPOAuthDCR = func(context.Context, string, mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		t.Fatal("forced removal opened the OAuth lifecycle")
		return mcp.OAuthDCRRemovalResult{}, nil
	}
	var output bytes.Buffer
	if err := runMCPRemove([]string{"connector", "--force", "--file", settings}, &output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "connector") || !strings.Contains(string(got), "# keep this note") {
		t.Fatalf("forced removal changed the wrong settings: %s", got)
	}
	want := "MCP profile abandoned locally; encrypted records were retained, no upstream client was revoked, and later re-enrollment may create a new registration."
	if !strings.Contains(output.String(), want) {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestMCPAbandonUnavailable_Scenario1_FinalEligibilityRecheck(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, forcedDirectDCRSettings(t, filepath.Join(t.TempDir(), "credentials")), 0o600); err != nil {
		t.Fatal(err)
	}
	oldLookup := lookupMCPEnvironment
	t.Cleanup(func() { lookupMCPEnvironment = oldLookup })
	calls := 0
	lookupMCPEnvironment = func(string) (string, bool) {
		calls++
		if calls == 1 {
			return "", false
		}
		return "became-available", true
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMCPRemove([]string{"connector", "--force", "--file", settings}, io.Discard); err == nil {
		t.Fatal("forced removal succeeded after custody became available")
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("final eligibility rejection changed settings")
	}
	if calls != 2 {
		t.Fatalf("environment checks = %d, want initial and final checks", calls)
	}
}

func TestMCPAbandonUnavailable_Scenario1_RejectionsPreserveState(t *testing.T) {
	cases := []struct {
		name  string
		data  []byte
		key   string
		value string
	}{
		{name: "readable legacy key", data: forcedDirectDCRSettings(t, filepath.Join(t.TempDir(), "credentials")), key: "MECATL_LOGIN_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 32))},
		{name: "malformed legacy key", data: forcedDirectDCRSettings(t, filepath.Join(t.TempDir(), "credentials")), key: "MECATL_LOGIN_KEY", value: "not-a-key"},
		{name: "native custody", data: func() []byte {
			data := forcedDirectDCRSettings(t, filepath.Join(t.TempDir(), "credentials"))
			return []byte(strings.Replace(string(data), "key_env: MECATL_LOGIN_KEY", "key: {mode: keyring}", 1))
		}(), key: "MECATL_LOGIN_KEY", value: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(settings, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.key, tc.value)
			before, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := runMCPRemove([]string{"connector", "--force", "--file", settings}, io.Discard); err == nil {
				t.Fatal("forced removal unexpectedly succeeded")
			}
			after, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected forced removal changed settings")
			}
		})
	}
}
