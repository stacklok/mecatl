package toolhivellm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/llm"
	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
)

// TestDirectMode_THVSECv1CredentialsListModels exercises ToolHive's encrypted
// store and OIDC token source with an isolated credential. The access-token
// cache is written in the same LLM scope as thv llm setup; the model endpoint
// is local, and no default config path or OS keyring is consulted.
func TestDirectMode_THVSECv1CredentialsListModels(t *testing.T) {
	const accessToken = "isolated-toolhive-access-token"
	const issuer = "https://issuer.example"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" ||
			r.Header.Get("Authorization") != "Bearer "+accessToken {
			http.Error(w, "unexpected model request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"test-model"}]}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets_encrypted")
	password := []byte("isolated-toolhive-test-pass-1234")
	store, err := pkgsecrets.NewEncryptedManager(secretsPath, password)
	if err != nil {
		t.Fatalf("create isolated secrets store: %v", err)
	}
	key := llm.DeriveSecretKey(server.URL, issuer)
	scoped := pkgsecrets.NewScopedProvider(store, pkgsecrets.ScopeLLM)
	if err := scoped.SetSecret(t.Context(), key+"_AT", accessToken+"|2099-01-01T00:00:00Z"); err != nil {
		t.Fatalf("write isolated credential: %v", err)
	}
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatalf("read isolated secrets file: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("THVSEC\x01")) {
		t.Fatal("isolated secrets file is not ToolHive THVSEC v1")
	}

	// Reopen the encrypted file as mecated does in a separate process.
	store, err = pkgsecrets.NewEncryptedManager(secretsPath, password)
	if err != nil {
		t.Fatalf("reopen THVSEC v1 secrets file: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configBody := "llm:\n  gateway_url: " + server.URL +
		"\n  oidc:\n    issuer: " + issuer + "\n    client_id: test-client\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
		t.Fatalf("write isolated ToolHive config: %v", err)
	}
	llmCfg, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatalf("load isolated ToolHive config: %v", err)
	}
	tokenSource := llm.NewTokenSource(&llmCfg,
		pkgsecrets.NewScopedProvider(store, pkgsecrets.ScopeLLM), false, false, nil)
	token, err := tokenSource.Token(t.Context())
	if err != nil {
		t.Fatalf("load THVSEC v1 credential: %v", err)
	}
	if token != accessToken {
		t.Fatal("loaded credential differs from isolated ToolHive credential")
	}
	models, err := openaicompat.NewLister(server.URL+"/v1", token, server.Client()).ListModels(t.Context())
	if err != nil {
		t.Fatalf("list direct-mode models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "test-model" {
		t.Fatalf("direct-mode models = %v, want test-model", models)
	}
}

func TestInvariant_toolhive_interactive_login_never_prints_access_token(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  gateway_url: https://gateway.example/v1\n  oidc:\n    issuer: https://issuer.example\n    client_id: client\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalFactory := interactiveTokenSourceFactory
	t.Cleanup(func() { interactiveTokenSourceFactory = originalFactory })
	const canary = "toolhive-access-token-canary"
	interactiveTokenSourceFactory = func(llm.Config, string, bool, bool, port.Diagnostics) (tokenSource, error) {
		return staticTokenSource(canary), nil
	}

	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = RunInteractiveLogin(t.Context(), path, false, nil)
	_ = write.Close()
	os.Stdout = oldStdout
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil {
		t.Fatalf("RunInteractiveLogin = %v, read stdout = %v", err, readErr)
	}
	if strings.Contains(string(output), canary) || len(output) != 0 {
		t.Fatalf("RunInteractiveLogin disclosed token on stdout: %q", output)
	}
}

type staticTokenSource string

func (s staticTokenSource) Token(context.Context) (string, error) { return string(s), nil }

// TestTokenRequiredHint (F5 AC #7): when the raw error is llm.ErrTokenRequired,
// sanitizeTokenError returns ErrTokenRequiredHint, which names BOTH remediations
// (thv llm setup AND --toolhive-llm-mode proxy) so a headless operator sees the
// exact next step.
func TestTokenRequiredHint(t *testing.T) {
	err := sanitizeTokenError(llm.ErrTokenRequired)
	if err == nil {
		t.Fatal("expected an error for ErrTokenRequired, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"thv llm setup",
		"--toolhive-llm-mode proxy",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "mecatui llm") {
		t.Errorf("error message names removed command: %s", msg)
	}
}

// TestSanitizeTokenError_StripsBearer (F8 AC #3): the sanitize path must not
// leak bearer material. llm.SanitizeTokenError strips *oauth2.RetrieveError
// bodies (where an IdP may echo back bearer material); we construct one carrying
// a secret in its Body field and assert the output contains neither the secret
// nor the raw body content.
func TestSanitizeTokenError_StripsBearer(t *testing.T) {
	secret := "secret123"
	// Simulate an *oauth2.RetrieveError whose Body echoes back bearer material
	// (the exact case SanitizeTokenError guards against).
	raw := &oauth2.RetrieveError{
		ErrorCode:        "invalid_grant",
		ErrorDescription: "token expired",
		Body:             []byte(`{"error":"invalid_grant","error_description":"token expired","access_token":"` + secret + `"}`),
	}
	err := sanitizeTokenError(raw)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Errorf("sanitize leaked token material: %q", msg)
	}
	// The error code/description should survive; the raw body should not.
	if !strings.Contains(msg, "invalid_grant") {
		t.Errorf("error message missing error code: %s", msg)
	}
}

// TestLoadLLMConfig_MissingFileNeverCreates pins that the config read REFUSES a
// missing file instead of creating it. config.LoadOrCreateConfigWithPath writes a
// default config.yaml when the path does not exist, so without the stat guard
// OIDCConfigured — a predicate reached from Build's validateToolhiveLLMMode —
// would create the operator's ToolHive config as a side effect of asking whether
// it exists. The assertion is the ABSENCE of the file after the call.
func TestLoadLLMConfig_MissingFileNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if _, err := loadLLMConfig(path); err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("loadLLMConfig CREATED %s (stat err = %v); it must never write another tool's config", path, err)
	}

	// The same guard through the public predicate: false, and still no file.
	if OIDCConfigured(path) {
		t.Error("OIDCConfigured = true for a missing config, want false (fail closed)")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OIDCConfigured CREATED %s (stat err = %v)", path, err)
	}
}

