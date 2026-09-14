//go:build linux || darwin

package privatefile_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/privatefile"
)

func TestProviderSetupFollowup_Scenario4_ClientPreservation(t *testing.T) {
	for _, client := range []string{"credentials", "settings"} {
		t.Run(client, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "document.yaml")
			var body, preserved string
			var update func(context.Context, string) (privatefile.CommitState, error)
			if client == "credentials" {
				preserved = "  openai-codex: # manual credential\n    oauth:\n      access_token: token-sentinel\n      account_id: account-sentinel\n      expires_at: '2099-01-01T00:00:00Z'\n"
				body = "# private credentials\nproviders:\n  openai:\n    api_key: old\n" + preserved
				update = func(ctx context.Context, value string) (privatefile.CommitState, error) {
					return authfile.UpdateAPIKey(ctx, path, authfile.APIKeyUpdate{Provider: "openai", APIKey: &value})
				}
			} else {
				preserved = "permissions: # unrelated policy\n  allow:\n    - Read\n"
				body = "# operator settings\n" + preserved + "models:\n  default_provider: openai\n  default: old\n"
				update = func(ctx context.Context, value string) (privatefile.CommitState, error) {
					return permconfig.UpdateDefaults(ctx, path, permconfig.DefaultUpdate{Provider: "openai", Model: value})
				}
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := update(t.Context(), "old")
			if err != nil || state != privatefile.CommitNoop {
				t.Fatalf("noop = %s %v", state, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, []byte(body)) {
				t.Fatal("no-op rewrote accepted bytes")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			state, err = update(ctx, "new")
			if err == nil || state != privatefile.CommitNotApplied {
				t.Fatalf("cancel = %s %v", state, err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(got, []byte(body)) {
				t.Fatal("cancelled update changed bytes")
			}
			state, err = update(t.Context(), "new")
			if err != nil || state != privatefile.CommitDurable {
				t.Fatalf("replacement = %s %v", state, err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !strings.Contains(string(got), preserved) || !strings.HasPrefix(string(got), strings.SplitN(body, "\n", 2)[0]) {
				t.Fatal("replacement lost unrelated records or comments")
			}
			if state, err = update(t.Context(), "new"); err != nil || state != privatefile.CommitNoop {
				t.Fatalf("replacement read-back = %s %v", state, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "document.yaml" {
				t.Fatal("writer left a temporary or sidecar lock")
			}
		})
	}
}
