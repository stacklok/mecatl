package toolhivellm

import (
	"errors"
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
