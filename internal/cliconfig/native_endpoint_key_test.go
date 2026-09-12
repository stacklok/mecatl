package cliconfig

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	keyringapi "github.com/zalando/go-keyring"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const nativeKeyEnv = "MECATL_NATIVE_LLM_CREDENTIAL_KEY"

func TestNativeEnvironmentKeyDecoding(t *testing.T) {
	raw := bytes.Repeat([]byte{0x71}, 32)
	encoded := base64.StdEncoding.EncodeToString(raw)
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"valid", encoded, true},
		{"missing", "", false},
		{"raw", string(raw), false},
		{"unpadded", strings.TrimRight(encoded, "="), false},
		{"newline", encoded + "\n", false},
		{"whitespace", " " + encoded, false},
		{"short", base64.StdEncoding.EncodeToString(raw[:31]), false},
		{"long", base64.StdEncoding.EncodeToString(append(raw, 0)), false},
		{"noncanonical bits", encoded[:42] + "F=", false},
		{"malformed", "key-value-canary!", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(nativeKeyEnv, tc.value)
			key, err := nativeEnvironmentKey(nativeKeyEnv).Key(t.Context(), true)
			defer clear(key)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && !bytes.Equal(key, raw) {
				t.Fatal("decoded key mismatch")
			}
			if err != nil && tc.value != "" && strings.Contains(err.Error(), tc.value) {
				t.Fatal("error disclosed key material")
			}
		})
	}
}

