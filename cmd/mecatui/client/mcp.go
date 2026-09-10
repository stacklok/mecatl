package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The MCP/ToolHive surface: plain client-owned structs mirroring the Stage C
// proto messages, the unary RPC wrappers that map proto → these structs, and the
// tea.Cmd constructors the ui calls. As with the Event mapper, NO proto type
// leaks past this file: the ui renders purely from the structs and the classified
// error msgs below. Everything here is unit-tested against a fake
// HarnessServiceClient (see mcp_test.go) so the mapping + error classification
// run offline with no network.

// MCPResource is one resource a server exposes (proto McpResource, proto-free).
type MCPResource struct {
	Server      string
	URI         string
	Name        string
	Title       string
	Description string
	MimeType    string
	Size        int64
	ReadOnly    bool
}

// MCPResourceContents is one chunk of a read resource (proto McpResourceContents).
// Blob is the raw bytes when the resource is binary (Text empty).
type MCPResourceContents struct {
	URI      string
	MimeType string
	Text     string
	Blob     []byte
}

// MCPPrompt is one prompt template a server exposes (proto McpPrompt).
type MCPPrompt struct {
	Server      string
	Name        string
	Title       string
	Description string
	Arguments   []MCPPromptArgument
}

// MCPPromptArgument is one templated argument of a prompt (proto McpPromptArgument).
type MCPPromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

// MCPPromptMessage is one rendered message of a got prompt (proto McpPromptMessage).
type MCPPromptMessage struct {
	Role string
	Text string
}

// MCPServerInfo is one MCP server within a source (proto McpServerInfo).
type MCPServerInfo struct {
	Name      string
	URL       string
	Transport string
	Group     string
}

// MCPSource is one inventory source — a ToolHive group or config block — with its
// servers and any diagnostics (proto McpSource). NOTE: ListMcpSources reflects a
// startup snapshot; servers started AFTER mecated launched won't appear.
type MCPSource struct {
	Name        string
	Kind        string
	Enabled     bool
	Group       string
	Servers     []MCPServerInfo
	Diagnostics []string
}

// MCPErrorClass classifies an MCP RPC failure so the ui can render it
// differently. It is derived from the gRPC status code per the Stage C contract:
// InvalidArgument → input, FailedPrecondition → not configured, everything else
// (Internal, Unavailable, …) → a server-side fault.
type MCPErrorClass int

const (
	// MCPErrInput is a client input error: an unknown server name or a missing
	// required field. Surfaced as "fix input". (gRPC InvalidArgument.)
	MCPErrInput MCPErrorClass = iota
	// MCPErrServer is a downstream/transport fault, or an unknown URI/prompt.
	// Surfaced as "server-side problem". (gRPC Internal / Unavailable / other.)
	MCPErrServer
	// MCPErrNotConfigured means no MCP provider is configured on mecated.
	// Surfaced as "MCP not configured". (gRPC FailedPrecondition.)
	MCPErrNotConfigured
)

// String renders the class as a short, stable label (used in the ui copy).
func (c MCPErrorClass) String() string {
	switch c {
	case MCPErrInput:
		return "fix input"
	case MCPErrNotConfigured:
		return "MCP not configured"
	default:
		return "server-side problem"
	}
}

// classifyMCPErr maps a gRPC error to an MCPErrorClass. A nil error must never be
// passed here. Non-status errors (e.g. a raw transport failure) classify as
// server-side, the safe default.
func classifyMCPErr(err error) MCPErrorClass {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return MCPErrInput
	case codes.FailedPrecondition:
		return MCPErrNotConfigured
	default:
		return MCPErrServer
	}
}

// MCP result/error msgs. Each RPC has a success msg carrying the plain structs and
// shares the classified MCPErrMsg on failure. The Op field on MCPErrMsg lets the
// ui say which action failed without coupling to the RPC machinery.

// MCPResourcesMsg carries a ListMcpResources success.
type MCPResourcesMsg struct {
	Server    string // the requested server filter ("" = all)
	Resources []MCPResource
}

// MCPResourceReadMsg carries a ReadMcpResource success.
type MCPResourceReadMsg struct {
	Server   string
	URI      string
	Contents []MCPResourceContents
}

