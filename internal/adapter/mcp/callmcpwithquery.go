package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// callMcpWithQueryToolName is the harness-level meta-tool name (plain,
// non-namespaced) for CallMcpWithQuery, matching the ListMcpResources /
// ReadMcpResource resource meta-tools.
const callMcpWithQueryToolName = "CallMcpWithQuery"

// callMcpWithQueryTool lets the model call an MCP tool and filter its JSON
// result through a jq expression BEFORE the result enters context, so a large
// JSON response does not have to be narrowed or truncated into an unparseable
// blob. It is read-only: it calls a remote tool (which mutates nothing on this
// process) and filters the result in memory (no disk), so it slots into
// read-parallel dispatch and survives the plan-mode catalog filter.
type callMcpWithQueryTool struct {
	provider Provider
}

var _ tool.Tool = callMcpWithQueryTool{}

func (callMcpWithQueryTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: callMcpWithQueryToolName,
		Description: "Call an MCP tool and filter its JSON result through a jq expression before it " +
			"enters context, so a large JSON result doesn't have to be narrowed or truncated. Use this " +
			"when a tool returns a big JSON structure and you only need a subset of fields. The remote " +
			"tool's full result is fetched and filtered in memory (no disk); only the filtered subset is " +
			"returned. Fails loud if the remote result isn't JSON or the jq filter is invalid. Prefer " +
			"narrowing the remote call with its own pagination/filter params when possible.\n\n" +
			"The remote tool's OWN parameters go INSIDE the \"args\" object — never as top-level fields " +
			"beside server/tool/jq_filter, and never as a string. Complete example: to filter the " +
			"\"github\" server's \"list_pull_requests\" tool down to a few fields, call this tool with\n" +
			"{\n" +
			"  \"server\": \"github\",\n" +
			"  \"tool\": \"list_pull_requests\",\n" +
			"  \"args\": {\"owner\": \"stacklok\", \"repo\": \"toolhive\", \"state\": \"open\", \"perPage\": 30},\n" +
			"  \"jq_filter\": \"[.[] | {number, title, state, updated_at}]\"\n" +
			"}",
		Schema: toolkit.Schema(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "The MCP server name that owns the tool (e.g. \"github\")."},
    "tool": {"type": "string", "description": "The remote tool name (NOT the mcp__-prefixed namespaced name), e.g. \"list_pull_requests\"."},
    "args": {"type": "object", "description": "A JSON object holding the remote tool's OWN input parameters as key/value pairs, e.g. {\"owner\": \"stacklok\", \"repo\": \"toolhive\", \"state\": \"open\"}. This MUST be a JSON object, not a string and not top-level fields. Omit entirely if the remote tool takes no arguments.", "additionalProperties": true},
    "jq_filter": {"type": "string", "description": "A jq expression to apply to the JSON result (e.g. \".items | length\" or \".items[] | {id, name}\")."}
  },
  "required": ["server", "tool", "jq_filter"]
}`),
	}
}

func (callMcpWithQueryTool) ReadOnly() bool { return true }

type callMcpWithQueryArgs struct {
	Server   string          `json:"server"`
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	JQFilter string          `json:"jq_filter"`
}

// Execute calls the remote tool, then narrows its JSON result through jq before
// returning it to the model. The JSON input fed to jq is chosen by precedence:
// StructuredContent (the typed, schema-validated view) first, then the first
// JSON-parseable Text content block, else a loud error. A remote tool-level
// error (IsError) is surfaced verbatim (truncated) without running jq — the
// model asked to filter a failed call, so it is told the call failed.
func (t callMcpWithQueryTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	var args callMcpWithQueryArgs
	if msg, ok := toolkit.ParseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	server := strings.TrimSpace(args.Server)
	toolName := strings.TrimSpace(args.Tool)
	jqFilter := strings.TrimSpace(args.JQFilter)
	if server == "" {
		return session.NewToolError(in.ID, `the "server" argument is required`), nil
	}
	if toolName == "" {
		return session.NewToolError(in.ID, `the "tool" argument is required`), nil
	}
	if jqFilter == "" {
		return session.NewToolError(in.ID, `the "jq_filter" argument is required`), nil
	}

	// Normalize the remote args. The schema asks for a JSON object, but some
	// models (notably GLM-family, whose native <arg_key>/<arg_value> tool format
	// does not express nesting) serialize a nested object parameter as a JSON
	// STRING instead. Passing that string to the remote server yields an opaque
	// "cannot unmarshal string into map[string]interface{}" from the far side.
	// normalizeRemoteArgs recovers the common recoverable case (a JSON object
	// encoded as a string) and turns everything else into a clear, self-correcting
	// error naming the exact expected shape — instead of a remote 400 the model
	// can't act on. It is safe to coerce here (unlike the direct namespaced tool
	// path) because THIS parameter is contractually an object.
	remoteArgs, msg := normalizeRemoteArgs(args.Args)
	if msg != "" {
		return session.NewToolError(in.ID, msg), nil
	}

	result, err := t.provider.CallTool(ctx, server, toolName, remoteArgs)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		// Mirrors readResourceTool: a reconnect failure or a connection-drop
		// after the one reconnect attempt surfaces as the clear "unavailable
		// after reconnect" message, never the raw transport string. Other
		// faults stay verbatim so the model can self-correct.
		if message := unavailableMessage(err, "call MCP tool failed", server); message != "" {
			return session.NewToolError(in.ID, message), nil
		}
		return session.NewToolError(in.ID, fmt.Sprintf("call MCP tool failed: %v", err)), nil
	}

	// A remote tool-level error: surface the remote error text (truncated)
	// without running jq — the model asked to filter a failed call; tell it
	// the call failed.
	if result.IsError {
		text := concatContentText(result.Content)
		return session.NewToolError(in.ID, toolkit.Truncate(text, toolkit.MaxOutputBytes)), nil
	}

	// Determine the JSON input for jq. StructuredContent (the typed,
	// schema-validated view) wins; otherwise the first JSON-parseable Text
	// content block; otherwise a loud error.
	var jsonInput []byte
	if len(result.StructuredContent) > 0 {
		jsonInput = result.StructuredContent
	} else {
		var found bool
		for _, c := range result.Content {
			if c.Text == "" {
				continue
			}
			var probe any
			if json.Unmarshal([]byte(c.Text), &probe) == nil {
				jsonInput = []byte(c.Text)
				found = true
				break
			}
		}
		if !found {
			return session.NewToolError(in.ID, fmt.Sprintf(
				"CallMcpWithQuery: tool %q result is not JSON (no StructuredContent and no JSON-parseable text block); a jq filter requires a JSON result.",
				toolName)), nil
		}
	}

	if len(jsonInput) > jq.MaxInputBytes {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"CallMcpWithQuery: remote result is %d bytes (exceeds the %d-byte in-memory filter cap); narrow the call using the remote tool's own pagination/filter parameters.",
			len(jsonInput), jq.MaxInputBytes)), nil
	}

	// jq.Run applies DefaultTimeout when ctx has no deadline; be explicit so a
	// caller-supplied short deadline still bounds pathological compute.
	runCtx, cancel := context.WithTimeout(ctx, jq.DefaultTimeout)
	defer cancel()
	filtered, err := jq.Run(runCtx, jqFilter, jsonInput)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("CallMcpWithQuery: %v", err)), nil
	}

	// Fail-closed on an OVER-CAP filtered result, mirroring tool.go's
	// structuredTooLargeError. jq.Run already caps the filtered output at its own
	// jq.MaxOutputBytes (100KB), which is looser than toolkit.MaxOutputBytes
	// (25KB); a 25-100KB result truncated on a byte boundary here would be
	// unparseable JSON handed to the model. filtered is always JSON-shaped
	// (jq.Run json-encodes every value), so oversized here means oversized JSON —
	// the same hazard the primary path fail-closes on rather than truncates.
	if len(filtered) > toolkit.MaxOutputBytes {
		return session.NewToolError(in.ID, filteredTooLargeError(toolName, server, jqFilter)), nil
	}

	return session.NewToolResult(in.ID, filtered), nil
}

// normalizeRemoteArgs coerces the model-supplied "args" value into the JSON
// object the remote MCP tool expects, or returns a model-facing correction
// message (the second return; empty means success):
//
//   - absent / empty / null           -> (nil, "")          no arguments
//   - a JSON object                    -> (raw, "")          passed through
//   - a JSON string that is itself a
//     JSON object                      -> (parsed, "")       double-encoding recovered
//   - anything else (incl. a garbled
//     or non-object string)            -> (nil, correction)  self-correcting error
//
// The string-recovery arm exists because some models (notably GLM-family, whose
// native <arg_key>/<arg_value> tool format does not express nesting) serialize a
// nested object parameter as a JSON string. It recovers the case where that
// string is itself a JSON object; it deliberately does NOT parse provider-specific
// tool-call encodings (e.g. raw GLM <arg_key> pairs) — that would couple this
// provider-agnostic MCP layer to one model's wire format. Unrecoverable shapes
// become a clear correction naming the exact expected object, so the model retries
// instead of hitting an opaque remote "cannot unmarshal string into map" 400.
func normalizeRemoteArgs(raw json.RawMessage) (json.RawMessage, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, ""
	}
	switch trimmed[0] {
	case '{':
		// A JSON object: validate it parses so a malformed object surfaces a
		// clear error here rather than a remote 400.
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, remoteArgsError(fmt.Sprintf("the \"args\" object is not valid JSON (%v)", err))
		}
		return trimmed, ""
	case '"':
		// A JSON string: the model serialized the object as a string. Recover the
		// inner value when it is itself a JSON object; otherwise correct.
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, remoteArgsError("the \"args\" value could not be decoded")
		}
		inner := strings.TrimSpace(s)
		var obj map[string]any
		if inner == "" || json.Unmarshal([]byte(inner), &obj) != nil {
			return nil, remoteArgsError("the \"args\" value arrived as a string, not a JSON object")
		}
		return json.RawMessage(inner), ""
	default:
		// A JSON array, number, bool, etc. — not an object.
		return nil, remoteArgsError("the \"args\" value must be a JSON object")
	}
}

// remoteArgsError is the model-facing correction shown when the remote "args"
// value is not (and cannot be recovered into) a JSON object. It names the exact
// expected shape with an example so the model can self-correct on the next turn.
func remoteArgsError(detail string) string {
	return fmt.Sprintf(`CallMcpWithQuery: %s. The "args" value must be a JSON OBJECT holding the remote `+
		`tool's own parameters, e.g. {"owner":"stacklok","repo":"toolhive","state":"open"} — not a string, `+
		`and not top-level fields beside server/tool/jq_filter. Retry with "args" as a JSON object (or omit `+
		`it entirely if the tool takes no arguments).`, detail)
}

