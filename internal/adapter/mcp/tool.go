package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// remoteTool adapts a single tool advertised by a remote MCP server into the
// harness's tool.Tool interface. Execute proxies the call back over the live
// MCP session; the Environment is ignored because the tool runs remotely and
// consumes neither its Workspace nor its CommandRunner.
//
// It holds the owning *Server (not a *ClientSession) so Execute can re-establish
// a dropped session transparently via Server.withSession (see reconnect.go,
// ADR 0056).
//
// outputSchema carries the remote tool's optional OutputSchema (a JSON Schema),
// marshaled to json.RawMessage at construction time. It is used ONLY for the
// OPTIONAL defense-in-depth validation of StructuredContent in Execute — the
// SDK client-side CallTool does NOT validate, so this is belt-and-suspenders
// against a misbehaving server. nil when the tool advertises no output schema.
type remoteTool struct {
	spec     tool.ToolSpec
	readOnly bool
	// remoteName is the tool's name on the server, used in the CallTool request
	// (NOT the namespaced spec.Name the model sees).
	remoteName   string
	server       *Server
	outputSchema json.RawMessage
}

// newRemoteTool builds a remoteTool from a server-advertised mcpsdk.Tool.
//
// Namespacing: the model-facing name is mcp__<server>__<toolname>, which cannot
// collide with built-in tool names. ReadOnly defaults to false (conservative,
// so the dispatcher serializes the call) unless the remote advertises
// annotations.readOnlyHint == true. The schema is the remote inputSchema
// marshaled to json.RawMessage.
func newRemoteTool(serverName string, srv *Server, remote *mcpsdk.Tool) (*remoteTool, error) {
	if remote == nil {
		return nil, fmt.Errorf("mcp: server %q advertised a nil tool", serverName)
	}
	if remote.Name == "" {
		return nil, fmt.Errorf("mcp: server %q advertised a tool with no name", serverName)
	}

	schema, err := schemaFor(remote.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("mcp: tool %q on server %q: input schema: %w", remote.Name, serverName, err)
	}

	readOnly := remote.Annotations != nil && remote.Annotations.ReadOnlyHint

	var outputSchema json.RawMessage
	if remote.OutputSchema != nil {
		if raw, ok := remote.OutputSchema.(json.RawMessage); ok {
			outputSchema = raw
		} else if b, err := json.Marshal(remote.OutputSchema); err == nil {
			outputSchema = b
		}
		// A marshal failure leaves outputSchema nil — the optional validator
		// simply stays inert; the structured content still rides through.
	}

	return &remoteTool{
		spec: tool.ToolSpec{
			Name:        namespacedName(serverName, remote.Name),
			Description: remote.Description,
			Schema:      schema,
		},
		readOnly:     readOnly,
		remoteName:   remote.Name,
		server:       srv,
		outputSchema: outputSchema,
	}, nil
}

// namespacedName produces the mcp__<server>__<tool> model-facing name.
func namespacedName(server, toolName string) string {
	return "mcp__" + server + "__" + toolName
}

