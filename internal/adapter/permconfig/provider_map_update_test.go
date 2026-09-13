//go:build linux || darwin

package permconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

// TestProviderUnification_Scenario5_LinuxAndDarwinWriteSupport is compiled only
// for the two supported desktop Unix platforms by this file's build constraint.
// A Darwin-native run is still required to claim macOS execution; this host run
// establishes no such claim.
func TestProviderUnification_Scenario5_LinuxAndDarwinWriteSupport(t *testing.T) {
	if !authfile.UpdateSupported() {
		t.Fatal("authfile writer is unsupported despite the linux/darwin build constraint")
	}
}

func TestUpdateProviderMap_AddReplaceRemovePreservesSettings(t *testing.T) {
	path := providerMapSettings(t, "permissions:\n  deny: [Shell]\nmodels:\n  default_provider: openai\n  default: gpt-5\nproviders:\n  old:\n    base_url: https://old.example/v1\n    default_model: old-model\n    api_flavor: openai-responses\n    auth: {method: none}\nunknown_top_level: preserved\n")

	add := ProviderMapUpdate{Provider: "new", Definition: &ProviderDefinition{
		BaseURL: "https://new.example/v1", DefaultModel: "new-model", APIFlavor: "openai-chat-completions", Auth: ProviderAuth{Method: "api_key"},
	}}
	if state, err := UpdateProviderMap(context.Background(), path, add); err != nil || state != authfile.CommitDurable {
		t.Fatalf("add = (%v, %v), want durable success", state, err)
	}
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "old", Definition: &ProviderDefinition{
		BaseURL: "https://replaced.example/v1", DefaultModel: "replaced-model", APIFlavor: "anthropic-messages", Auth: ProviderAuth{Method: "none"},
	}}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("replace = (%v, %v), want durable success", state, err)
	}
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "new"}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("remove = (%v, %v), want durable success", state, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	for _, want := range []string{"deny: [Shell]", "default_provider: openai", "unknown_top_level: preserved", "old:", "https://replaced.example/v1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing preserved or replaced content %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "new:") {
		t.Fatalf("removed provider remains in settings:\n%s", text)
	}
	if err := ValidateYAML(data); err != nil {
		t.Fatalf("updated settings are invalid: %v", err)
	}
}

func TestUpdateProviderMap_RemovesSelectedDefaultWithoutRewritingIt(t *testing.T) {
	path := providerMapSettings(t, "models:\n  default_provider: custom\n  default: custom-model\nproviders:\n  custom:\n    base_url: https://custom.example\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: none}\n")
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom"}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("remove selected default = (%v, %v), want durable success", state, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "default_provider: custom") || strings.Contains(string(data), "custom:\n    base_url") {
		t.Fatalf("removal changed defaults or retained provider:\n%s", data)
	}
	if err := ValidateYAML(data); err != nil {
		t.Fatalf("removal wrote invalid settings: %v\n%s", err, data)
	}
}

func TestUpdateProviderMap_RejectsInvalidOrAmbiguousInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		update      ProviderMapUpdate
	}{
		{"invalid provider", "{}\n", ProviderMapUpdate{Provider: "OpenAI", Definition: &ProviderDefinition{}}},
		{"reserved provider", "{}\n", ProviderMapUpdate{Provider: "openai", Definition: &ProviderDefinition{}}},
		{"invalid definition", "{}\n", ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "http://bad.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}},
		{"ambiguous providers", "providers:\n  custom: {base_url: https://one.example, default_model: one, api_flavor: openai-responses, auth: {method: none}}\n  custom: {base_url: https://two.example, default_model: two, api_flavor: openai-responses, auth: {method: none}}\n", ProviderMapUpdate{Provider: "other", Definition: &ProviderDefinition{BaseURL: "https://other.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := providerMapSettings(t, tc.input)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UpdateProviderMap(context.Background(), path, tc.update); err == nil {
				t.Fatal("UpdateProviderMap succeeded")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsafe update changed settings: %v\n%s", err, after)
			}
		})
	}
}