// filteredTooLargeError is the fail-closed message a CallMcpWithQuery filtered
// result over toolkit.MaxOutputBytes surfaces to the model. Truncating it would
// leave unparseable JSON, so it names the actionable recovery path: narrow the
// jq filter further, or paginate the remote call.
func filteredTooLargeError(toolName, serverName, jqFilter string) string {
	return fmt.Sprintf(
		"CallMcpWithQuery: filtered result for tool %q (server %q) with jq_filter %q exceeded the "+
			"%d-byte output cap and is JSON. Truncating it would make it unparseable, so it was NOT "+
			"returned. To get the data, narrow the jq filter further to select a smaller subset (e.g. "+
			"add pagination slicing like \".items[0:20]\" or project fewer fields), or paginate the "+
			"remote call using the remote tool's own filter/pagination parameters.",
		toolName, serverName, jqFilter, toolkit.MaxOutputBytes)
}

// RegisterCallWithQuery registers the CallMcpWithQuery meta-tool into cat, gated
// on the manager exposing at least one tool (the escape hatch is meaningless
// without tools to call). It returns whether the tool was registered and the
// first registration error (if any). It takes the *Manager (not just Provider)
// because the gate reads the tool list — Provider has CallTool but no Tools(),
// while *Manager exposes both (it is the production Provider implementation AND
// the tool-list owner). This mirrors RegisterResourceTools's "register only when
// non-empty" gating, but over TOOLS rather than resources: the meta-tool is
// about calling a remote tool, so it registers whenever any MCP tools are
// present, independent of the resource meta-tools' MCPResourceTools gate.
func RegisterCallWithQuery(cat *tool.Catalog, m *Manager) (bool, error) {
	if m == nil {
		return false, nil
	}
	if len(m.Tools()) == 0 {
		return false, nil
	}
	if err := cat.Register(callMcpWithQueryTool{provider: m}); err != nil {
		return true, fmt.Errorf("mcp: register %q: %w", callMcpWithQueryToolName, err)
	}
	return true, nil
}

// concatContentText concatenates the Text fields of every content block (text
// blocks and the text half of embedded resources). Used to surface a remote
// tool-level error's text without running jq.
func concatContentText(contents []ResourceContents) string {
	if len(contents) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range contents {
		if c.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
