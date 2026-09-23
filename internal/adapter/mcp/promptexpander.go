package mcp

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/prompt"
)

// PromptExpander implements prompt.CommandExpander by recognizing an input of
// the form
//
//	/mcp__<server>__<prompt> [key=value ...]
//
// expanding it via Provider.GetPrompt, and returning the flattened, role-tagged
// expansion. The "mcp__<server>__<prompt>" shape mirrors the namespacing of
// proxied remote tools, so a model that knows the tool namespace can address a
// prompt the same way. It carries no SDK types and only depends on the domain
// CommandExpander interface plus this package's Provider.
//
// ARGUMENT SYNTAX: arguments are whitespace-separated "key=value" pairs (the
// value may not contain spaces in v1; this is a deliberately simple, documented
// grammar consistent with the positional feel of DirCommandExpander). A token
// without an "=" is ignored. The MCP prompt's declared arguments determine what
// the server actually uses.
//
// PRECEDENCE: this expander only matches the "/mcp__..." shape. Any other input
// — including a "/name"-style slash command handled by DirCommandExpander —
// returns ("", false, nil) so the next expander in a prompt.MultiExpander runs.
// Composed AFTER DirCommandExpander, a file-backed command named "mcp__x__y"
// would shadow this; in practice command files are not named with the mcp__
// prefix, so the two do not overlap.
type PromptExpander struct {
	provider Provider
}

// NewPromptExpander builds a PromptExpander over the given Provider.
func NewPromptExpander(p Provider) *PromptExpander {
	return &PromptExpander{provider: p}
}

var _ prompt.CommandExpander = (*PromptExpander)(nil)

// promptPrefix is the leading token marking an MCP prompt invocation.
const promptPrefix = "/mcp__"

// Expand implements prompt.CommandExpander. See the type doc for the grammar.
func (e *PromptExpander) Expand(ctx context.Context, input string) (string, bool, error) {
	server, name, args, ok := parsePromptInvocation(input)
	if !ok {
		return input, false, nil
	}
	if e.provider == nil {
		// Nothing to expand against; let the next expander try.
		return input, false, nil
	}
	res, err := e.provider.GetPrompt(ctx, server, name, args)
	if err != nil {
		// A failed expansion is NOT a fatal error: leave the input unchanged so
		// the run is not aborted (a mistyped server/prompt should not crash the
		// turn). The next expander (if any) gets a chance, then the raw text flows
		// through. Returning a non-nil error here would abort recordPrompt.
		return input, false, nil
	}
	return flattenPromptResult(res), true, nil
}

// parsePromptInvocation reports whether input is an "/mcp__<server>__<prompt>"
// invocation and, if so, returns the server, prompt name, and parsed key=value
// arguments. The server and prompt segments are split on the FIRST "__" after
// the prefix (server) and the remainder is the prompt name (which may itself
// contain "__").
func parsePromptInvocation(input string) (server, name string, args map[string]string, ok bool) {
	s := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(s, promptPrefix) {
		return "", "", nil, false
	}
	rest := s[len(promptPrefix):]

	// Split the head token (before whitespace) from the argument tail.
	head := rest
	var tail string
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		head = rest[:i]
		tail = rest[i+1:]
	}

	// head is "<server>__<prompt>". Split on the first "__".
	sep := strings.Index(head, "__")
	if sep < 0 {
		return "", "", nil, false
	}
	server = head[:sep]
	name = head[sep+len("__"):]
	if server == "" || name == "" {
		return "", "", nil, false
	}

	args = parsePromptArgs(tail)
	return server, name, args, true
}

// parsePromptArgs splits a whitespace-separated list of key=value pairs into a
// map. Tokens without an "=" are ignored. Returns nil when there are no pairs so
// callers can pass it straight to GetPrompt (nil == no arguments).
func parsePromptArgs(tail string) map[string]string {
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		k, v, found := strings.Cut(f, "=")
		if !found || k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
