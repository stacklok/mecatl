package toolhivellm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/llm"
	"golang.org/x/oauth2"
)

// TestTokenRequiredHint (F5 AC #7): when the raw error is llm.ErrTokenRequired,
// sanitizeTokenError returns ErrTokenRequiredHint, which names BOTH remediations
// (thv llm setup / mecatui login AND --toolhive-llm-mode proxy) so a headless
// operator sees the exact next step.
func TestTokenRequiredHint(t *testing.T) {
	err := sanitizeTokenError(llm.ErrTokenRequired)
	if err == nil {
		t.Fatal("expected an error for ErrTokenRequired, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"thv llm setup",
		"mecatui login",
		"--toolhive-llm-mode proxy",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
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
