// Package openaicodex supplies the credential and HTTP request policy for the
// experimental ChatGPT Codex backend. It deliberately does not implement an LLM
// provider: successful Responses encoding and streaming translation remain in
// provider/openai.
package openaicodex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxJWTBytes          = 128 * 1024
	maxJWTHeaderBytes    = 16 * 1024
	maxJWTPayloadBytes   = 64 * 1024
	maxJWTSignatureBytes = 64 * 1024
	maxAccountIDBytes    = 512
	maxJSONDepth         = 32
	authClaim            = "https://api.openai.com/auth"
)

var (
	errInvalidCredential = errors.New("openai-codex: invalid manual access token credential")
	errExpiredCredential = errors.New("openai-codex: manual access token expired; replace it in auth.yaml and restart mecatl")
)

// Credential is an immutable snapshot of a manually supplied ChatGPT access
// token and its unverified routing metadata. JWT payload decoding here is not
// signature verification; the backend remains the authority when the token is
// used.
type Credential struct {
	accessToken string
	accountID   string
	expiresAt   time.Time
	fedRAMP     bool
}

var (
	_ fmt.Formatter  = Credential{}
	_ slog.LogValuer = Credential{}
)

// NewCredential builds and validates a credential snapshot. accountID and
// expiresAt are optional explicit auth.yaml fields. When both JWT and explicit
// expiry exist, the earlier expiry wins. now is the startup validation time.
func NewCredential(accessToken, accountID, expiresAt string, now time.Time) (Credential, error) {
	if accessToken == "" || len(accessToken) > maxJWTBytes {
		return Credential{}, errInvalidCredential
	}
	claims, err := decodeJWTPayload(accessToken)
	if err != nil {
		return Credential{}, errInvalidCredential
	}

	jwtAccount, jwtExpiry, fedRAMP, err := routingClaims(claims)
	if err != nil {
		return Credential{}, errInvalidCredential
	}
	if accountID != "" && !validAccountID(accountID) {
		return Credential{}, errInvalidCredential
	}
	if jwtAccount != "" && accountID != "" && jwtAccount != accountID {
		return Credential{}, errors.New("openai-codex: explicit account ID does not match the manual token")
	}
	resolvedAccount := accountID
	if resolvedAccount == "" {
		resolvedAccount = jwtAccount
	}
	if !validAccountID(resolvedAccount) {
		return Credential{}, errors.New("openai-codex: manual token has no usable account ID")
	}

	var explicitExpiry time.Time
	if expiresAt != "" {
		explicitExpiry, err = time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return Credential{}, errors.New("openai-codex: invalid explicit token expiry")
		}
	}
	resolvedExpiry := earlierNonZero(jwtExpiry, explicitExpiry)
	if resolvedExpiry.IsZero() {
		return Credential{}, errors.New("openai-codex: manual token has no usable expiry")
	}

	credential := Credential{
		accessToken: accessToken,
		accountID:   resolvedAccount,
		expiresAt:   resolvedExpiry,
		fedRAMP:     fedRAMP,
	}
	if err := credential.Validate(now); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

// Validate checks the immutable snapshot at the supplied time. Callers use it
// both at startup and immediately before each network request.
func (c Credential) Validate(now time.Time) error {
	if now.IsZero() || c.accessToken == "" || !validAccountID(c.accountID) || c.expiresAt.IsZero() {
		return errInvalidCredential
	}
	if !now.Before(c.expiresAt) {
		return errExpiredCredential
	}
	return nil
}

// AccessToken returns the snapshot's bearer token for the existing OpenAI
// adapter construction seam. Callers must treat it as secret.
func (c Credential) AccessToken() string { return c.accessToken }

// AccountID returns the backend routing account identifier.
func (c Credential) AccountID() string { return c.accountID }

// ExpiresAt returns the effective (earliest) expiry.
func (c Credential) ExpiresAt() time.Time { return c.expiresAt }

// FedRAMP reports the unverified JWT routing claim.
func (c Credential) FedRAMP() bool { return c.fedRAMP }

