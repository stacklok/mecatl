package identitybroker

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// CallEntryType is the authorization_details type of a call entry
// (credentials-and-lifecycle.md, step (8)): ToolHive's MCP call type. AS-1
// signs the entry the broker sends into the ID-JAG, AS-2 copies it into the
// gateway token, and the vMCP accepts that token for the one call it names.
const CallEntryType = "https://toolhive.dev/mcp_tool_call"

// callEntry is one call entry on the wire. A tool call has Tool,
// ArgumentsSHA256, and OperationID; a list call has Method. Server is always
// set, and the members of the other shape are omitted.
type callEntry struct {
	Type            string `json:"type"`
	Server          string `json:"server"`
	Tool            string `json:"tool,omitempty"`
	ArgumentsSHA256 string `json:"arguments_sha256,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
	Method          string `json:"method,omitempty"`
}

// callEntryFor returns the call entry of a valid req, as the
// authorization_details array the broker sends AS-1: one tool-call entry for
// a tools/call, naming its server, the tool as sent in params.name, its
// argument hash, and its operation; one list-call entry for a tools/list,
// naming its server and method.
func callEntryFor(req AuthorizeCallRequest) json.RawMessage {
	e := callEntry{Type: CallEntryType, Server: req.Server}
	if req.Method == MethodToolsCall {
		e.Tool, e.ArgumentsSHA256, e.OperationID = req.Tool, req.ArgumentsSHA256, req.OperationID
	} else {
		e.Method = req.Method
	}
	// Marshalling strings cannot fail.
	raw, _ := json.Marshal([]callEntry{e})
	return raw
}

// CallEntryCheck is the result of the broker's output check (step (8a)):
// whether AS-2's token response carried the call entry the broker sent.
type CallEntryCheck string

// The output check's results.
const (
	// CallEntryEqual: the response's authorization_details equals the entry
	// sent, as decoded JSON values.
	CallEntryEqual CallEntryCheck = "equal"
	// CallEntryAbsent: the response has no authorization_details member, or
	// it is null.
	CallEntryAbsent CallEntryCheck = "absent"
	// CallEntryDiffers: the response's authorization_details is not the
	// entry sent: another entry, another shape, an extra entry, or a value
	// that is not valid JSON without repeated member names.
	CallEntryDiffers CallEntryCheck = "differs"
)

// checkCallEntry compares got, the authorization_details member of AS-2's
// token response (nil when absent), with sent, the array the broker sent
// AS-1, as decoded JSON values: key order and whitespace do not matter. A
// member with a repeated name or invalid UTF-8 differs, since decoders
// disagree on which value it holds; json.RawMessage is a jsontext.Value,
// whose IsValid refuses both.
func checkCallEntry(sent, got json.RawMessage) CallEntryCheck {
	got = bytes.TrimSpace(got)
	if len(got) == 0 || string(got) == "null" {
		return CallEntryAbsent
	}
	if !got.IsValid() {
		return CallEntryDiffers
	}
	var want, have any
	if json.Unmarshal(sent, &want) != nil || json.Unmarshal(got, &have) != nil || !reflect.DeepEqual(want, have) {
		return CallEntryDiffers
	}
	return CallEntryEqual
}

// callEntryError is the refusal of a gateway token that failed the output
// check, or nil when it passed. Neither refusal is retried: the broker sent
// the entry, so a token without it points to a fault at AS-1 or AS-2.
func callEntryError(check CallEntryCheck) error {
	switch check {
	case CallEntryEqual:
		return nil
	case CallEntryAbsent:
		return ReasonError(ReasonCallEntryMissing, "the gateway token's response carries no call entry")
	default:
		return ReasonError(ReasonCallEntryMismatch, "the gateway token's call entry is not the one the broker sent")
	}
}
