package mcpbroker

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
)

// errNotIJSON is the refusal of arguments that have no argument hash. Its
// text is fixed, so it never quotes the arguments.
var errNotIJSON = errors.New("mcpbroker: arguments are not I-JSON")

// jsonWhitespace is JSON's insignificant whitespace (RFC 8259 section 2).
const jsonWhitespace = " \t\n\r"

// argumentsSHA256 returns the arguments to send and their argument hash, as
// docs/drafts/agent-identity/credentials-and-lifecycle.md step (8), "The
// argument hash", defines it. sent is {} when args is absent (empty, or JSON
// whitespace only) or JSON null, and args unchanged otherwise. The hash is
// the vMCP's own, callbinding.ArgumentsSHA256 over sent, so the two sides
// agree by construction; arguments it refuses (anything that is not exactly
// one I-JSON object) fail with errNotIJSON, before anything is authorized.
func argumentsSHA256(args json.RawMessage) (sent json.RawMessage, sum string, err error) {
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
	return sent, sum, nil
}