// schemaFor normalizes the SDK's input schema (which arrives client-side as a
// map[string]any) into a json.RawMessage. A nil schema yields an empty
// object-schema so the model always sees valid JSON.
func schemaFor(in any) (json.RawMessage, error) {
	if in == nil {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	if raw, ok := in.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// Spec returns the model-facing specification (namespaced name, remote
// description, remote input schema).
func (t *remoteTool) Spec() tool.ToolSpec { return t.spec }

// ReadOnly reports the remote read-only hint (defaulting to false).
func (t *remoteTool) ReadOnly() bool { return t.readOnly }

// Execute proxies the call to the remote MCP server over the live session and
// maps the MCP result into a session.ToolResult.
//
// The Workspace is intentionally ignored: MCP tools execute on the remote
// server, not against the local workspace. ctx cancellation is honored by the
// SDK call. A transport/protocol fault is returned as a model-facing tool error
// (IsError) rather than a Go error, so the loop feeds it back to the model for
// self-correction instead of aborting the turn; the Go error return is reserved
// for cases the model genuinely cannot recover from (none here).
func (t *remoteTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	// Respect cancellation before doing any work.
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}

	args, err := argsFor(in.Args)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("invalid tool arguments: %v", err)), nil
	}

	var res *mcpsdk.CallToolResult
	callErr := t.server.withSession(ctx, func(sess *mcpsdk.ClientSession) error {
		var err error
		res, err = sess.CallTool(ctx, &mcpsdk.CallToolParams{
			Name:      t.remoteName,
			Arguments: args,
		})
		return err
	})
	if callErr != nil {
		// Propagate context cancellation as a hard Go error so the loop can
		// distinguish an aborted turn from a recoverable tool failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		// A connection-drop after the one reconnect attempt (the retry-drop
		// case) OR a reconnect DIAL failure (errReconnectFailed — the server
		// could not be re-established, e.g. a genuinely-down server) is a
		// terminal "server unavailable" — surface the clear message, not the
		// raw transport string ("connection refused" / "context deadline
		// exceeded"). Any other fault is surfaced verbatim.
		if message := unavailableMessage(callErr, "mcp call failed", t.server.Name()); message != "" {
			return session.NewToolError(in.ID, message), nil
		}
		return session.NewToolError(in.ID, fmt.Sprintf("mcp call failed: %v", callErr)), nil
	}

	modelStr, blocks := mapContent(res.Content)

	// Structured content: the spec's SHOULD is that a tool returning
	// StructuredContent also serializes it as a TextContent block. The harness
	// honours that by appending the JSON to the model-facing string AND building
	// a dedicated BlockStructuredContent block (decision #7).
	if res.StructuredContent != nil {
		structuredJSON, err := json.Marshal(res.StructuredContent)
		if err == nil {
			blocks = append(blocks, session.NewStructuredContentBlock(string(structuredJSON)))
			if modelStr != "" {
				modelStr += "\n"
			}
			modelStr += string(structuredJSON)
			// Defense-in-depth: validate the structured instance against the
			// remote tool's advertised outputSchema. The SDK client-side CallTool
			// does NOT validate, so this catches a misbehaving server. A failure
			// is surfaced as a model-facing NOTE — never a silent drop, never a
			// hard error (the structured content still rides through as a block).
			if note := validateStructuredContent(t.outputSchema, res.StructuredContent); note != "" {
				modelStr += "\n" + note
			}
		}
		// A marshal failure of StructuredContent is unexpected (the SDK already
		// unmarshaled it); leave it inert rather than inventing a noisy note.
	}

	// Fail-closed on an OVER-CAP STRUCTURED (JSON) result. Truncating a JSON
	// blob would leave the model with a partial, unparseable fragment it can't
	// reason over, so we surface an actionable tool ERROR pointing at the two
	// escape hatches (narrow/paginate the remote call, or run it through
	// CallMcpWithQuery with a jq filter) instead. This runs AFTER the
	// StructuredContent mirror is appended to modelStr (so a structured result
	// that includes a JSON-shaped TextContent is also caught) and BEFORE the
	// truncate call. Error results (res.IsError) are deliberately NOT
	// fail-closed: an error payload stays string-only and is truncated as today,
	// so the model still reads the error text and self-corrects.
	if !res.IsError && len(modelStr) > toolkit.MaxOutputBytes && isStructuredResult(t.outputSchema, res, res.Content) {
		return session.NewToolError(in.ID, structuredTooLargeError(t.spec.Name, t.server.Name())), nil
	}

	content := toolkit.Truncate(modelStr, toolkit.MaxOutputBytes)

	// Enforce the per-result block caps (CWE-770). On a violation, drop the
	// offending block and append a model-facing note to the model string rather
	// than hard-failing — a single oversized block must not blackhole the whole
	// result. The note rides AFTER the text and is preserved within the output
	// cap: the text is (re-)truncated to leave room for the note, so the total
	// Content stays bounded by toolkit.MaxOutputBytes while the clamp reason
	// survives.
	parts := blocks
	if err := session.ValidateToolResultParts(parts); err != nil {
		var note string
		parts, note = clampToolResultParts(parts, err)
		content = appendWithinCap(content, note)
	}

	if res.IsError {
		// Error results stay string-only (devex finding #6): the model reads the
		// error text directly and self-corrects, and a typed block on an error
		// result is a v1 scope limit we deliberately keep simple.
		return session.NewToolError(in.ID, content), nil
	}
	return session.NewToolResultWithParts(in.ID, content, parts), nil
}

