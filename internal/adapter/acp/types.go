package acp

import "encoding/json"

// This file defines the ACP wire types this adapter (de)serializes. They are the
// adapter's OWN JSON shapes — deliberately NOT the proto contract (contracts/gen)
// — mirroring the relevant subset of the ACP schema
// (github.com/zed-industries/agent-client-protocol). Only the fields this adapter uses
// are modelled; unmodelled fields are simply not emitted (ACP treats absent optional
// fields as defaults).

// protocolVersion is the ACP protocol version this adapter implements. ACP bumps
// it only for breaking changes; capabilities cover the rest.
const protocolVersion = 1

// ACP JSON-RPC method names.
const (
	methodInitialize        = "initialize"
	methodSessionNew        = "session/new"
	methodSessionPrompt     = "session/prompt"
	methodSessionCancel     = "session/cancel"
	methodSessionUpdate     = "session/update"             // agent -> client notification
	methodRequestPermission = "session/request_permission" // agent -> client request
	methodSessionSetMode    = "session/set_mode"           // client -> agent request
	methodSessionLoad       = "session/load"               // client -> agent request
	methodSessionClose      = "session/close"              // client -> agent request
	methodFSReadTextFile    = "fs/read_text_file"          // agent -> client request
	methodFSWriteTextFile   = "fs/write_text_file"         // agent -> client request
)

// --- initialize --------------------------------------------------------------

type initializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         *implementation    `json:"clientInfo,omitempty"`
}

// clientCapabilities is the subset of the ACP ClientCapabilities the agent
// consults. The ACP schema nests the filesystem methods the client implements
// under "fs": clientCapabilities.fs.readTextFile / .writeTextFile. mecatl reads
// these to decide whether to DELEGATE file Read/Write through the editor's
// buffers (fs/read_text_file, fs/write_text_file) instead of touching disk
// directly. Unmodelled capability fields (e.g. terminal) decode to their zero
// value and are ignored. An absent clientCapabilities (a non-conformant or
// minimal client) leaves FS all-false, so the agent falls back to osfs.
type clientCapabilities struct {
	FS fsCapabilities `json:"fs"`
}

// fsCapabilities advertises which fs/* methods the CLIENT implements. The agent
// delegates file I/O only when BOTH are true (read-only delegation would read
// buffers but write disk, re-introducing the very divergence the delegation
// fixes); otherwise it uses its own osfs workspace.
type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type initializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AuthMethods       []any             `json:"authMethods"`
	AgentInfo         *implementation   `json:"agentInfo,omitempty"`
}

type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type agentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	McpCapabilities     mcpCapabilities     `json:"mcpCapabilities"`
	PromptCapabilities  promptCapabilities  `json:"promptCapabilities"`
	SessionCapabilities sessionCapabilities `json:"sessionCapabilities"`
}

// sessionCapabilities advertises the session-lifecycle requests the agent
// supports. The ACP schema (protocolVersion 1) nests them under
// agentCapabilities.sessionCapabilities as booleans — confirmed against the
// canonical schema $defs/SessionCapabilities
// (raw.githubusercontent.com/zed-industries/agent-client-protocol/main/schema/schema.json),
// which defines close / list / resume. mecatl advertises only "close"
// (session/close); list and resume are not modelled (absent → their false
// default), so they are simply not emitted.
type sessionCapabilities struct {
	Close bool `json:"close"`
}

// mcpCapabilities advertises which MCP transports the agent accepts from the
// client's session/new mcpServers. mecatl is streaming-HTTP MCP ONLY (see
// CLAUDE.md: "No stdio MCP, ever"), so http is the only transport we accept; an
// http client server is mounted per-session (see session/new handling). sse is
// false (the SSE transport is not supported), and a stdio (command-shaped) entry
// is hard-rejected — mecatl never spawns an MCP server process.
type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type promptCapabilities struct {
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
	Image           bool `json:"image"`
}

// --- session/new -------------------------------------------------------------

type newSessionRequest struct {
	Cwd        string          `json:"cwd"`
	McpServers []mcpServer     `json:"mcpServers"`
	Execution  json.RawMessage `json:"execution"`
}