func TestUpdateProviderMap_AddOIDCAddsOrPreservesCredentialStore(t *testing.T) {
	definition := ProviderDefinition{
		BaseURL: "https://oidc.example/v1", DefaultModel: "model", APIFlavor: "openai-responses",
		Auth: ProviderAuth{Method: "oidc", OIDC: &ProviderOIDC{
			Issuer: "https://issuer.example", ClientID: "client", Scopes: []string{"openid"},
			IssuerTrust: NativeTrust{Policy: "public"}, GatewayTrust: NativeTrust{Policy: "public"},
		}},
	}
	defaultStore := &OIDCCredentialStore{Home: "/var/lib/mecatl/provider-oidc", Key: NativeCredentialKey{Source: "keyring"}}

	t.Run("fresh", func(t *testing.T) {
		path := providerMapSettings(t, "providers: {}\n")
		if _, err := UpdateProviderMap(t.Context(), path, ProviderMapUpdate{Provider: "oidc", Definition: &definition, OIDCCredentialStore: defaultStore}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "credential_store:") || !strings.Contains(string(data), "home: '/var/lib/mecatl/provider-oidc'") {
			t.Fatalf("OIDC credential store was not saved:\n%s", data)
		}
		if err := ValidateYAML(data); err != nil {
			t.Fatalf("OIDC settings are invalid: %v", err)
		}
	})

	t.Run("existing", func(t *testing.T) {
		path := providerMapSettings(t, "credential_store:\n  oidc:\n    home: /existing\n    key: {source: keyring}\n")
		if _, err := UpdateProviderMap(t.Context(), path, ProviderMapUpdate{Provider: "oidc", Definition: &definition, OIDCCredentialStore: defaultStore}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "home: /existing") || strings.Contains(string(data), "home: '/var/lib/mecatl/provider-oidc'") {
			t.Fatalf("existing OIDC credential store was replaced:\n%s", data)
		}
	})
}

func TestUpdateProviderMap_InvalidOIDCDoesNotWrite(t *testing.T) {
	path := providerMapSettings(t, "providers: {}\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	update := ProviderMapUpdate{
		Provider:            "oidc",
		Definition:          &ProviderDefinition{BaseURL: "https://oidc.example", DefaultModel: "model", APIFlavor: "anthropic-messages", Auth: ProviderAuth{Method: "oidc"}},
		OIDCCredentialStore: &OIDCCredentialStore{Home: "/var/lib/mecatl/provider-oidc", Key: NativeCredentialKey{Source: "keyring"}},
	}
	if _, err := UpdateProviderMap(t.Context(), path, update); err == nil {
		t.Fatal("invalid OIDC update succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid OIDC update changed settings: %v\n%s", err, after)
	}
}

func TestUpdateProviderMap_ChangedTargetAbortsWithoutLockArtifact(t *testing.T) {
	path := providerMapSettings(t, "providers: {}\n")
	providerMapUpdateTestHook = func(stage string) error {
		if stage == "before-compare" {
			return os.WriteFile(path, []byte("providers: {}\n# changed\n"), 0o600)
		}
		return nil
	}
	t.Cleanup(func() { providerMapUpdateTestHook = nil })

	_, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}})
	if err == nil || err.Error() != "Configuration changed while this command was running; no changes were made. Review the file and retry." {
		t.Fatalf("error = %v, want changed-target error", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".lock") {
			t.Fatalf("unexpected lock artifact %q", entry.Name())
		}
	}
}

