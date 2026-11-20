package identitybroker

import (
	"fmt"
	"maps"
	"slices"
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
// its exclusions. Blank server and tool names are ignored.
func NewCeiling(servers ...CeilingServer) Ceiling {
	c := Ceiling{servers: map[string]map[string]struct{}{}}
	for _, s := range servers {
		name := strings.TrimSpace(s.Server)
		if name == "" {
			continue
		}
		except := c.servers[name]
		for _, tool := range s.ExceptTools {
			if tool = strings.TrimSpace(tool); tool == "" {
				continue
			}
			if except == nil {
				except = map[string]struct{}{}
			}
			except[tool] = struct{}{}
		}
		c.servers[name] = except
	}
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

// allows reports whether c allows a call of tool, by its name on server.
func (c Ceiling) allows(server, tool string) bool {
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
	return out
}

// equal reports whether c and o allow the same calls.
func (c Ceiling) equal(o Ceiling) bool {
	return maps.EqualFunc(c.servers, o.servers, maps.Equal[map[string]struct{}])
}

// String is c in a canonical form for records: each granted server, sorted,
// followed by the tools it excludes, as in "calendar; github except
// delete_repo,merge_pr". It is "none" for a ceiling that grants nothing.
func (c Ceiling) String() string {
	servers := c.Servers()
	if len(servers) == 0 {
		return "none"
	}
	parts := make([]string, len(servers))
	for i, s := range servers {
		parts[i] = s.Server
		if len(s.ExceptTools) > 0 {
			parts[i] += " except " + strings.Join(s.ExceptTools, ",")
		}
	}
	return strings.Join(parts, "; ")
}

// checkCeiling is design step (6) check 7: the acting agent's stored
// effective ceiling allows the call's exact server and tool, by its name on
// the server, or, for a listing, the server. It is one function so generated
// Cedar policy (#2849) replaces only its body.
//
// Shortcut: the ceiling is a plain set check, not generated Cedar policy, and decision records name no policy revision; removed by #2849.
func checkCeiling(c Ceiling, method, server, tool string) error {
	switch {
	case method == MethodToolsList && c.allowsServer(server):
		return nil
	case method == MethodToolsCall && c.allows(server, tool):
		return nil
	}
	return deniedError(fmt.Sprintf("the agent's tool limit (%s) does not allow the call", c))
}