// mcpServer is a client-provided MCP server entry. We model the transport
// discriminant fields (type/command/url) enough to classify each entry:
// stdio (command-shaped) and sse are rejected; an http entry is accepted and
// mounted per-session, carrying its optional auth Headers.
type mcpServer struct {
	Name    string          `json:"name"`
	Command string          `json:"command,omitempty"`
	URL     string          `json:"url,omitempty"`
	Type    string          `json:"type,omitempty"`
	Headers []mcpHeader     `json:"headers,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// mcpHeader is one HTTP header (name/value) the client supplies for an http MCP
// server — typically an Authorization header. It mirrors the ACP HttpHeader
// shape (a {name,value} object), distinct from a JSON map.
type mcpHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type newSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *sessionModeState `json:"modes,omitempty"`
}

// sessionModeState reflects mecatl's permission modes (default/plan/acceptEdits)
// to the client, advertising the available modes and the session's CURRENT mode.
// It is returned on session/new and session/load so the editor's mode picker is
// seeded with the right selection; session/set_mode then switches between them.
type sessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []sessionMode `json:"availableModes"`
}

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// --- session/prompt ----------------------------------------------------------

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResponse struct {
	StopReason string `json:"stopReason"`
}

// ACP stop reasons (PromptResponse.stopReason).
const (
	stopEndTurn         = "end_turn"
	stopMaxTokens       = "max_tokens"
	stopMaxTurnRequests = "max_turn_requests"
	stopRefusal         = "refusal"
	stopCancelled       = "cancelled"
)

// --- session/cancel ----------------------------------------------------------

type cancelNotification struct {
	SessionID string `json:"sessionId"`
}

// --- session/set_mode (client -> agent) --------------------------------------

// setModeRequest is the inbound session/set_mode params: switch the session to
// the named modeId (one of the availableModes' ids advertised on session/new).
type setModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// setModeResponse is the (empty) session/set_mode result. ACP models it as an
// object with only an optional _meta, so an empty struct serializes to "{}".
type setModeResponse struct{}

// --- session/load (client -> agent) ------------------------------------------

// loadSessionRequest is the inbound session/load params: resume the persisted
// session under sessionId, rooted at cwd. mcpServers mirrors session/new and is
// rejected the same way (mecatl connects only its own streaming-HTTP MCP).
type loadSessionRequest struct {
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd"`
	McpServers []mcpServer     `json:"mcpServers"`
	Execution  json.RawMessage `json:"execution"`
}

// loadSessionResponse echoes the resumed session's mode state so the editor can
// seed its mode picker, mirroring session/new. configOptions is omitted.
type loadSessionResponse struct {
	Modes *sessionModeState `json:"modes,omitempty"`
}

// --- session/close (client -> agent) -----------------------------------------

// closeSessionRequest is the inbound session/close params: cancel any ongoing
// work for sessionId (as session/cancel would) and free the session's
// resources. It is the spec's first-class per-session-end hook (the mid-session
// twin of the gRPC CloseSession RPC / HTTP DELETE), capability-gated by
// sessionCapabilities.close.
type closeSessionRequest struct {
	SessionID string `json:"sessionId"`
}

// closeSessionResponse is the (empty) session/close result. ACP models it as an
// object with only an optional _meta, so an empty struct serializes to "{}" —
// symmetric with setModeResponse{}.
type closeSessionResponse struct{}

// --- content blocks ----------------------------------------------------------

// contentBlock is the ACP ContentBlock. It models the variants mecatl consumes
// on an inbound session/prompt: text, image, audio, resource (an embedded
// resource carrying inline text or a binary blob), and resource_link (a URI
// reference). The variants share one struct — a Type discriminant plus the union
// of their fields, each omitempty — because Go cannot express a JSON sum type on
// one tag; buildPromptContent dispatches on Type and reads only that variant's
// fields. The media fields mirror the ACP schema's camelCase names:
//
//   - text          → Text
//   - image / audio → Data (base64) XOR URI, plus MimeType
//   - resource      → Resource (embedded contents: text XOR blob+mimeType)
//   - resource_link → URI (a reference mecatl cannot fetch — rejected loudly)
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Data is the base64-encoded inline bytes of an image/audio block. ACP carries
	// media inline as base64 in "data" alongside "mimeType".
	Data string `json:"data,omitempty"`
	// MimeType is the IANA media type of an image/audio block (e.g. "image/png").
	MimeType string `json:"mimeType,omitempty"`
	// URI is the reference on a resource_link block (and the optional URI on an
	// image/audio block sourced from a URL rather than inline bytes).
	URI string `json:"uri,omitempty"`
	// Resource is the embedded resource contents on a "resource" block.
	Resource *resourceContents `json:"resource,omitempty"`
}

// resourceContents is the embedded payload of an ACP "resource" ContentBlock. The
// ACP EmbeddedResourceResource is itself a union: a TEXT resource carries Text (+
// MimeType), a BINARY resource carries Blob (base64) + MimeType; both carry the
// source Uri. mecatl flattens an inline-text resource into the prompt text and
// treats a blob resource as image/audio per its MimeType (see buildPromptContent).
type resourceContents struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// Text is set on a text resource (the inline text contents).
	Text string `json:"text,omitempty"`
	// Blob is the base64-encoded bytes of a binary resource.
	Blob string `json:"blob,omitempty"`
}

// textBlock constructs a text ContentBlock for an outbound chunk.
func textBlock(text string) contentBlock { return contentBlock{Type: "text", Text: text} }

// --- session/update (agent -> client) ----------------------------------------

// sessionNotification is the session/update params envelope. Update holds one of
// the union variants (chunkUpdate or toolCallUpdate) — the variants carry
// DIFFERENT "content" shapes (a single ContentBlock for the *_chunk variants vs a
// []ToolCallContent for the tool_call variants), which a single Go struct cannot
// express on one JSON tag, so Update is `any` and the projector supplies the
// correct purpose-built variant value (each marshals the right shape).
type sessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// session/update "sessionUpdate" discriminator const values.
const (
	updateAgentMessageChunk = "agent_message_chunk"
	updateAgentThoughtChunk = "agent_thought_chunk"
	updateToolCall          = "tool_call"
	updateToolCallUpdate    = "tool_call_update"
	updateAvailableCommands = "available_commands_update"
	updateCurrentMode       = "current_mode_update"
)

