package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// Prompt is the adapter's value-object view of a prompt template advertised by a
// remote MCP server. Like Resource it carries no SDK types so non-adapter layers
// (and a later gRPC stage) can consume it freely. Server records the owning
// connected server.
type Prompt struct {
	Server      string
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
}

// PromptArgument describes a single templating argument a prompt accepts.
type PromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

// PromptResult is the adapter's view of an expanded prompt: its optional
// description plus the rendered messages.
type PromptResult struct {
	Description string
	Messages    []PromptMessage
}

// PromptMessage is one role-tagged message of an expanded prompt. Role is the
// MCP role string ("user"/"assistant"); Text is the flattened textual content.
type PromptMessage struct {
	Role string
	Text string
}

// promptFromSDK translates an SDK *Prompt into the adapter value object, tagging
// it with the owning server. A nil input yields the zero value.
func promptFromSDK(server string, p *mcpsdk.Prompt) Prompt {
	if p == nil {
		return Prompt{Server: server}
	}
	args := make([]PromptArgument, 0, len(p.Arguments))
	for _, a := range p.Arguments {
		if a == nil {
			continue
		}
		args = append(args, PromptArgument{
			Name:        a.Name,
			Title:       a.Title,
			Description: a.Description,
			Required:    a.Required,
		})
	}
	return Prompt{
		Server:      server,
		Name:        p.Name,
		Title:       p.Title,
		Description: p.Description,
		Arguments:   args,
	}
}

// promptResultFromSDK translates an SDK *GetPromptResult into the adapter value
// object, flattening each message's content to text (role preserved).
func promptResultFromSDK(res *mcpsdk.GetPromptResult) PromptResult {
	if res == nil {
		return PromptResult{}
	}
	msgs := make([]PromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		if m == nil {
			continue
		}
		msgs = append(msgs, PromptMessage{
			Role: string(m.Role),
			Text: flattenContentModel([]mcpsdk.Content{m.Content}),
		})
	}
	return PromptResult{Description: res.Description, Messages: msgs}
}

// flattenPromptResult renders an expanded prompt into a single model-facing
// string. Each message is prefixed with its role (e.g. "user: ...") so a
// multi-message prompt keeps its turn structure when injected as one prompt.
// The whole thing is truncated to toolkit.MaxOutputBytes.
func flattenPromptResult(res PromptResult) string {
	var b strings.Builder
	for i, m := range res.Messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&b, "%s: %s", role, m.Text)
	}
	return toolkit.Truncate(b.String(), toolkit.MaxOutputBytes)
}

// listPrompts pages through the server's prompts and returns them as adapter
// value objects.
func (s *Server) listPrompts(ctx context.Context) ([]Prompt, error) {
	var out []Prompt
	for p, err := range s.session.Prompts(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, promptFromSDK(s.name, p))
	}
	return out, nil
}

// getPrompt expands a named prompt with the given arguments and returns the
// translated result. The GetPrompt call rides through withSession so a dropped
// session is re-established transparently (one bounded reconnect).
func (s *Server) getPrompt(ctx context.Context, name string, args map[string]string) (PromptResult, error) {
	var res *mcpsdk.GetPromptResult
	err := s.withSession(ctx, func(sess *mcpsdk.ClientSession) error {
		var err error
		res, err = sess.GetPrompt(ctx, &mcpsdk.GetPromptParams{Name: name, Arguments: args})
		return err
	})
	if err != nil {
		return PromptResult{}, err
	}
	return promptResultFromSDK(res), nil
}
