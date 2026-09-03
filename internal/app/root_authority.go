package app

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/noopauthority"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/cedarauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

const (
	rootAuthorityProvenance = "composed_root"
	rootAuthorityDefinition = "root"
	rootDelegationDepth     = 1
)

func authorityEvaluatorPostureLine(adapter string) string {
	return fmt.Sprintf("authority evaluator posture: adapter=%s enforcement=%t", adapter, adapter == "local" || adapter == "cedar")
}

func selectAuthorityEvaluator(mode, cedarPolicyPath string) (port.AuthorityEvaluator, string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "local":
		return localauthority.New(), "local", nil
	case "noop":
		return noopauthority.New(), "noop", nil
	case "cedar":
		if strings.TrimSpace(cedarPolicyPath) == "" {
			return nil, "", fmt.Errorf("cedar authority evaluator requires an operator policy file")
		}
		policy, err := os.ReadFile(cedarPolicyPath)
		if err != nil {
			return nil, "", fmt.Errorf("load Cedar authority policy %q: %w", cedarPolicyPath, err)
		}
		evaluator, err := cedarauthority.New(policy)
		if err != nil {
			return nil, "", err
		}
		return evaluator, "cedar", nil
	default:
		return nil, "", fmt.Errorf("unknown authority evaluator %q (supported: local, noop, cedar)", mode)
	}
}

func managedDefinitionAuthority(def tool.AgentDef) bool {
	return def.Origin == tool.AgentOriginExplicit
}

// agentDefinitionAuthorityCeiling projects the fully resolved specialist catalog
// into the authority representation. Explicit definitions alone may establish this
// ceiling; callers enforce that tier bit independently so a lower tier cannot gain
// one by choosing a colliding name.
func agentDefinitionAuthorityCeiling(def tool.AgentDef, resolved, resources []string, directWrite bool) governance.CapabilitySet {
	disallowed := make(map[string]struct{}, len(def.DisallowedTools))
	for _, name := range def.DisallowedTools {
		disallowed[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(resolved))
	tools := make([]string, 0, len(resolved))
	for _, name := range resolved {
		if _, denied := disallowed[name]; denied {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, name)
	}
	for _, capability := range resources {
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		tools = append(tools, capability)
	}
	return governance.CapabilitySet{Tools: tools, RemainingDelegationDepth: rootDelegationDepth, FileSystem: true, DirectWrite: directWrite}
}

func mcpResourceCapabilities(manager *mcp.Manager) []string {
	if manager == nil {
		return nil
	}
	var capabilities []string
	for _, server := range manager.Servers() {
		if len(server.Resources()) != 0 {
			capabilities = append(capabilities, governance.MCPResourceCapability(server.Name()))
		}
	}
	return capabilities
}

// mintRootAuthority establishes a complete root capability set from the catalog
// assembled for the session. A Team-capable root also carries the latent member
// coordination names that can be installed only in its derived member catalogs.
// Child derivation consumes this carried value later; it is not performed at the
// composition root.
func mintRootAuthority(catalog *tool.Catalog, resources []string, kind session.SessionKind) session.Authority {
	if kind == session.SessionKindDebug {
		return session.Authority{
			CapabilitySet:      governance.CapabilitySet{Tools: []string{sessiondebug.ToolName}},
			Provenance:         rootAuthorityProvenance,
			DefinitionIdentity: rootAuthorityDefinition,
		}
	}
	tools := catalog.Tools()
	names := make([]string, 0, len(tools)+len(resources))
	for _, registered := range tools {
		names = append(names, registered.Spec().Name)
	}
	if _, hasTeam := catalog.Lookup("Team"); hasTeam {
		memberTools := agent.MemberToolNames()
		latent := make([]string, 0, len(memberTools))
		registered := make(map[string]struct{}, len(names))
		for _, name := range names {
			registered[name] = struct{}{}
		}
		for name := range memberTools {
			if _, exists := registered[name]; !exists {
				latent = append(latent, name)
			}
		}
		sort.Strings(latent)
		names = append(names, latent...)
	}
	names = append(names, resources...)
	_, fileSystem := catalog.Lookup("Read")
	_, directWrite := catalog.Lookup("Write")
	return session.Authority{
		CapabilitySet: governance.CapabilitySet{
			Tools:                    names,
			RemainingDelegationDepth: rootDelegationDepth,
			FileSystem:               fileSystem,
			DirectWrite:              directWrite,
		},
		Provenance:         rootAuthorityProvenance,
		DefinitionIdentity: rootAuthorityDefinition,
	}
}