// MCPPromptsMsg carries a ListMcpPrompts success.
type MCPPromptsMsg struct {
	Server  string
	Prompts []MCPPrompt
}

// MCPPromptGotMsg carries a GetMcpPrompt success.
type MCPPromptGotMsg struct {
	Server      string
	Name        string
	Description string
	Messages    []MCPPromptMessage
}

// MCPSourcesMsg carries a ListMcpSources success (the panel inventory).
type MCPSourcesMsg struct {
	Sources []MCPSource
}

// MCPGroupsMsg carries a ListToolHiveGroups success.
type MCPGroupsMsg struct {
	Groups []string
}

// MCPConnectorStatus is one broker-local publication row. It deliberately has no
// health, credential, routing, or prompt-readiness fields.
type MCPConnectorStatus struct {
	Name           string
	CatalogueState string
	ToolCount      uint32
}

// MCPConnectorInventory is the proto-free broker-local inventory response.
type MCPConnectorInventory struct {
	Availability    string
	EnrollmentState string
	Connectors      []MCPConnectorStatus
	TotalConnectors uint32
	Truncated       bool
}

// MCPConnectorStatusMsg binds a broker response to the requesting UI session and
// refresh generation so a late command cannot overwrite newer state.
type MCPConnectorStatusMsg struct {
	SessionID  string
	Generation uint64
	Inventory  MCPConnectorInventory
}

// MCPErrMsg is the classified failure msg shared by all MCP RPCs. Op names the
// action ("list resources", "read resource", …) for the ui; Class drives the
// distinct rendering (input vs server vs not-configured); Err is the raw error
// for detail.
type MCPErrMsg struct {
	Op    string
	Class MCPErrorClass
	Err   error
}

// Error satisfies error so MCPErrMsg can be logged/compared directly.
func (e MCPErrMsg) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Class, e.Err)
}

// --- RPC wrappers (proto → plain structs) -----------------------------------

// ListMCPResources lists resources, optionally filtered to one server ("" = all).
func (c *Client) ListMCPResources(ctx context.Context, server string) ([]MCPResource, error) {
	resp, err := c.svc.ListMcpResources(ctx, &mecatlv1.ListMcpResourcesRequest{Server: server})
	if err != nil {
		return nil, err
	}
	return mapResources(resp.GetResources()), nil
}

// ReadMCPResource reads one resource by (server, uri).
func (c *Client) ReadMCPResource(ctx context.Context, server, uri string) ([]MCPResourceContents, error) {
	resp, err := c.svc.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: server, Uri: uri})
	if err != nil {
		return nil, err
	}
	return mapContents(resp.GetContents()), nil
}

// ListMCPPrompts lists prompts, optionally filtered to one server ("" = all).
func (c *Client) ListMCPPrompts(ctx context.Context, server string) ([]MCPPrompt, error) {
	resp, err := c.svc.ListMcpPrompts(ctx, &mecatlv1.ListMcpPromptsRequest{Server: server})
	if err != nil {
		return nil, err
	}
	return mapPrompts(resp.GetPrompts()), nil
}

// GetMCPPrompt renders one prompt by (server, name) with the given arguments.
func (c *Client) GetMCPPrompt(ctx context.Context, server, name string, args map[string]string) (string, []MCPPromptMessage, error) {
	resp, err := c.svc.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: server, Name: name, Arguments: args})
	if err != nil {
		return "", nil, err
	}
	return resp.GetDescription(), mapPromptMessages(resp.GetMessages()), nil
}

// ListMCPSources lists the inventory sources (the panel snapshot).
func (c *Client) ListMCPSources(ctx context.Context) ([]MCPSource, error) {
	resp, err := c.svc.ListMcpSources(ctx, &mecatlv1.ListMcpSourcesRequest{})
	if err != nil {
		return nil, err
	}
	return mapSources(resp.GetSources()), nil
}