func nativeKeyRuntime(t *testing.T, selection permconfig.NativeCredentialKey) *NativeEndpointRuntime {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := OpenNativeEndpointRuntime(permconfig.ProviderDefinition{
		ID: "corp", BaseURL: "https://gateway.example/v1", DefaultModel: "model", APIFlavor: "openai-responses",
		Native: &permconfig.NativeEndpointIdentity{
			CredentialHome: root, CredentialKey: selection,
			OIDC:        permconfig.NativeOIDC{Issuer: "https://issuer.example", ClientID: "mecatl", Scopes: []string{"openid", "offline_access"}},
			IssuerTrust: permconfig.NativeTrust{Policy: "public"}, GatewayTrust: permconfig.NativeTrust{Policy: "public"},
		},
	}, func(context.Context, string) (oauthlogin.Result, error) {
		t.Error("unexpected authorization presenter")
		return oauthlogin.Result{}, errors.New("unexpected authorization")
	})
	if err != nil {
		t.Fatal(err)
	}
	r.issuer = &http.Client{Transport: nativeNoNetwork{t: t}}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

type nativeNoNetwork struct{ t *testing.T }

func (n nativeNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("unexpected issuer network request")
	return nil, errors.New("unexpected issuer request")
}

func TestNativeEnvironmentKeyLifecycle(t *testing.T) {
	keyringapi.MockInitWithError(errors.New("keyring-must-not-be-touched-canary"))
	t.Cleanup(keyringapi.MockInit)
	raw := bytes.Repeat([]byte{0x71}, 32)
	encoded := base64.StdEncoding.EncodeToString(raw)
	t.Setenv(nativeKeyEnv, encoded)
	r := nativeKeyRuntime(t, permconfig.NativeCredentialKey{Source: "environment", KeyEnv: nativeKeyEnv})
	root := r.definition.Native.CredentialHome
	toolhiveHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", toolhiveHome)
	toolhivePath := filepath.Join(toolhiveHome, "auth.yaml")
	toolhiveCanary := []byte("toolhive-authority-canary")
	if err := os.WriteFile(toolhivePath, toolhiveCanary, 0o600); err != nil {
		t.Fatal(err)
	}

	// Passive operations cannot initialize an empty namespace or keyring.
	if got := r.Status(t.Context()); got != llmendpoint.StatusStorageUnavailable {
		t.Fatalf("empty namespace status = %s", got)
	}
	if err := r.Logout(t.Context()); !errors.Is(err, credentialstore.ErrUnavailable) {
		t.Fatalf("empty namespace logout = %v", err)
	}
	if _, err := r.Source(t.Context()); err == nil {
		t.Fatal("serving accepted an absent record")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("passive operation initialized credential storage")
	}

	repo, store, err := r.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	old := llmendpoint.Token{AccessToken: "native-access-canary", RefreshToken: "native-refresh-canary", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}
	rotated := llmendpoint.Token{AccessToken: "rotated-access-canary", RefreshToken: "rotated-refresh-canary", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).In(time.FixedZone("test", 0))}
	lifecycle := r.lifecycle(repo)
	lifecycle.Authorize = func(context.Context) (llmendpoint.Token, error) { return old, nil }
	lifecycle.Exchange = func(_ context.Context, refresh string) (llmendpoint.Token, error) {
		if refresh != old.RefreshToken {
			t.Fatal("incorrect refresh material")
		}
		return rotated, nil
	}
	lifecycle.ValidateAccessToken = nil
	if err := lifecycle.Enroll(t.Context(), r.identity); err != nil {
		t.Fatal(err)
	}
	committed := false
	lifecycle.AfterCommit = func() {
		loaded, err := repo.Load(t.Context(), r.identity)
		if err != nil {
			t.Fatalf("load rotated credential: %v", err)
		}
		// JSON preserves the instant, not time.Time's location or monotonic metadata.
		if loaded.Token.AccessToken != rotated.AccessToken || loaded.Token.RefreshToken != rotated.RefreshToken || loaded.Token.TokenType != rotated.TokenType || !loaded.Token.Expiry.Equal(rotated.Expiry) {
			t.Fatal("refresh returned before durable rotation")
		}
		committed = true
	}
	if got, err := lifecycle.Refresh(t.Context(), r.identity); err != nil {
		t.Fatal(err)
	} else if !committed || got.AccessToken != rotated.AccessToken {
		t.Fatal("refresh did not return the durably committed bearer")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh serving runtime reopens the actual encrypted record, not an in-memory cache.
	reopened, err := OpenNativeEndpointRuntime(r.definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	source, err := reopened.Source(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token, err := source.Token(t.Context()); err != nil || token != rotated.AccessToken {
		t.Fatal("serving did not recover persisted refreshed token")
	}
	if got := reopened.Status(t.Context()); got != llmendpoint.StatusUsable {
		t.Fatalf("status = %s", got)
	}

	canaries := []string{string(raw), encoded, old.AccessToken, old.RefreshToken, rotated.AccessToken, rotated.RefreshToken}
	before := nativeCredentialFiles(t, root, canaries)
	for _, value := range []string{"", "malformed-key-canary", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))} {
		t.Setenv(nativeKeyEnv, value)
		for _, operation := range []func() error{
			func() error { return r.Login(t.Context()) },
			func() error { return r.Logout(t.Context()) },
			func() error { _, err := r.Source(t.Context()); return err },
		} {
			err := operation()
			if err == nil {
				t.Fatal("invalid key accepted")
			}
			for _, secret := range append(canaries, "malformed-key-canary") {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("error disclosed credential material")
				}
			}
		}
		if got := r.Status(t.Context()); got != llmendpoint.StatusCorrupt && got != llmendpoint.StatusStorageUnavailable {
			t.Fatalf("invalid key status = %s", got)
		}
		after := nativeCredentialFiles(t, root, canaries)
		if len(before) != len(after) {
			t.Fatal("invalid key changed stored files")
		}
		for name, data := range before {
			if !bytes.Equal(data, after[name]) {
				t.Fatal("invalid key modified existing storage")
			}
		}
	}
	t.Setenv(nativeKeyEnv, encoded)
	// Changing the source does not change the credential identity or trigger migration.
	r.definition.Native.CredentialKey = permconfig.NativeCredentialKey{Source: "keyring"}
	keyringRuntime, err := OpenNativeEndpointRuntime(r.definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = keyringRuntime.Close() }()
	originalKey, _ := llmendpoint.CredentialRecordKey(r.identity)
	changedKey, _ := llmendpoint.CredentialRecordKey(keyringRuntime.identity)
	if !bytes.Equal(originalKey, changedKey) {
		t.Fatal("key source entered credential identity")
	}
	if _, err := keyringRuntime.Source(t.Context()); err == nil {
		t.Fatal("broken keyring fell back to environment")
	}
	r.definition.Native.CredentialKey = permconfig.NativeCredentialKey{Source: "environment", KeyEnv: nativeKeyEnv}
	repo, store, err = r.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := r.lifecycle(repo).Logout(t.Context(), r.identity, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.Status(t.Context()); got != llmendpoint.StatusNotEnrolled {
		t.Fatalf("logout status = %s", got)
	}
	if data, err := os.ReadFile(toolhivePath); err != nil || !bytes.Equal(data, toolhiveCanary) {
		t.Fatal("native lifecycle modified ToolHive state")
	}
}

func nativeCredentialFiles(t *testing.T, root string, canaries []string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.Contains(entry.Name(), "keyring") {
			t.Fatal("environment source touched keyring lock")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatal("credential file is not owner-only")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, canary := range canaries {
			if bytes.Contains(data, []byte(canary)) {
				t.Fatal("secret persisted outside encryption")
			}
		}
		files[path] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNativeDefaultKeyringCompatibility(t *testing.T) {
	keyringapi.MockInit()
	t.Cleanup(keyringapi.MockInit)
	t.Setenv(nativeKeyEnv, "ignored-malformed-env-key-canary")
	r := nativeKeyRuntime(t, permconfig.NativeCredentialKey{})
	keyring, err := oidcclient.NewKeyring(r.definition.Native.CredentialHome)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Status(t.Context()); got != llmendpoint.StatusNotEnrolled {
		t.Fatalf("status = %s", got)
	}
	if err := r.Logout(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Source(t.Context()); err == nil {
		t.Fatal("serving created key material")
	}
	if _, err := keyring.Key(t.Context(), false); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatal("passive operations created a keyring entry")
	}
	repo, store, err := r.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	token := llmendpoint.Token{AccessToken: "keyring-access", RefreshToken: "keyring-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).In(time.FixedZone("test", 0))}
	if _, err := repo.Save(t.Context(), r.identity, token, nil); err != nil {
		t.Fatal(err)
	}
	key, err := keyring.Key(t.Context(), false)
	if err != nil || len(key) != 32 {
		t.Fatal("default did not create the legacy root-bound key")
	}
	defer clear(key)
	legacy, err := credentialstore.OpenExistingEncryptedFile(r.definition.Native.CredentialHome, llmendpoint.CredentialNamespace, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	rec, err := llmendpoint.NewCredentialRepository(legacy).Load(t.Context(), r.identity)
	if err != nil {
		t.Fatalf("load credential with existing keyring encryption: %v", err)
	}
	if rec.Token.AccessToken != token.AccessToken || rec.Token.RefreshToken != token.RefreshToken || rec.Token.TokenType != token.TokenType || !rec.Token.Expiry.Equal(token.Expiry) {
		t.Fatal("default record is incompatible with existing keyring encryption")
	}
}
