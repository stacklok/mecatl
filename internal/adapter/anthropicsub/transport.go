package anthropicsub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// billingHeaderPrefix opens the first system block. The provider reads the
// client version and entrypoint from it and attests the body through the cch
// field that follows.
const billingHeaderPrefix = "x-anthropic-billing-header:"

// billingFingerprintSalt seeds the client-version suffix. Both it and the
// character positions below are copied from the first-party client's
// fingerprint routine.
const billingFingerprintSalt = "59cf53e54c78"

// billingSeedPositions are the first user message offsets sampled into the
// fingerprint. A missing position contributes a literal zero.
var billingSeedPositions = []int{4, 7, 20}

// CredentialSource yields a currently-valid subscription access token.
type CredentialSource interface {
	AccessToken(context.Context) (string, error)
	AccountID(context.Context) (string, error)
}

// Transport authenticates and shapes Claude subscription requests.
//
// It owns the complete header set and rewrites the request body, so it is the
// single place the first-party fingerprint is reproduced. An API-key request
// never reaches it.
type Transport struct {
	Source CredentialSource
	Base   http.RoundTripper
	// SessionID, when set, is reported as the client session. Empty derives a
	// per-request value.
	SessionID string
}

var errNoTransportSource = errors.New("anthropic: subscription transport requires a credential source")

// RoundTrip authenticates the request, reshapes the body, and stamps the
// attestation. The caller's request is never mutated.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.Source == nil {
		return nil, errNoTransportSource
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	token, err := t.Source.AccessToken(req.Context())
	if err != nil {
		return nil, err
	}
	accountID, err := t.Source.AccountID(req.Context())
	if err != nil {
		return nil, err
	}

	clone := req.Clone(req.Context())

	// Subscription traffic uses the beta resource; the API-key path does not.
	query := clone.URL.Query()
	query.Set("beta", "true")
	clone.URL.RawQuery = query.Encode()

	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	shaped, agentRequest, thinkingRequest, err := shapeBody(body, accountID, t.sessionID(accountID))
	if err != nil {
		return nil, err
	}
	patchCch(shaped)
	setBody(clone, shaped)

	// The header set is rebuilt from owned constants rather than filtered:
	// copying a caller header could let a configured custom header displace
	// part of the fingerprint.
	clean := make(http.Header, len(stainlessHeaders)+12)
	clean.Set("Accept", "application/json")
	clean.Set("Content-Type", "application/json")
	clean.Set("User-Agent", userAgent)
	for key, value := range stainlessHeaders {
		clean.Set(key, value)
	}
	if beta := betaHeader(agentRequest, thinkingRequest); beta != "" {
		clean.Set("anthropic-beta", beta)
	}
	clean.Set("anthropic-dangerous-direct-browser-access", "true")
	clean.Set("anthropic-version", apiVersion)
	clean.Set("Authorization", "Bearer "+token)
	clean.Set("x-app", "cli")
	clean.Set("x-client-request-id", newRequestID())
	clean.Set("Connection", "keep-alive")
	if t.SessionID != "" {
		clean.Set("X-Claude-Code-Session-Id", t.SessionID)
	}
	clone.Header = clean

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func (t *Transport) sessionID(accountID string) string {
	if t.SessionID != "" {
		return t.SessionID
	}
	return deriveSessionID(accountID)
}

func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer func() { _ = req.Body.Close() }()
	return io.ReadAll(req.Body)
}

func setBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

// bodyKeyOrder fixes the serialized field order. Order is load-bearing twice
// over: the attestation anchor expects the system block to open a known byte
// sequence, and a stable prefix is what lets the provider's prompt cache hit.
var bodyKeyOrder = []string{
	"model", "messages", "system", "tools", "metadata", "max_tokens",
	"thinking", "context_management", "output_config", "stream",
}

// shapeBody applies every subscription-only body rule and reports whether the
// request carries tools or thinking, which selects the beta list.
func shapeBody(body []byte, accountID, sessionID string) (shaped []byte, agentRequest, thinkingRequest bool, err error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return body, false, false, nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, false, false, fmt.Errorf("anthropic: subscription request body is not an object: %w", err)
	}

	agentRequest = hasTools(fields["tools"])
	thinkingRequest = len(fields["thinking"]) > 0 && string(fields["thinking"]) != "null"
	agentRequest = agentRequest || thinkingRequest

	if err := clampMaxTokens(fields); err != nil {
		return nil, false, false, err
	}
	if err := prefixToolNames(fields); err != nil {
		return nil, false, false, err
	}
	if err := injectSystemBlocks(fields); err != nil {
		return nil, false, false, err
	}
	if err := setMetadata(fields, accountID, sessionID); err != nil {
		return nil, false, false, err
	}

	encoded, err := encodeOrdered(fields)
	if err != nil {
		return nil, false, false, err
	}
	return encoded, agentRequest, thinkingRequest, nil
}

func hasTools(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var tools []json.RawMessage
	return json.Unmarshal(raw, &tools) == nil && len(tools) > 0
}

// clampMaxTokens holds the request to the client's ceiling. The provider
// refuses a larger value on a subscription grant.
func clampMaxTokens(fields map[string]json.RawMessage) error {
	limit := int64(MaxOutputTokens)
	if raw, ok := fields["max_tokens"]; ok && len(raw) > 0 {
		var requested int64
		if err := json.Unmarshal(raw, &requested); err != nil {
			return errors.New("anthropic: subscription request has a non-numeric max_tokens")
		}
		if requested > 0 && requested < limit {
			limit = requested
		}
	}
	encoded, err := json.Marshal(limit)
	if err != nil {
		return err
	}
	fields["max_tokens"] = encoded
	return nil
}