func TestUpdateDefaults_ChangedTargetAborts(t *testing.T) {
	path := providerMapSettings(t, "models:\n  default_provider: openai\n  default: gpt-5\n")
	defaultUpdateTestHook = func(stage string) error {
		if stage == "before-compare" {
			return os.WriteFile(path, []byte("models:\n  default_provider: openai\n  default: gpt-5\n# changed\n"), 0o600)
		}
		return nil
	}
	t.Cleanup(func() { defaultUpdateTestHook = nil })

	state, err := UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "openai", Model: "gpt-5.1"})
	if state != authfile.CommitNotApplied || err == nil || err.Error() != "Configuration changed while this command was running; no changes were made. Review the file and retry." {
		t.Fatalf("UpdateDefaults = (%v, %v), want changed-target error", state, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(data), "# changed") {
		t.Fatalf("changed target was overwritten: %v\n%s", readErr, data)
	}
}

func TestProviderUnification_Scenario5_PortableSafeWrite(t *testing.T) {
	definition := &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}
	writers := []struct {
		name   string
		leaf   string
		seed   string
		update func(context.Context, string) (authfile.CommitState, error)
	}{
		{"API-key", "auth.yaml", "", func(ctx context.Context, path string) (authfile.CommitState, error) {
			key := "writer-secret"
			return authfile.UpdateAPIKey(ctx, path, authfile.APIKeyUpdate{Provider: "openai", APIKey: &key})
		}},
		{"default", "settings.yaml", "models: {}\n", func(ctx context.Context, path string) (authfile.CommitState, error) {
			return UpdateDefaults(ctx, path, DefaultUpdate{Provider: "openai", Model: "gpt-5"})
		}},
		{"provider map", "settings.yaml", "providers: {}\n", func(ctx context.Context, path string) (authfile.CommitState, error) {
			return UpdateProviderMap(ctx, path, ProviderMapUpdate{Provider: "custom", Definition: definition})
		}},
	}

	t.Run("new credential file and private configuration targets are owner-private", func(t *testing.T) {
		for _, writer := range writers {
			t.Run(writer.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, writer.leaf)
				if writer.seed != "" {
					if err := os.WriteFile(path, []byte(writer.seed), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if state, err := writer.update(context.Background(), path); err != nil || state != authfile.CommitDurable {
					t.Fatalf("update = (%v, %v), want durable success", state, err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat new file: %v", err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("new file mode = %v, want 0600", info.Mode())
				}
			})
		}
	})

	for _, target := range []struct {
		name string
		make func(t *testing.T, dir, path string)
	}{
		{"symlink", func(t *testing.T, dir, path string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "real.yaml"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real.yaml", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"special file", func(t *testing.T, _ string, path string) {
			t.Helper()
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(target.name+" targets are rejected", func(t *testing.T) {
			for _, writer := range writers {
				t.Run(writer.name, func(t *testing.T) {
					dir := t.TempDir()
					if err := os.Chmod(dir, 0o700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, writer.leaf)
					target.make(t, dir, path)
					if state, err := writer.update(context.Background(), path); err == nil || state != authfile.CommitNotApplied {
						t.Fatalf("unsafe target update = (%v, %v), want not-applied error", state, err)
					}
				})
			}
		})
	}

	t.Run("same-directory temporary is removed when cancellation prevents replacement", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "settings.yaml")
		before := []byte("providers: {}\n")
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		providerMapUpdateTestHook = func(stage string) error {
			if stage != "after-temp-sync" {
				return nil
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".settings-default-") && strings.HasSuffix(entry.Name(), ".tmp") {
					cancel()
					return nil
				}
			}
			return errors.New("same-directory temporary was not observable")
		}
		t.Cleanup(func() { providerMapUpdateTestHook = nil })

		state, err := UpdateProviderMap(ctx, path, ProviderMapUpdate{Provider: "custom", Definition: definition})
		if state != authfile.CommitNotApplied || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled update = (%v, %v), want not-applied cancellation", state, err)
		}
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, before) {
			t.Fatalf("cancelled update changed settings: %v\n%s", readErr, after)
		}
		entries, readDirErr := os.ReadDir(dir)
		if readDirErr != nil {
			t.Fatal(readDirErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".settings-default-") || strings.Contains(entry.Name(), ".lock") {
				t.Fatalf("temporary or lock artifact remains: %q", entry.Name())
			}
		}
	})
}

func providerMapSettings(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
