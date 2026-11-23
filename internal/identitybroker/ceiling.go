package identitybroker

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Ceiling is the limit on an agent's gateway calls (design step (6), check
// 7): the gateway servers it may call, each with the tools on that server it
// may not. A server is granted whole, minus named tools, because a
// definition grants a server by naming it and excludes tools by name; the
// broker needs no server's tool list, which only a signed-in user can list
// and which can differ per user.
//
// A Ceiling is immutable once made, so it can be shared and compared. The
// zero Ceiling allows no call.
type Ceiling struct {
	// servers maps each granted server to the tools on it that are excluded,
	// by their name on the server. A nil set excludes nothing.
	servers map[string]map[string]struct{}
	// text is String's result, computed when c is made; empty only for the
	// zero Ceiling, which prints as "none".
	text string
}

// CeilingServer is one server a ceiling grants and the tools on it the
// ceiling excludes, by their name on the server, such as "merge_pr" for the
// tool the gateway names "github.merge_pr".
type CeilingServer struct {
	Server      string
	ExceptTools []string
}

// NewCeiling returns the ceiling granting each of servers, minus its
// excluded tools. A server listed twice is granted once, with the union of
// its exclusions. Blank server names are ignored and server names are
// trimmed, as the harness trims a definition's server references; tool names
// are kept exactly, as the harness matches exclusions exactly, and an empty
// one is ignored.
func NewCeiling(servers ...CeilingServer) Ceiling {
	c := Ceiling{servers: map[string]map[string]struct{}{}}
	for _, s := range servers {
		name := strings.TrimSpace(s.Server)
		if name == "" {
			continue
		}
		except := c.servers[name]
		for _, tool := range s.ExceptTools {
			if tool == "" {
				continue
			}
			if except == nil {
				except = map[string]struct{}{}
			}
			except[tool] = struct{}{}
		}
		c.servers[name] = except
	}
	c.text = c.render()
	return c
}

// Servers returns the servers c grants, sorted, each with its excluded tools
// sorted.
func (c Ceiling) Servers() []CeilingServer {
	out := make([]CeilingServer, 0, len(c.servers))
	for _, name := range slices.Sorted(maps.Keys(c.servers)) {
		out = append(out, CeilingServer{Server: name, ExceptTools: slices.Sorted(maps.Keys(c.servers[name]))})
	}
	return out
}

// allowsServer reports whether c grants server at all.
func (c Ceiling) allowsServer(server string) bool {
	_, ok := c.servers[server]
	return ok
}

// Allows reports whether c allows a call of tool, by its name on server.
func (c Ceiling) Allows(server, tool string) bool {
	except, ok := c.servers[server]
	if !ok {
		return false
	}
	_, excluded := except[tool]
	return !excluded
}

// intersect returns the ceiling allowing exactly the calls both c and o
// allow: the servers both grant, each excluding what either excludes.
func (c Ceiling) intersect(o Ceiling) Ceiling {
	out := Ceiling{servers: map[string]map[string]struct{}{}}
	for name, except := range c.servers {
		other, ok := o.servers[name]
		if !ok {
			continue
		}
		union := make(map[string]struct{}, len(except)+len(other))
		maps.Copy(union, except)
		maps.Copy(union, other)
		out.servers[name] = union
	}
	out.text = out.render()
	return out
}

// equal reports whether c and o allow the same calls.
func (c Ceiling) equal(o Ceiling) bool {
	return maps.EqualFunc(c.servers, o.servers, maps.Equal[map[string]struct{}])
}

// String is c in a canonical form for records: each granted server, sorted,
// followed by the tools it excludes, as in "calendar; github except
// delete_repo,merge_pr". A tool name other than letters, digits, '_', '-',
// and '.' is quoted as a Go string, so a name holding a separator cannot be
// read as two. Server names are gateway server names, which hold none. It
// is "none" for a ceiling that grants nothing.
func (c Ceiling) String() string {
	if c.text == "" {
		return "none"
	}
	return c.text
}

// render builds String's canonical form from c's servers; a Ceiling is
// immutable, so it is built once, where c is made.
func (c Ceiling) render() string {
	servers := c.Servers()
	if len(servers) == 0 {
		return "none"
	}
	parts := make([]string, len(servers))
	for i, s := range servers {
		parts[i] = s.Server
		if len(s.ExceptTools) > 0 {
			names := make([]string, len(s.ExceptTools))
			for j, tool := range s.ExceptTools {
				names[j] = recordToolName(tool)
			}
			parts[i] += " except " + strings.Join(names, ",")
		}
	}
	return strings.Join(parts, "; ")
}

// recordToolName is tool as String records it: bare when it is letters,
// digits, '_', '-', and '.', quoted otherwise.
func recordToolName(tool string) string {
	for _, r := range tool {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return strconv.Quote(tool)
		}
	}
	return tool
}

// checkCeiling is design step (6) check 7: the acting agent's stored
// effective ceiling allows the call's exact server and tool, by its name on
// the server, or, for a listing, the server. Its refusal does not name the
// ceiling, which can hold a parent's exclusions; the decision record does.
//
// Shortcut: the ceiling is a plain set check, not generated Cedar policy, and decision records name no policy revision; removed by #2849, which also replaces the Ceiling value and its intersection with generated policy.
func checkCeiling(c Ceiling, method, server, tool string) error {
	switch {
	case method == MethodToolsList && c.allowsServer(server):
		return nil
	case method == MethodToolsCall && c.Allows(server, tool):
		return nil
	}
	return deniedError("the agent's tool limit does not allow the call")
}