// prefixToolNames namespaces caller tools away from the client's built-ins. A
// tools array is always present, even when empty, because the client always
// sends one.
func prefixToolNames(fields map[string]json.RawMessage) error {
	raw, ok := fields["tools"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		fields["tools"] = json.RawMessage(`[]`)
		return nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return errors.New("anthropic: subscription request has a malformed tools array")
	}
	for _, tool := range tools {
		nameRaw, ok := tool["name"]
		if !ok {
			continue
		}
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			continue
		}
		if _, builtin := builtinToolNames[name]; builtin || strings.HasPrefix(name, toolPrefix) {
			continue
		}
		encoded, err := json.Marshal(toolPrefix + name)
		if err != nil {
			return err
		}
		tool["name"] = encoded
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	fields["tools"] = encoded
	return nil
}

type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// injectSystemBlocks prepends the billing and identity blocks. A body that
// already carries the billing block is left alone so a resumed or retried
// request cannot accumulate duplicates.
func injectSystemBlocks(fields map[string]json.RawMessage) error {
	existing, err := decodeSystemBlocks(fields["system"])
	if err != nil {
		return err
	}
	for _, block := range existing {
		if strings.HasPrefix(block.Text, billingHeaderPrefix) {
			return nil
		}
	}

	blocks := make([]systemBlock, 0, len(existing)+2)
	blocks = append(blocks,
		systemBlock{Type: "text", Text: billingHeader(firstUserMessageText(fields["messages"]))},
		systemBlock{Type: "text", Text: identityInstruction},
	)
	blocks = append(blocks, existing...)

	encoded, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	fields["system"] = encoded
	return nil
}

// decodeSystemBlocks accepts both shapes the field takes: a bare string or an
// array of typed blocks.
func decodeSystemBlocks(raw json.RawMessage) ([]systemBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString == "" {
			return nil, nil
		}
		return []systemBlock{{Type: "text", Text: asString}}, nil
	}
	var blocks []systemBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("anthropic: subscription request has a malformed system field")
	}
	return blocks, nil
}

// billingHeader builds the first system block, including the version suffix
// the client derives from the conversation's first user message.
func billingHeader(firstUserMessage string) string {
	seed := make([]byte, 0, len(billingSeedPositions))
	runes := []rune(firstUserMessage)
	for _, position := range billingSeedPositions {
		if position < len(runes) {
			seed = append(seed, []byte(string(runes[position]))...)
		} else {
			seed = append(seed, '0')
		}
	}
	sum := sha256.Sum256([]byte(billingFingerprintSalt + string(seed) + ClientVersion))
	suffix := hex.EncodeToString(sum[:])[:3]
	return billingHeaderPrefix + " cc_version=" + ClientVersion + "." + suffix +
		"; cc_entrypoint=claude-desktop; " + cchPlaceholder + ";"
}

// firstUserMessageText returns the text of the first user message, accepting
// both a bare string content and a content-block array.
func firstUserMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return ""
	}
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		var asString string
		if err := json.Unmarshal(message.Content, &asString); err == nil {
			return asString
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(message.Content, &blocks); err == nil {
			for _, block := range blocks {
				if block.Type == "text" {
					return block.Text
				}
			}
		}
		return ""
	}
	return ""
}

// setMetadata stamps the client-shaped user id. A caller-supplied value is
// preserved only when it already has that shape, so a plain opaque id cannot
// leak into a field the provider parses.
func setMetadata(fields map[string]json.RawMessage, accountID, sessionID string) error {
	metadata := map[string]json.RawMessage{}
	if raw, ok := fields["metadata"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return errors.New("anthropic: subscription request has a malformed metadata object")
		}
	}
	if raw, ok := metadata["user_id"]; ok {
		var existing string
		if err := json.Unmarshal(raw, &existing); err == nil && strings.HasPrefix(existing, "{") {
			return nil
		}
	}

	userID := map[string]string{
		"device_id":  deriveDeviceID(accountID),
		"session_id": sessionID,
	}
	if accountID != "" {
		userID["account_uuid"] = accountID
	}
	// The provider expects a JSON document rendered into the string field.
	encodedUserID, err := json.Marshal(userID)
	if err != nil {
		return err
	}
	encodedString, err := json.Marshal(string(encodedUserID))
	if err != nil {
		return err
	}
	metadata["user_id"] = encodedString

	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	fields["metadata"] = encoded
	return nil
}

// encodeOrdered serializes with bodyKeyOrder first, then any remaining keys in
// a stable order, so the attestation anchor and the cache prefix are
// deterministic.
func encodeOrdered(fields map[string]json.RawMessage) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	written := 0
	emit := func(key string, value json.RawMessage) error {
		if written > 0 {
			out.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(value)
		written++
		return nil
	}

	emitted := make(map[string]struct{}, len(fields))
	for _, key := range bodyKeyOrder {
		value, ok := fields[key]
		if !ok || len(value) == 0 {
			continue
		}
		if err := emit(key, value); err != nil {
			return nil, err
		}
		emitted[key] = struct{}{}
	}
	remaining := make([]string, 0, len(fields))
	for key := range fields {
		if _, done := emitted[key]; !done && len(fields[key]) > 0 {
			remaining = append(remaining, key)
		}
	}
	sortStrings(remaining)
	for _, key := range remaining {
		if err := emit(key, fields[key]); err != nil {
			return nil, err
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
