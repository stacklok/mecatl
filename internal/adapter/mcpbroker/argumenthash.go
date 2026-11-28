package mcpbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
)

// errNotIJSON is the refusal of arguments that have no argument hash. Its
// text is fixed, so it never quotes the arguments.
var errNotIJSON = errors.New("mcpbroker: arguments are not I-JSON")

// errCaseFoldedMembers is the refusal of arguments with an object whose
// member names collide under case folding, which the vMCP refuses with
// -32600 before it computes the hash. Its text is fixed, so it never quotes
// the arguments.
var errCaseFoldedMembers = errors.New("mcpbroker: arguments have member names that differ only by case")

// errArgumentsTooLarge is the refusal of arguments longer than
// MaxGatewayArgumentBytes, which the vMCP would refuse with 413 before
// reading them. Its text is fixed, so it never quotes the arguments.
var errArgumentsTooLarge = errors.New("mcpbroker: arguments are too large for the gateway")

// vmcpMaxRequestBodyBytes is the largest request body the embedded vMCP
// reads. Two limits apply, and each answers a longer body with 413 before it
// is parsed: ToolHive's own, bodylimit.DefaultMaxRequestBodySize, outermost;
// and the MCP SDK's, mcpsdk.DefaultMaxRequestBodyBytes, in the SDK's
// streamable HTTP handler that serves the vMCP's MCP endpoint, which leaves
// it at its default. The SDK linked into the vMCP is the one this binary
// links, so both constants are the vMCP's own.
const vmcpMaxRequestBodyBytes = min(bodylimit.DefaultMaxRequestBodySize, mcpsdk.DefaultMaxRequestBodyBytes)

// MaxGatewayArgumentBytes bounds the arguments of a gateway call: the vMCP's
// request body limit less gatewayEnvelopeAllowance for the tools/call's
// JSON-RPC envelope around them (its method, ID, tool name, and _meta). The
// MCP client never makes the arguments longer (it compacts them and does not
// escape HTML), so arguments within the bound fit with a tool name of up to
// tens of KiB. A body that still exceeds the limit gets 413, which the MCP
// client reports as refused.
const MaxGatewayArgumentBytes = vmcpMaxRequestBodyBytes - gatewayEnvelopeAllowance

// gatewayEnvelopeAllowance is the part of the vMCP's request body limit kept
// for the tools/call's envelope around the arguments.
const gatewayEnvelopeAllowance = 64 << 10

// jsonWhitespace is JSON's insignificant whitespace (RFC 8259 section 2).
const jsonWhitespace = " \t\n\r"

// argumentsSHA256 returns the arguments to send and their argument hash, as
// docs/drafts/agent-identity/credentials-and-lifecycle.md step (8), "The
// argument hash", defines it. sent is {} when args is absent (empty, or JSON
// whitespace only) or JSON null, and args unchanged otherwise. The hash is
// the vMCP's own, callbinding.ArgumentsSHA256 over sent, so the two sides
// agree by construction; arguments it refuses (anything that is not exactly
// one I-JSON object) fail with errNotIJSON, before anything is authorized.
// Arguments the vMCP's request parser refuses although they have a hash, an
// object with member names equal under case folding, fail with
// errCaseFoldedMembers, and arguments longer than MaxGatewayArgumentBytes,
// which the vMCP refuses before reading them, fail with errArgumentsTooLarge,
// also before anything is authorized.
func argumentsSHA256(args json.RawMessage) (sent json.RawMessage, sum string, err error) {
	if int64(len(args)) > MaxGatewayArgumentBytes {
		return nil, "", errArgumentsTooLarge
	}
	sent = args
	// Only JSON's own whitespace is insignificant; bytes.TrimSpace would
	// also drop Unicode spaces such as U+00A0, which the vMCP refuses.
	if trimmed := bytes.Trim(args, jsonWhitespace); len(trimmed) == 0 || string(trimmed) == "null" {
		sent = json.RawMessage(`{}`)
	}
	sum, err = callbinding.ArgumentsSHA256(sent)
	if err != nil {
		return nil, "", errNotIJSON
	}
	if hasCaseFoldedMembers(sent) {
		return nil, "", errCaseFoldedMembers
	}
	return sent, sum, nil
}

// hasCaseFoldedMembers reports whether an object anywhere in args, valid
// JSON, has two member names equal under Unicode simple case folding, the
// equivalence strings.EqualFold uses. It is the rule of ToolHive's vMCP
// request parser (pkg/mcp, hasAmbiguousJSONMembers), which refuses such a
// request with -32600 Invalid Request: names are compared within one object,
// at any depth, after folding each rune to the smallest rune in its
// unicode.SimpleFold orbit.
func hasCaseFoldedMembers(args json.RawMessage) bool {
	type container struct {
		seen         map[string]struct{} // nil for an array
		expectingKey bool
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var stack []container
	for {
		token, err := dec.Token()
		if err != nil {
			return false
		}
		if n := len(stack); n > 0 && stack[n-1].seen != nil {
			top := &stack[n-1]
			if top.expectingKey {
				if key, ok := token.(string); ok {
					folded := foldMemberName(key)
					if _, dup := top.seen[folded]; dup {
						return true
					}
					top.seen[folded] = struct{}{}
					top.expectingKey = false
					continue
				}
			} else {
				top.expectingKey = true
			}
		}
		switch token {
		case json.Delim('{'):
			stack = append(stack, container{seen: map[string]struct{}{}, expectingKey: true})
		case json.Delim('['):
			stack = append(stack, container{})
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
	}
}

// foldMemberName returns one representative of name's class under Unicode
// simple case folding: each rune folded to the smallest rune in its
// unicode.SimpleFold orbit, as ToolHive's vMCP folds member names.
func foldMemberName(name string) string {
	var folded strings.Builder
	folded.Grow(len(name))
	for _, r := range name {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			canonical = min(canonical, next)
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
