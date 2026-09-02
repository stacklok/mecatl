package client

import (
	"context"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The slash-command discovery surface: a plain client-owned struct mirroring the
// proto Command message, the unary RPC wrapper that maps proto → the struct, and
// the tea.Cmd constructor the ui's palette calls. As with the MCP surface, NO
// proto type leaks past this file — the ui renders purely from the structs and
// msgs below, and the mapping is exercised offline against a fake client.

// Command is one discovered slash command (proto Command, proto-free): its
// invocation name (without the leading "/") and a short description. Builtin
// marks a CLIENT-SIDE command (e.g. /clear, /help) injected by the ui rather
// than discovered from the server; it stays false for every server row (the
// zero value). The ui uses it to dispatch the row to a Model action instead of
// expanding it server-side, and to win name collisions with workspace commands.
type Command struct {
	Name        string
	Description string
	Builtin     bool
}

// CommandsMsg carries a ListCommands success (the palette's command set). Err is
// set on failure; the palette degrades quietly to "no commands" rather than
// surfacing an error chrome, so the field is informational.
type CommandsMsg struct {
	Commands []Command
	Err      error
}

// ListCommands lists the available slash commands for workspace ("" => empty).
func (c *Client) ListCommands(ctx context.Context, sessionID string) ([]Command, error) {
	resp, err := c.svc.ListCommands(ctx, &mecatlv1.ListCommandsRequest{SessionId: sessionID})
	if err != nil {
		return nil, err
	}
	return mapCommands(resp.GetCommands()), nil
}

// mapCommands maps proto Commands to the plain structs (nil-safe).
func mapCommands(in []*mecatlv1.Command) []Command {
	out := make([]Command, 0, len(in))
	for _, c := range in {
		out = append(out, Command{Name: c.GetName(), Description: c.GetDescription()})
	}
	return out
}

// Commander is the subset of *Client the ui's command palette needs. Splitting
// it out keeps the ui injectable with a fake for offline tests; *Client
// satisfies it.
type Commander interface {
	ListCommands(ctx context.Context, workspace string) ([]Command, error)
}

// ListCommandsCmd fetches the slash commands for workspace off the update
// goroutine; the result (success or error) arrives as a CommandsMsg.
func ListCommandsCmd(ctx context.Context, c Commander, workspace string) tea.Cmd {
	return func() tea.Msg {
		cmds, err := c.ListCommands(ctx, workspace)
		if err != nil {
			return CommandsMsg{Err: err}
		}
		return CommandsMsg{Commands: cmds}
	}
}
