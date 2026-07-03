package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
)

// CallResult is the adapter value-object view of a remote MCP tool call's raw
// result, BEFORE any model-facing truncation. CallMcpWithQuery filters it
// through jq. It carries NO mcpsdk types (so internal/app can consume it
// without the SDK dependency): the per-block content is the existing
// ResourceContents adapter VO, and StructuredContent is a json.RawMessage.
//
// It is deliberately separate from remoteTool.Execute's session.ToolResult:
// Execute truncates and fail-closes on over-cap structured output (a truncated
// JSON blob is unparseable, so the model gets an actionable error pointing at
// CallMcpWithQuery instead). CallTool returns the UNTRUNCATED raw result so the
// jq filter in CallMcpWithQuery can narrow it BEFORE it enters model context.
type CallResult struct {
	// Server is the configured name of the server the call was routed to.
	Server string
	// Tool is the remote tool name the call was made against (the server-side
	// name, NOT the mcp__<server>__<tool> namespaced name the model sees).
	Tool string
	// Content is the per-block content (text/blob + MIME + URI), extracted from
	// the remote CallToolResult.Content. The model-facing flattened string half
	// is NOT here — CallMcpWithQuery builds its own (filtered) string, or the
	// caller renders the blocks itself.
	Content []ResourceContents
	// StructuredContent is the remote result's structuredContent, marshaled to
	// json.RawMessage. nil when the remote result carried none. CallMcpWithQuery
	// prefers StructuredContent (it is the typed, schema-validated view) and
	// falls back to a JSON-parsing Text block otherwise.
	StructuredContent json.RawMessage
	// IsError reports whether the remote tool reported an error (the MCP
	// IsError flag). A remote error is still a successful CallTool at the
	// transport level — the caller decides how to surface it.
	IsError bool
}

// callTool invokes a remote tool by name on this server and returns the raw
// typed result, BEFORE any model-facing truncation. It mirrors remoteTool.Execute's
// call path (withSession → sess.CallTool) but skips the truncation, block-clamp,
// and fail-closed logic — CallMcpWithQuery wants the full unfiltered result so a
// jq filter can narrow it. Transport faults surface verbatim; a connection-drop
// after the one bounded reconnect (the retry-drop case) or a reconnect dial
// failure maps to the clear "unavailable after reconnect" message, matching
// remoteTool.Execute. args (a json.RawMessage of the tool's input schema) are
// decoded to an any via argsFor and passed verbatim to the remote tool.
func (s *Server) callTool(ctx context.Context, tool string, args json.RawMessage) (CallResult, error) {
	argsAny, err := argsFor(args)
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp: invalid tool arguments for %q: %w", tool, err)
	}

	var res *mcpsdk.CallToolResult
	callErr := s.withSession(ctx, func(sess *mcpsdk.ClientSession) error {
		var err error
		res, err = sess.CallTool(ctx, &mcpsdk.CallToolParams{
			Name:      tool,
			Arguments: argsAny,
		})
		return err
	})
	if callErr != nil {
		// Propagate context cancellation as a hard Go error so the caller
		// distinguishes an aborted call from a recoverable fault.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CallResult{}, ctxErr
		}
		// A connection-drop after the one reconnect attempt OR a reconnect dial
		// failure (errReconnectFailed) is a terminal "server unavailable" —
		// surface the clear message, not the raw transport string. Any other
		// fault is surfaced verbatim. (Mirrors remoteTool.Execute.)
		if isConnectionDrop(callErr) || errors.Is(callErr, errReconnectFailed) {
			return CallResult{}, fmt.Errorf("mcp call failed: MCP server %q unavailable after reconnect", s.name)
		}
		return CallResult{}, fmt.Errorf("mcp call failed: %w", callErr)
	}

	var structured json.RawMessage
	if res.StructuredContent != nil {
		// The SDK unmarshals structuredContent into an any; re-marshal to a
		// stable json.RawMessage so the caller (CallMcpWithQuery) can feed it
		// to jq without re-decoding. A marshal failure is unexpected (the SDK
		// already unmarshaled it); leave structured nil rather than inventing a
		// noisy error — the Content blocks still carry the data.
		if raw, ok := res.StructuredContent.(json.RawMessage); ok {
			structured = raw
		} else if b, mErr := json.Marshal(res.StructuredContent); mErr == nil {
			structured = b
		}
	}

	// Early oversize gate (PR #226 review finding #5): without this, a
	// hostile/oversized remote result would be fully copied by
	// callResultContent (every block, incl. any blob/image data) BELOW, and
	// only THEN would CallMcpWithQuery.Execute notice the JSON source it
	// picked exceeds jq.MaxInputBytes — peak memory ~3x the response. The
	// go-sdk has already parsed the whole result into memory by the time we
	// get here (an irreducible floor this cannot bound); this only skips OUR
	// additional per-block copy once the JSON source Execute would choose
	// (StructuredContent first, else the first JSON-parseable text/embedded
	// -resource block) is already over cap. jqInputPreview mirrors Execute's
	// precedence directly off the raw SDK content so this gate doesn't itself
	// pay for the copy it is trying to avoid. Skipped on a remote tool-level
	// error: Execute's IsError branch concatenates ALL content text
	// (concatContentText) regardless of jq's cap, so it needs the full copy.
	if !res.IsError {
		if text, size, found := jqInputPreview(res, structured); found && size > jq.MaxInputBytes {
			var content []ResourceContents
			if structured == nil {
				// The chosen source was a text/embedded-resource block (not
				// StructuredContent): carry just that one block so Execute's
				// existing selection finds it and reports the identical
				// over-cap message. Other blocks are irrelevant — Execute
				// rejects before it would ever look at them.
				content = []ResourceContents{{Text: text}}
			}
			return CallResult{
				Server:            s.name,
				Tool:              tool,
				Content:           content,
				StructuredContent: structured,
				IsError:           res.IsError,
			}, nil
		}
	}

	out := callResultContent(res.Content)

	return CallResult{
		Server:            s.name,
		Tool:              tool,
		Content:           out,
		StructuredContent: structured,
		IsError:           res.IsError,
	}, nil
}