// TestSanitizeTokenError_PreservesErrorChain is the anti-flattening pin:
// llmresilience classifies retry-vs-cancel with errors.Is(err, context.Canceled)
// / context.DeadlineExceeded, and the token-source error is what reaches it
// through bearerRoundTripper.RoundTrip. If sanitizeTokenError ever goes back to
// returning errors.New for these, a ctrl-C during an IdP refresh is reported as
// a failed run instead of a cancelled one and becomes eligible for a retry — so
// the chain must survive. ErrTokenRequired is checked for the same reason.
func TestSanitizeTokenError_PreservesErrorChain(t *testing.T) {
	for name, sentinel := range map[string]error{
		"canceled":         context.Canceled,
		"deadlineExceeded": context.DeadlineExceeded,
		"tokenRequired":    llm.ErrTokenRequired,
	} {
		t.Run(name, func(t *testing.T) {
			// Wrapped one level deep, as the oauth2/HTTP layers would deliver it.
			got := sanitizeTokenError(fmt.Errorf("refresh: %w", sentinel))
			if !errors.Is(got, sentinel) {
				t.Errorf("errors.Is(%v, %v) = false, want true (error chain flattened)", got, sentinel)
			}
		})
	}
}

// TestSanitizeTokenError_GenericError: a non-ErrTokenRequired, non-OAuth2 error
// is sanitised through llm.SanitizeTokenError (the error string is preserved
// but any oauth2.RetrieveError body is stripped).
func TestSanitizeTokenError_GenericError(t *testing.T) {
	raw := errors.New("network timeout")
	err := sanitizeTokenError(raw)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "network timeout") {
		t.Errorf("error message should preserve the original: %s", err.Error())
	}
}