// availableCommandsUpdate is the available_commands_update session/update
// variant: it lists the slash commands the editor should offer in its input
// palette. An empty list clears the palette.
type availableCommandsUpdate struct {
	SessionUpdate     string             `json:"sessionUpdate"`
	AvailableCommands []availableCommand `json:"availableCommands"`
}

// availableCommand is one ACP AvailableCommand (name + human description). The
// input.hint field is omitted — mecatl commands take free-form text, so there is
// no structured input schema to advertise this phase.
type availableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// currentModeUpdate is the current_mode_update session/update variant: it tells
// the editor the session's mode changed (e.g. after session/set_mode took
// effect) so its mode picker reflects the new selection.
type currentModeUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	CurrentModeID string `json:"currentModeId"`
}

// chunkUpdate is the *_chunk session/update variant (a single ContentBlock).
type chunkUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       contentBlock `json:"content"`
}

// toolCallUpdate is the tool_call / tool_call_update session/update variant. It
// is also the shape embedded in a request_permission's toolCall field.
type toolCallUpdate struct {
	SessionUpdate string            `json:"sessionUpdate,omitempty"`
	ToolCallID    string            `json:"toolCallId"`
	Title         string            `json:"title,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Status        string            `json:"status,omitempty"`
	RawInput      json.RawMessage   `json:"rawInput,omitempty"`
	Content       []toolCallContent `json:"content,omitempty"`
}

// ACP ToolCallStatus values.
const (
	toolStatusPending    = "pending"
	toolStatusInProgress = "in_progress"
	toolStatusCompleted  = "completed"
	toolStatusFailed     = "failed"
)

// toolCallContent is the ToolCallContent union. Two variants are emitted:
//   - the "content" variant wraps a text ContentBlock (results, progress lines);
//   - the "diff" variant carries a file path + old/new text so the editor renders
//     a native inline diff for an Edit/Write.
//
// The two variants carry mutually-exclusive fields, so the unused ones are
// omitempty and a single struct expresses both. (terminal is still deferred.)
type toolCallContent struct {
	Type string `json:"type"`
	// Content is set on the "content" variant only.
	Content *contentBlock `json:"content,omitempty"`
	// Path/OldText/NewText are set on the "diff" variant only. OldText is a pointer
	// so it is OMITTED for a new/overwritten file (ACP: absent oldText means the
	// file did not exist / is fully replaced) rather than serialized as "".
	Path    string  `json:"path,omitempty"`
	OldText *string `json:"oldText,omitempty"`
	NewText string  `json:"newText,omitempty"`
}

// textToolContent wraps result text as a single ToolCallContent of the content
// variant.
func textToolContent(text string) []toolCallContent {
	cb := textBlock(text)
	return []toolCallContent{{Type: "content", Content: &cb}}
}

// diffToolContent builds a single "diff" ToolCallContent. oldText is a pointer so
// the caller can omit it (nil) for a new/overwritten file.
func diffToolContent(path string, oldText *string, newText string) []toolCallContent {
	return []toolCallContent{{Type: "diff", Path: path, OldText: oldText, NewText: newText}}
}

// --- session/request_permission (agent -> client) ----------------------------

type requestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  toolCallUpdate     `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// ACP PermissionOptionKind values.
const (
	permAllowOnce    = "allow_once"
	permAllowAlways  = "allow_always"
	permRejectOnce   = "reject_once"
	permRejectAlways = "reject_always"
)

type requestPermissionResponse struct {
	Outcome permissionOutcome `json:"outcome"`
}

// permissionOutcome is the discriminated outcome (discriminator: "outcome").
// For "selected" OptionID names the chosen option; for "cancelled" it is empty.
type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

const (
	outcomeSelected  = "selected"
	outcomeCancelled = "cancelled"
)

// --- fs/read_text_file, fs/write_text_file (agent -> client) -----------------
//
// These are the outbound filesystem-delegation requests: when the client
// advertised fs.readTextFile && fs.writeTextFile, the agent's per-session
// workspace routes file Read/Write through the editor (so edits flow through its
// in-memory buffers, including unsaved changes) instead of touching disk. The
// path is ABSOLUTE — the ACP fs/* contract addresses real files — and the agent
// confines a session-relative tool path under the session root BEFORE issuing
// the call (the editor is trusted, the model is not). See fsworkspace.go.

// fsReadTextFileRequest is the fs/read_text_file params. line/limit are omitted
// (the agent reads whole-file; the Read tool does its own line slicing), so the
// pointers are nil and elided.
type fsReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

// fsReadTextFileResponse is the fs/read_text_file result: the file's text
// content (the editor's buffer view).
type fsReadTextFileResponse struct {
	Content string `json:"content"`
}

// fsWriteTextFileRequest is the fs/write_text_file params: replace the file's
// content via the editor. The response is empty/null.
type fsWriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}