// ListToolHiveGroups lists the configured ToolHive group names.
func (c *Client) ListToolHiveGroups(ctx context.Context) ([]string, error) {
	resp, err := c.svc.ListToolHiveGroups(ctx, &mecatlv1.ListToolHiveGroupsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetGroups(), nil
}

// ListMCPConnectors reads the owned session's broker-local catalogue without
// probing, enrolling, or changing session state.
func (c *Client) ListMCPConnectors(ctx context.Context, sessionID string) (MCPConnectorInventory, error) {
	resp, err := c.svc.ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: sessionID})
	if err != nil {
		return MCPConnectorInventory{}, err
	}
	out := MCPConnectorInventory{Availability: resp.GetAvailability(), EnrollmentState: resp.GetEnrollmentState(), TotalConnectors: resp.GetTotalConnectors(), Truncated: resp.GetTruncated(), Connectors: make([]MCPConnectorStatus, 0, len(resp.GetConnectors()))}
	for _, row := range resp.GetConnectors() {
		out.Connectors = append(out.Connectors, MCPConnectorStatus{Name: row.GetName(), CatalogueState: row.GetCatalogueState(), ToolCount: row.GetToolCount()})
	}
	return out, nil
}

// --- proto → plain mappers (nil-safe) ----------------------------------------

func mapResources(in []*mecatlv1.McpResource) []MCPResource {
	out := make([]MCPResource, 0, len(in))
	for _, r := range in {
		out = append(out, MCPResource{
			Server:      r.GetServer(),
			URI:         r.GetUri(),
			Name:        r.GetName(),
			Title:       r.GetTitle(),
			Description: r.GetDescription(),
			MimeType:    r.GetMimeType(),
			Size:        r.GetSize(),
			ReadOnly:    r.GetReadOnly(),
		})
	}
	return out
}

func mapContents(in []*mecatlv1.McpResourceContents) []MCPResourceContents {
	out := make([]MCPResourceContents, 0, len(in))
	for _, c := range in {
		out = append(out, MCPResourceContents{
			URI:      c.GetUri(),
			MimeType: c.GetMimeType(),
			Text:     c.GetText(),
			Blob:     c.GetBlob(),
		})
	}
	return out
}

func mapPrompts(in []*mecatlv1.McpPrompt) []MCPPrompt {
	out := make([]MCPPrompt, 0, len(in))
	for _, p := range in {
		out = append(out, MCPPrompt{
			Server:      p.GetServer(),
			Name:        p.GetName(),
			Title:       p.GetTitle(),
			Description: p.GetDescription(),
			Arguments:   mapPromptArgs(p.GetArguments()),
		})
	}
	return out
}

func mapPromptArgs(in []*mecatlv1.McpPromptArgument) []MCPPromptArgument {
	out := make([]MCPPromptArgument, 0, len(in))
	for _, a := range in {
		out = append(out, MCPPromptArgument{
			Name:        a.GetName(),
			Title:       a.GetTitle(),
			Description: a.GetDescription(),
			Required:    a.GetRequired(),
		})
	}
	return out
}

func mapPromptMessages(in []*mecatlv1.McpPromptMessage) []MCPPromptMessage {
	out := make([]MCPPromptMessage, 0, len(in))
	for _, m := range in {
		out = append(out, MCPPromptMessage{Role: m.GetRole(), Text: m.GetText()})
	}
	return out
}

func mapServers(in []*mecatlv1.McpServerInfo) []MCPServerInfo {
	out := make([]MCPServerInfo, 0, len(in))
	for _, s := range in {
		out = append(out, MCPServerInfo{
			Name:      s.GetName(),
			URL:       s.GetUrl(),
			Transport: s.GetTransport(),
			Group:     s.GetGroup(),
		})
	}
	return out
}

func mapSources(in []*mecatlv1.McpSource) []MCPSource {
	out := make([]MCPSource, 0, len(in))
	for _, s := range in {
		out = append(out, MCPSource{
			Name:        s.GetName(),
			Kind:        s.GetKind(),
			Enabled:     s.GetEnabled(),
			Group:       s.GetGroup(),
			Servers:     mapServers(s.GetServers()),
			Diagnostics: s.GetDiagnostics(),
		})
	}
	return out
}

// --- tea.Cmd constructors (the ui's entry points) ----------------------------
//
// Each runs its RPC off the update goroutine and returns either the success msg
// or a classified MCPErrMsg. They take an MCP interface (not *Client) so the ui
// can be driven offline by a fake; *Client satisfies it.