// Configured reports whether this value is a populated credential snapshot.
// It does not replace Validate: callers must still check expiry at use time.
func (c Credential) Configured() bool { return c.accessToken != "" }

// Format makes every fmt rendering secret-safe, including when Credential is
// nested inside another formatted struct. Account IDs are routing metadata but
// still identity-shaped, so the representation intentionally exposes neither
// them nor the bearer token.
func (c Credential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "openaicodex.Credential{credential:[REDACTED],configured:")
	_, _ = io.WriteString(state, strconv.FormatBool(c.Configured())+"}")
}

// LogValue gives structured slog handlers the same redacted representation as
// fmt. It returns only non-secret presence metadata; expiry, account ID, and
// token never cross the logging boundary.
func (c Credential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("credential", "[REDACTED]"),
		slog.Bool("configured", c.Configured()),
	)
}

func earlierNonZero(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func validAccountID(value string) bool {
	if value == "" || len(value) > maxAccountIDBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func decodeJWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errInvalidCredential
	}
	header, err := decodeJWTPart(parts[0], maxJWTHeaderBytes)
	if err != nil || len(header) == 0 {
		return nil, errInvalidCredential
	}
	payload, err := decodeJWTPart(parts[1], maxJWTPayloadBytes)
	if err != nil || !utf8.Valid(payload) {
		return nil, errInvalidCredential
	}
	signature, err := decodeJWTPart(parts[2], maxJWTSignatureBytes)
	if err != nil || len(signature) == 0 {
		return nil, errInvalidCredential
	}
	value, err := decodeStrictJSON(payload)
	if err != nil {
		return nil, errInvalidCredential
	}
	claims, ok := value.(map[string]any)
	if !ok {
		return nil, errInvalidCredential
	}
	return claims, nil
}

func decodeJWTPart(encoded string, maxDecodedBytes int) ([]byte, error) {
	if encoded == "" || strings.Contains(encoded, "=") || len(encoded) > base64.RawURLEncoding.EncodedLen(maxDecodedBytes)+1 {
		return nil, errInvalidCredential
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDecodedBytes || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return nil, errInvalidCredential
	}
	return decoded, nil
}

func routingClaims(claims map[string]any) (string, time.Time, bool, error) {
	var expiry time.Time
	if raw, ok := claims["exp"]; ok {
		number, ok := raw.(json.Number)
		if !ok {
			return "", time.Time{}, false, errInvalidCredential
		}
		seconds, err := number.Int64()
		if err != nil || seconds <= 0 {
			return "", time.Time{}, false, errInvalidCredential
		}
		expiry = time.Unix(seconds, 0).UTC()
	}

	var account string
	var fedRAMP bool
	if raw, ok := claims[authClaim]; ok {
		auth, ok := raw.(map[string]any)
		if !ok {
			return "", time.Time{}, false, errInvalidCredential
		}
		if rawAccount, exists := auth["chatgpt_account_id"]; exists {
			account, ok = rawAccount.(string)
			if !ok || !validAccountID(account) {
				return "", time.Time{}, false, errInvalidCredential
			}
		}
		if rawFedRAMP, exists := auth["chatgpt_account_is_fedramp"]; exists {
			fedRAMP, ok = rawFedRAMP.(bool)
			if !ok {
				return "", time.Time{}, false, errInvalidCredential
			}
		}
	}
	return account, expiry, fedRAMP, nil
}

// decodeStrictJSON rejects trailing values and duplicate object keys at every
// nesting level. encoding/json otherwise silently accepts last-key-wins input,
// which is unsuitable for security-sensitive routing metadata.
func decodeStrictJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("JSON nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("non-string object key")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("duplicate object key")
			}
			value, valueErr := decodeJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			object[key] = value
		}
		if end, endErr := decoder.Token(); endErr != nil || end != json.Delim('}') {
			return nil, errors.New("unterminated object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, valueErr := decodeJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		if end, endErr := decoder.Token(); endErr != nil || end != json.Delim(']') {
			return nil, errors.New("unterminated array")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter")
	}
}