// jqInputPreview locates the JSON source CallMcpWithQuery.Execute will choose
// for jq — StructuredContent first (already marshaled by the caller), else
// the first JSON-parseable text (TextContent or EmbeddedResource.Resource.Text)
// block — directly off the raw SDK result, mirroring Execute's precedence
// WITHOUT paying for callResultContent's full per-block copy. It exists solely
// to let callTool's early oversize gate (above) decide whether that copy is
// worth doing; Execute performs its own (unchanged) selection over the
// returned CallResult on every path, including the ones this function skips.
func jqInputPreview(res *mcpsdk.CallToolResult, structured json.RawMessage) (text string, size int, found bool) {
	if len(structured) > 0 {
		return "", len(structured), true
	}
	for _, p := range res.Content {
		var t string
		switch c := p.(type) {
		case *mcpsdk.TextContent:
			t = c.Text
		case *mcpsdk.EmbeddedResource:
			if c.Resource != nil {
				t = c.Resource.Text
			}
		default:
			continue
		}
		if t == "" {
			continue
		}
		var probe any
		if json.Unmarshal([]byte(t), &probe) == nil {
			return t, len(t), true
		}
	}
	return "", 0, false
}

// callResultContent translates an MCP CallToolResult.Content slice into the
// adapter ResourceContents VO, per-block. It is the raw-content analogue of
// mapContent (tool.go) — WITHOUT the model-facing string half, since
// CallMcpWithQuery builds its own filtered string and the caller renders blocks
// itself. Each block kind maps to the matching ResourceContents fields:
//   - TextContent    → Text
//   - ImageContent   → MIMEType + Blob (Data)
//   - AudioContent   → MIMEType + Blob (Data)
//   - EmbeddedResource → URI + MIMEType + (Text | Blob) from the embedded resource
//   - ResourceLink   → URI + MIMEType (a reference, no body)
//
// Unknown content kinds are skipped (a future SDK content type must not break the
// call; it simply carries no block). A nil EmbeddedResource.Resource yields an
// empty ResourceContents (URI/MIMEType blank), mirroring resourceContentsFromSDK.
func callResultContent(parts []mcpsdk.Content) []ResourceContents {
	if len(parts) == 0 {
		return nil
	}
	out := make([]ResourceContents, 0, len(parts))
	for _, p := range parts {
		switch c := p.(type) {
		case *mcpsdk.TextContent:
			out = append(out, ResourceContents{Text: c.Text})
		case *mcpsdk.ImageContent:
			out = append(out, ResourceContents{MIMEType: c.MIMEType, Blob: c.Data})
		case *mcpsdk.AudioContent:
			out = append(out, ResourceContents{MIMEType: c.MIMEType, Blob: c.Data})
		case *mcpsdk.ResourceLink:
			out = append(out, ResourceContents{URI: c.URI, MIMEType: c.MIMEType})
		case *mcpsdk.EmbeddedResource:
			if c.Resource == nil {
				out = append(out, ResourceContents{})
				continue
			}
			out = append(out, ResourceContents{
				URI:      c.Resource.URI,
				MIMEType: c.Resource.MIMEType,
				Text:     c.Resource.Text,
				Blob:     c.Resource.Blob,
			})
		default:
			// Unknown content kind: skip rather than synthesize a placeholder —
			// a future SDK content type carries no block until explicitly wired.
		}
	}
	return out
}