// MCP is the subset of *Client the ui's MCP commands need. Splitting it out keeps
// the ui injectable with a fake for offline golden tests.
type MCP interface {
	ListMCPResources(ctx context.Context, server string) ([]MCPResource, error)
	ReadMCPResource(ctx context.Context, server, uri string) ([]MCPResourceContents, error)
	ListMCPPrompts(ctx context.Context, server string) ([]MCPPrompt, error)
	GetMCPPrompt(ctx context.Context, server, name string, args map[string]string) (string, []MCPPromptMessage, error)
	ListMCPSources(ctx context.Context) ([]MCPSource, error)
	ListToolHiveGroups(ctx context.Context) ([]string, error)
}

// MCPConnectorReader is optional so older/direct-only MCP collaborators retain
// their existing surface while broker-only mode has no direct-RPC fallback.
type MCPConnectorReader interface {
	ListMCPConnectors(ctx context.Context, sessionID string) (MCPConnectorInventory, error)
}

// ListMcpResourcesCmd lists resources (server "" = all).
func ListMcpResourcesCmd(ctx context.Context, m MCP, server string) tea.Cmd {
	return func() tea.Msg {
		res, err := m.ListMCPResources(ctx, server)
		if err != nil {
			return MCPErrMsg{Op: "list resources", Class: classifyMCPErr(err), Err: err}
		}
		return MCPResourcesMsg{Server: server, Resources: res}
	}
}

// ReadMcpResourceCmd reads one resource by (server, uri).
func ReadMcpResourceCmd(ctx context.Context, m MCP, server, uri string) tea.Cmd {
	return func() tea.Msg {
		c, err := m.ReadMCPResource(ctx, server, uri)
		if err != nil {
			return MCPErrMsg{Op: "read resource", Class: classifyMCPErr(err), Err: err}
		}
		return MCPResourceReadMsg{Server: server, URI: uri, Contents: c}
	}
}

// ListMcpPromptsCmd lists prompts (server "" = all).
func ListMcpPromptsCmd(ctx context.Context, m MCP, server string) tea.Cmd {
	return func() tea.Msg {
		p, err := m.ListMCPPrompts(ctx, server)
		if err != nil {
			return MCPErrMsg{Op: "list prompts", Class: classifyMCPErr(err), Err: err}
		}
		return MCPPromptsMsg{Server: server, Prompts: p}
	}
}

// GetMcpPromptCmd renders one prompt by (server, name) with arguments.
func GetMcpPromptCmd(ctx context.Context, m MCP, server, name string, args map[string]string) tea.Cmd {
	return func() tea.Msg {
		desc, msgs, err := m.GetMCPPrompt(ctx, server, name, args)
		if err != nil {
			return MCPErrMsg{Op: "get prompt", Class: classifyMCPErr(err), Err: err}
		}
		return MCPPromptGotMsg{Server: server, Name: name, Description: desc, Messages: msgs}
	}
}

// ListMcpSourcesCmd lists the inventory sources (the panel snapshot).
func ListMcpSourcesCmd(ctx context.Context, m MCP) tea.Cmd {
	return func() tea.Msg {
		s, err := m.ListMCPSources(ctx)
		if err != nil {
			return MCPErrMsg{Op: "list sources", Class: classifyMCPErr(err), Err: err}
		}
		return MCPSourcesMsg{Sources: s}
	}
}

// ListToolHiveGroupsCmd lists the configured ToolHive groups.
func ListToolHiveGroupsCmd(ctx context.Context, m MCP) tea.Cmd {
	return func() tea.Msg {
		g, err := m.ListToolHiveGroups(ctx)
		if err != nil {
			return MCPErrMsg{Op: "list groups", Class: classifyMCPErr(err), Err: err}
		}
		return MCPGroupsMsg{Groups: g}
	}
}

// ListMCPConnectorsCmd reads broker-local inventory. The identity fields are
// echoed even on failure by the UI-owned command generation, never the server.
func ListMCPConnectorsCmd(ctx context.Context, m MCPConnectorReader, sessionID string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		inventory, err := m.ListMCPConnectors(ctx, sessionID)
		if err != nil {
			return MCPErrMsg{Op: "list broker catalogue", Class: classifyMCPErr(err), Err: err}
		}
		return MCPConnectorStatusMsg{SessionID: sessionID, Generation: generation, Inventory: inventory}
	}
}
