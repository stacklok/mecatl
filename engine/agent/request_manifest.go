package agent

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func (e *Engine) emitRequestManifest(r *Run, sess *session.Session, env tool.Environment, req port.LLMRequest, turn int) {
	if !e.deps.EnableDurableEvidence {
		return
	}
	manifest := e.requestManifest(r, sess, env, req)
	e.emit(r, session.Event{Type: session.EvRequestManifest, Turn: turn, RequestManifest: &manifest})
}

func (e *Engine) requestManifest(r *Run, sess *session.Session, env tool.Environment, req port.LLMRequest) session.RequestManifestPayload {
	messageBytes, _ := json.Marshal(req.Messages)
	payload := session.RequestManifestPayload{
		Provider:        manifestIdentifier(sess.ProviderID),
		Model:           manifestIdentifier(e.deps.Model),
		ReasoningEffort: manifestIdentifier(sess.ReasoningEffort),
		ToolNames:       make([]string, 0, len(req.Tools)),
		ToolDecisions:   e.requestToolDecisions(r, sess, env),
		MessageCount:    len(req.Messages),
		MessageBytes:    len(messageBytes),
		Prompt:          requestPromptManifest(req.System, r.fragments, r.fragmentManifest),
	}
	if e.deps.ContextWindow != nil {
		if window := e.deps.ContextWindow(); window > 0 {
			payload.ContextWindow = window
		}
	}
	for _, spec := range req.Tools {
		payload.ToolNames = append(payload.ToolNames, manifestLabel(spec.Name))
	}
	return payload
}

func (e *Engine) requestToolDecisions(r *Run, sess *session.Session, env tool.Environment) []session.RequestToolDecision {
	authority, bound := sess.BoundAuthority()
	available := make(map[string]tool.Tool, len(e.deps.Catalog.Tools()))
	for _, candidate := range e.deps.Catalog.Available(sess.Mode) {
		available[candidate.Spec().Name] = candidate
	}
	overlays := make(map[string]struct{}, len(r.req.ExtraTools))
	for _, extra := range r.req.ExtraTools {
		overlays[extra.Spec().Name] = struct{}{}
	}
	decisions := make([]session.RequestToolDecision, 0, len(e.deps.Catalog.Tools())+len(r.req.ExtraTools))
	catalogNames := make(map[string]struct{}, len(e.deps.Catalog.Tools()))
	for _, candidate := range e.deps.Catalog.Tools() {
		name := candidate.Spec().Name
		catalogNames[name] = struct{}{}
		decision := session.RequestToolAdvertised
		if _, ok := available[name]; !ok {
			decision = session.RequestToolModeFiltered
		} else if bound && !authorityDisclosesTool(name, authority.CapabilitySet) {
			decision = session.RequestToolAuthorityFiltered
		} else if name == tool.ShellToolName && env.CommandRunner() == nil && env.Ref().Kind != session.EnvKindNoFS {
			decision = session.RequestToolMountUnavailable
		} else if _, ok := overlays[name]; ok {
			decision = session.RequestToolShadowed
		} else if e.deps.ProgressiveTools {
			if _, ok := candidate.(tool.Disclosable); ok {
				decision = session.RequestToolDisclosureHidden
			}
		}
		sourceOverlay := decision == session.RequestToolShadowed
		decisions = append(decisions, session.RequestToolDecision{
			Name: manifestLabel(name), Source: requestToolSource(name, sourceOverlay), Decision: decision,
		})
	}
	for _, extra := range r.req.ExtraTools {
		name := extra.Spec().Name
		if _, exists := catalogNames[name]; exists {
			continue
		}
		catalogNames[name] = struct{}{}
		decisions = append(decisions, session.RequestToolDecision{
			Name: manifestLabel(name), Source: requestToolSource(name, true), Decision: session.RequestToolAdvertised,
		})
	}
	return decisions
}

func requestToolSource(name string, overlay bool) string {
	if overlay {
		return session.RequestToolSourceOverlay
	}
	if strings.HasPrefix(name, "mcp__") {
		return session.RequestToolSourceMCP
	}
	return session.RequestToolSourceCatalog
}

func requestPromptManifest(system prompt.Layered, fragments []session.Message, metadata []prompt.InstructionManifest) []session.RequestPromptComponent {
	out := make([]session.RequestPromptComponent, 0, 2+len(fragments))
	appendComponent := func(kind, provenance string, body []byte) {
		if len(body) == 0 {
			return
		}
		out = append(out, session.RequestPromptComponent{
			Kind: kind, Provenance: provenance, Bytes: len(body),
		})
	}
	appendComponent(session.RequestPromptSystem, session.RequestProvenanceStable, []byte(system.StablePrefix))
	appendComponent(session.RequestPromptSystem, session.RequestProvenanceVolatile, []byte(system.VolatileSuffix))
	for i, fragment := range fragments {
		kind, provenance := session.RequestProvenanceCustom, session.RequestProvenanceUnknown
		if i < len(metadata) {
			kind, provenance = metadata[i].Kind, metadata[i].Provenance
		}
		body, _ := json.Marshal(fragment)
		appendComponent(kind, provenance, body)
	}
	return out
}

func manifestIdentifier(value string) string {
	value = manifestLabel(value)
	if len(value) > 256 || strings.Contains(value, "://") {
		return ""
	}
	return value
}

func manifestLabel(value string) string {
	value = session.ToValidUTF8(value)
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) {
			return utf8.RuneError
		}
		return r
	}, value)
}
