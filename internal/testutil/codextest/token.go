// Package codextest creates synthetic, non-secret manual-token fixtures.
package codextest

import (
	"encoding/base64"
	"fmt"
	"time"
)

// Token returns a structurally valid synthetic JWT with Codex routing claims.
func Token(expires time.Time, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, expires.Unix(), accountID)
	return header + "." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("signature"))
}