// argsFor decodes the model's raw JSON args into the any value the SDK marshals
// back to JSON. Empty/absent args map to nil (no arguments).
func argsFor(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// renderableImageMIMEs is the allowlist of image media types this harness will
// actually forward to a provider as a typed BlockImage. session.NewImageContent
// (the shared domain constructor) only validates the "image/" PREFIX, which is
// deliberately broad — but a valid-yet-exotic subtype (image/svg+xml,
// image/tiff, image/bmp, …) is NOT accepted by mainstream vision providers
// (Anthropic, OpenAI) and would surface as a hard HTTP 400 from the provider,
// failing the whole turn. There is no runtime source of truth for "which image
// subtypes does the configured provider accept" at this granularity, so this is
// a hardcoded set of the four core web image formats both Anthropic and OpenAI
// document support for. Degrading an unlisted MIME to a text note fails safe:
// worst case a newly-supported format is needlessly described as text instead
// of rendered as an image, never a crashed session.
var renderableImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// isAllowlistedImageMIME reports whether mime (optionally carrying
// parameters, e.g. "image/jpeg; charset=binary") names a core image format
// this harness will forward as a typed image block. Matching is
// case-insensitive on the bare media type.
func isAllowlistedImageMIME(mime string) bool {
	base, _, _ := strings.Cut(mime, ";")
	return renderableImageMIMEs[strings.ToLower(strings.TrimSpace(base))]
}

// mapContent translates an MCP CallToolResult.Content slice into BOTH a
// model-facing string (the legacy flattened view, kept for the default Content
// field and for byte-stable truncation) and a slice of typed session.Content
// tool-result blocks (the new Parts path).
//
// Text content is included verbatim in both outputs. Non-text content (images,
// audio, resource links, embedded resources) is summarized in the model string
// by type — never base64-dumped — so the model knows something non-textual came
// back without the adapter inventing an encoding, while the parallel block
// carries the typed payload a provider can render natively. Resource links are
// NEVER auto-dereferenced (decision #5): a link is a reference, and any fetch is
// the provider's responsibility.
func mapContent(parts []mcpsdk.Content) (modelString string, blocks []session.Content) {
	var b strings.Builder
	for _, p := range parts {
		switch c := p.(type) {
		case *mcpsdk.TextContent:
			blocks = append(blocks, session.NewTextBlock(c.Text))
			b.WriteString(c.Text)
		case *mcpsdk.ImageContent:
			// NewImageContent only validates the "image/" PREFIX (broad, by
			// design — it is the shared domain constructor). At THIS boundary we
			// additionally enforce isAllowlistedImageMIME so a valid-but-exotic
			// image MIME (e.g. image/svg+xml, image/tiff) can never reach a
			// provider that will 400 on it — degrade to a text note instead.
			// Once allowlisted, NewImageContent validates (mime/kind consistency,
			// exactly-one-of data/url) and builds the media fields; we then stamp
			// BlockKind so the part reads as a tool-result BLOCK (not a legacy
			// Message media part). On a construction error we surface a
			// model-facing note rather than dropping silently — the model needs
			// to see something came back malformed. The block is omitted on
			// failure (both the allowlist miss and the construction error).
			if !isAllowlistedImageMIME(c.MIMEType) {
				fmt.Fprintf(&b, "[image content: %s (unsupported image type, not sent)]", c.MIMEType)
				continue
			}
			blk, err := session.NewImageContent(c.MIMEType, c.Data)
			if err != nil {
				fmt.Fprintf(&b, "[image content: %s (invalid: %v)]", c.MIMEType, err)
				continue
			}
			blk.BlockKind = session.BlockImage
			blocks = append(blocks, blk)
			fmt.Fprintf(&b, "[image content: %s]", c.MIMEType)
		case *mcpsdk.AudioContent:
			blk, err := session.NewAudioContent(c.MIMEType, c.Data)
			if err != nil {
				fmt.Fprintf(&b, "[audio content: %s (invalid: %v)]", c.MIMEType, err)
				continue
			}
			blk.BlockKind = session.BlockAudio
			blocks = append(blocks, blk)
			fmt.Fprintf(&b, "[audio content: %s]", c.MIMEType)
		case *mcpsdk.ResourceLink:
			// Audience is advisory-only server self-attestation (CWE-345);
			// carried through for operator/model visibility, NEVER enforced here.
			audience := audienceFrom(c.Annotations)
			blocks = append(blocks, session.NewResourceLinkBlock(
				c.URI, c.Name, c.Title, c.Description, c.MIMEType, derefSize(c.Size), audience))
			// Include the name when present so the default model-facing string is
			// a useful handle, not a bare placeholder URI.
			if c.Name != "" {
				fmt.Fprintf(&b, "[resource link: %s (%s)]", c.URI, c.Name)
			} else {
				fmt.Fprintf(&b, "[resource link: %s]", c.URI)
			}
		case *mcpsdk.EmbeddedResource:
			if c.Resource == nil {
				b.WriteString("[embedded resource: missing resource]")
				continue
			}
			uri := c.Resource.URI
			mime := c.Resource.MIMEType
			audience := audienceFrom(c.Annotations)
			switch {
			case len(c.Resource.Blob) > 0:
				// Binary form: summarize in the model string (mirror
				// resource.go:flattenResourceContents), never base64-dump. The
				// block carries the raw bytes for a provider that can render them.
				blk, err := session.NewEmbeddedResourceBlock(uri, mime, "", c.Resource.Blob, audience)
				if err != nil {
					fmt.Fprintf(&b, "[embedded resource: %s (invalid: %v)]", uri, err)
					continue
				}
				if mime == "" {
					mime = "application/octet-stream"
				}
				blocks = append(blocks, blk)
				fmt.Fprintf(&b, "[binary resource: %s, %d bytes]", mime, len(c.Resource.Blob))
			case c.Resource.Text != "":
				blk, err := session.NewEmbeddedResourceBlock(uri, mime, c.Resource.Text, nil, audience)
				if err != nil {
					fmt.Fprintf(&b, "[embedded resource: %s (invalid: %v)]", uri, err)
					continue
				}
				blocks = append(blocks, blk)
				b.WriteString(c.Resource.Text)
			default:
				b.WriteString("[embedded resource: empty resource]")
			}
		default:
			b.WriteString("[unsupported content]")
		}
	}
	return b.String(), blocks
}

// appendWithinCap appends note to text, keeping the combined length within
// toolkit.MaxOutputBytes. When text alone already fills the cap, it is
// re-truncated to leave room for the note (plus a separating newline), so the
// clamp reason always survives within the bounded Content string. An empty note
// returns text unchanged (still cap-bounded by a prior Truncate).
func appendWithinCap(text, note string) string {
	if note == "" {
		return text
	}
	budget := toolkit.MaxOutputBytes
	// Room for "\n" + note, capped so a pathological note can't evict the whole
	// text: reserve at least half the cap for the note when it is tiny.
	need := len(note) + 1
	if need > budget/2 {
		need = budget / 2
	}
	if len(text)+need > budget {
		text = toolkit.Truncate(text, budget-need)
	}
	if text != "" {
		text += "\n"
	}
	return text + note
}

// flattenContentModel returns only the model-facing string half of mapContent.
// It is the legacy flattened view used by callers (e.g. prompt assembly) that
// need the textual summary without the typed blocks.
func flattenContentModel(parts []mcpsdk.Content) string {
	s, _ := mapContent(parts)
	return s
}

// audienceFrom extracts the advisory audience list from an MCP Annotations
// value, carrying it through verbatim as []string. The Audience field is
// UNTRUSTED SERVER SELF-ATTESTATION (CWE-345): it is advisory-only and MUST
// NEVER be treated as authoritative by the harness (see session.Content.Audience).
func audienceFrom(a *mcpsdk.Annotations) []string {
	if a == nil || len(a.Audience) == 0 {
		return nil
	}
	out := make([]string, len(a.Audience))
	for i, r := range a.Audience {
		out[i] = string(r)
	}
	return out
}

// derefSize safely dereferences a *int64, returning 0 for nil. Used for the
// optional Size field on mcpsdk.ResourceLink.
func derefSize(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// validateStructuredContent performs the OPTIONAL defense-in-depth validation of
// a StructuredContent instance against the remote tool's advertised outputSchema.
// It returns a model-facing NOTE (sans trailing newline) on a validation failure,
// or "" when there is nothing to surface (no schema, validation passes, or the
// validator itself could not be set up — the latter is inert, never a hard
// error). The SDK client-side CallTool does NOT validate, so this is
// belt-and-suspenders against a misbehaving server.
func validateStructuredContent(schemaRaw json.RawMessage, instance any) string {
	if len(schemaRaw) == 0 {
		return ""
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		// A malformed schema is the server's problem; stay inert.
		return ""
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return ""
	}
	if err := resolved.Validate(instance); err != nil {
		// Suppress a warning when the failure is SOLELY a top-level type
		// mismatch between the instance's JSON type and the schema's declared
		// top-level type. The MCP spec's "must marshal to a JSON object" is a
		// SHOULD, not a hard contract; a valid JSON value of any shape is an
		// acceptable StructuredContent. A field-level violation (a real schema
		// breach inside an object instance) is NOT suppressed — those still
		// surface as warnings, because they are a misbehaving-server signal.
		declared := schema.Type
		declaredTypes := schema.Types
		if isTopLevelTypeMismatch(declared, declaredTypes, instance) {
			return ""
		}
		return fmt.Sprintf("[structured output validation warning: %v]", err)
	}
	return ""
}

// JSON type name constants (the JSON Schema "type" vocabulary), shared by the
// top-level type-mismatch classifier and the instance-type probe.
const (
	jsonTypeObject  = "object"
	jsonTypeArray   = "array"
	jsonTypeNumber  = "number"
	jsonTypeInteger = "integer"
	jsonTypeString  = "string"
	jsonTypeBoolean = "boolean"
	jsonTypeNull    = "null"
)

// isTopLevelTypeMismatch reports whether the instance's JSON type does not
// match ANY of the schema's declared top-level types — i.e. the validation
// failure is a top-level shape mismatch (array-against-object, etc.), NOT a
// field-level breach inside a matching shape. declared is schema.Type (a
// single type, "" when the schema used Types); declaredTypes is schema.Types
// (nil for a single-type schema). The two are mutually exclusive on
// jsonschema.Schema (see schema.go: "Use Type for a single type, or Types for
// multiple types; never both").
//
// integer is treated as a subtype of number (a JSON instance number that is
// integral matches a schema type of integer OR number), mirroring the
// validator's own subsumption rule. A schema with NO top-level type (both
// empty) cannot produce a top-level type mismatch, so this returns false and
// the original validation error surfaces.
func isTopLevelTypeMismatch(declared string, declaredTypes []string, instance any) bool {
	types := declaredTypes
	if len(types) == 0 && declared != "" {
		types = []string{declared}
	}
	if len(types) == 0 {
		// No top-level type declared — the validation failure is not a
		// top-level type mismatch; surface the original error.
		return false
	}
	got := jsonInstanceType(instance)
	if got == "" {
		return false
	}
	for _, t := range types {
		if t == got {
			return false
		}
		// "number" subsumes "integer": an integral instance matches a schema
		// type of number (mirrors the validator's own rule). The reverse is
		// NOT true: a non-integral number does not match an "integer" schema.
		if t == jsonTypeNumber && got == jsonTypeInteger {
			return false
		}
	}
	return true
}

// jsonInstanceType returns the JSON type name of instance ("object",
// "array", "number", "integer", "string", "boolean", "null"), or "" when the
// value is not a valid JSON instance. A json.RawMessage is unmarshaled first
// (the SDK may pass StructuredContent through raw). A float64 that is
// mathematically an integer is reported as "integer" so a schema type of
// "integer" can match it.
func jsonInstanceType(instance any) string {
	if raw, ok := instance.(json.RawMessage); ok {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return ""
		}
		instance = v
	}
	switch v := instance.(type) {
	case nil:
		return jsonTypeNull
	case bool:
		return jsonTypeBoolean
	case string:
		return jsonTypeString
	case map[string]any:
		return jsonTypeObject
	case []any:
		return jsonTypeArray
	case float64:
		if v == float64(int64(v)) {
			return jsonTypeInteger
		}
		return jsonTypeNumber
	case int:
		return jsonTypeInteger
	case int32:
		return jsonTypeInteger
	case int64:
		return jsonTypeInteger
	}
	return ""
}

// clampToolResultParts drops the block that tripped ValidateToolResultParts and
// returns a model-facing note describing the clamp, so an oversized block is
// surfaced rather than blackholing the whole result. It is best-effort: it
// retries validation after the drop and, if a second block still trips the cap,
// drops that too. A fully empty result is valid (the caller's Content string
// still carries the truncated summary). The returned note is appended to the
// (already-truncated) model string by the caller.
func clampToolResultParts(parts []session.Content, firstErr error) ([]session.Content, string) {
	note := fmt.Sprintf("[tool result: %v]", firstErr)
	// Drop the block whose index the error names (ValidateToolResultParts reports
	// "block[%d] …"); fall back to dropping the largest text/blob block if the
	// index is not recoverable.
	for {
		if err := session.ValidateToolResultParts(parts); err == nil {
			break
		} else {
			idx := offendingBlockIndex(err)
			if idx < 0 || idx >= len(parts) {
				// Drop the largest inline-bytes block as a last resort.
				idx = largestBlockIndex(parts)
				if idx < 0 {
					break
				}
			}
			parts = append(parts[:idx], parts[idx+1:]...)
		}
	}
	return parts, note
}

// offendingBlockIndex extracts the block index named in a ValidateToolResultParts
// error ("block[%d] …"). It returns -1 when the index cannot be recovered.
func offendingBlockIndex(err error) int {
	// Look for the first "%d" between "block[" and "]".
	s := err.Error()
	i := strings.Index(s, "block[")
	if i < 0 {
		return -1
	}
	rest := s[i+len("block["):]
	j := strings.IndexByte(rest, ']')
	if j < 0 {
		return -1
	}
	n := 0
	for _, r := range rest[:j] {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// largestBlockIndex returns the index of the block with the largest inline
// byte footprint (Text for text blocks, Data for media/embedded-resource), or
// -1 for an empty slice. It is the fallback for the oversized-block clamp.
func largestBlockIndex(parts []session.Content) int {
	if len(parts) == 0 {
		return -1
	}
	best, bestN := -1, -1
	for i, p := range parts {
		n := len(p.Text) + len(p.Data)
		if n > bestN {
			best, bestN = i, n
		}
	}
	return best
}

// isStructuredResult reports whether the MCP result is structured (JSON-shaped)
// and therefore must NOT be truncated (truncation would make it unparseable).
// Three signals, OR'd (any one ⇒ structured):
//  1. the remote tool advertised an outputSchema (outputSchema non-nil), OR
//  2. the remote result carried StructuredContent (res.StructuredContent != nil), OR
//  3. a content block is an EmbeddedResource whose MIME is application/json or
//     +json, or an EmbeddedResource/TextContent whose trimmed text starts with
//     '{' or '[' and parses as JSON.
//
// (mcpsdk.TextContent carries no MIME type, so for text blocks only the
// content-based parse applies.) Signal 3's JSON parse only runs when the result
// is already over-cap (the caller gates it), so the parse cost is justified only
// when needed.
func isStructuredResult(outputSchema json.RawMessage, res *mcpsdk.CallToolResult, parts []mcpsdk.Content) bool {
	// Signal 1: the remote tool advertised an output schema.
	if len(outputSchema) > 0 {
		return true
	}
	// Signal 2: the result carried StructuredContent.
	if res != nil && res.StructuredContent != nil {
		return true
	}
	// Signal 3: a content block is JSON by MIME or by content.
	for _, p := range parts {
		switch c := p.(type) {
		case *mcpsdk.TextContent:
			if textParsesAsJSON(c.Text) {
				return true
			}
		case *mcpsdk.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			if isJSONMIME(c.Resource.MIMEType) {
				return true
			}
			if c.Resource.Text != "" && textParsesAsJSON(c.Resource.Text) {
				return true
			}
		}
	}
	return false
}

// isJSONMIME reports whether mime is a JSON media type: "application/json" or
// any "application/…+json" (or "text/json" — a common variant). An empty mime
// is not JSON.
func isJSONMIME(mime string) bool {
	if mime == "" {
		return false
	}
	switch m := strings.ToLower(strings.TrimSpace(mime)); {
	case m == "application/json":
		return true
	case m == "text/json":
		return true
	case strings.HasPrefix(m, "application/") && strings.HasSuffix(m, "+json"):
		return true
	}
	return false
}

// textParsesAsJSON reports whether the trimmed text starts with '{' or '[' and
// unmarshals as JSON. It is the content-based fallback for a JSON result that
// arrives as a bare TextContent with no MIME and no outputSchema.
func textParsesAsJSON(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if t[0] != '{' && t[0] != '[' {
		return false
	}
	var v any
	return json.Unmarshal([]byte(t), &v) == nil
}

// structuredTooLargeError is the fail-closed message a structured (JSON) result
// over the output cap surfaces to the model. It names the two escape hatches
// (narrow/paginate the remote call, or run it through CallMcpWithQuery with a
// jq filter) so the model has an actionable recovery path.
func structuredTooLargeError(toolName, serverName string) string {
	return fmt.Sprintf(
		"mcp tool %q (server %q) result exceeded the %d-byte output cap and is structured (JSON). "+
			"Truncating it would make it unparseable, so it was NOT returned. "+
			"To get the data, either (1) narrow/paginate the call using the remote tool's own filter/pagination parameters, "+
			"or (2) call it through CallMcpWithQuery with a jq filter to extract only the fields you need (no disk required).",
		toolName, serverName, toolkit.MaxOutputBytes)
}
