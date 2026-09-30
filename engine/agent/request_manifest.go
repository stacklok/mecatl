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
	fragments := len(r.fragments)
	if fragments > len(req.Messages) {
		fragments = len(req.Messages)
	}
	ephemeral, persisted := req.Messages[:fragments], req.Messages[fragments:]
	systemBytes := len(req.System.Render())
	ephemeralBytes := marshaledBytes(ephemeral)
	persistedBytes := marshaledBytes(persisted)
	toolMetrics, toolBytes := requestToolMetrics(e.deps.TokenCounter, req.Tools)
	requestTokens := estimateRequestTokens(e.deps.TokenCounter, req)
	systemTokens := countLayered(e.deps.TokenCounter, req.System)
	ephemeralTokens := e.deps.TokenCounter.CountMessages(ephemeral)
	persistedTokens := e.deps.TokenCounter.CountMessages(persisted)
	toolTokens := estimatedToolTokens(toolMetrics)
	payload := session.RequestManifestPayload{
		Provider:                         manifestIdentifier(sess.ProviderID),
		Model:                            manifestIdentifier(e.deps.Model),
		ReasoningEffort:                  manifestIdentifier(sess.ReasoningEffort),
		ToolNames:                        make([]string, 0, len(req.Tools)),
		ToolDecisions:                    e.requestToolDecisions(r, sess, env),
		AdvertisedToolSchemaBytes:        advertisedToolSchemaBytes(req.Tools),
		MessageCount:                     len(req.Messages),
		MessageBytes:                     len(messageBytes),
		Prompt:                           requestPromptManifest(e.deps.TokenCounter, req.System, r.fragments, r.fragmentManifest),
		TokenEstimateMethod:              "local_estimate",
		EstimatedRequestTokens:           &requestTokens,
		EstimatedSystemTokens:            &systemTokens,
		EstimatedEphemeralFragmentTokens: &ephemeralTokens,
		EstimatedPersistedHistoryTokens:  &persistedTokens,
		EstimatedAdvertisedToolTokens:    &toolTokens,
		EstimatedSystemBytes:             &systemBytes,
		EstimatedEphemeralFragmentBytes:  &ephemeralBytes,
		EstimatedPersistedHistoryBytes:   &persistedBytes,
		EstimatedAdvertisedToolBytes:     &toolBytes,
		AdvertisedTools:                  toolMetrics,
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

func marshaledBytes(value any) int {
	encoded, _ := json.Marshal(value)
	return len(encoded)
}

func requestToolMetrics(counter TokenCounter, specs []tool.ToolSpec) ([]session.RequestToolMetric, int) {
	metrics := make([]session.RequestToolMetric, 0, len(specs))
	bytes := 0
	for _, spec := range specs {
		nameTokens := counter.Count(spec.Name)
		descriptionTokens := counter.Count(spec.Description)
		schemaTokens := countBytes(counter, spec.Schema)
		metrics = append(metrics, session.RequestToolMetric{
			Name: manifestLabel(spec.Name), NameBytes: len(spec.Name), DescriptionBytes: len(spec.Description), SchemaBytes: len(spec.Schema),
			EstimatedNameTokens: nameTokens, EstimatedDescriptionTokens: descriptionTokens, EstimatedSchemaTokens: schemaTokens,
			EstimatedTokens: perToolSpecOverhead + nameTokens + descriptionTokens + schemaTokens,
		})
		bytes += len(spec.Name) + len(spec.Description) + len(spec.Schema)
	}
	return metrics, bytes
}

func estimatedToolTokens(metrics []session.RequestToolMetric) int {
	tokens := 0
	for _, metric := range metrics {
		tokens += metric.EstimatedTokens
	}
	return tokens
}

func advertisedToolSchemaBytes(specs []tool.ToolSpec) int {
	bytes := 0
	for _, spec := range specs {
		bytes += len(spec.Schema)
	}
	return bytes
}

func (e *Engine) requestToolDecisions(r *Run, sess *session.Session, env tool.Environment) []session.RequestToolDecision {
	authority, bound := sess.BoundAuthority()
	available := make(map[string]struct{}, len(e.deps.Catalog.AvailableNames(sess.Mode)))
	for _, name := range e.deps.Catalog.AvailableNames(sess.Mode) {
		available[name] = struct{}{}
	}
	overlays := make(map[string]struct{}, len(r.extraToolNames))
	for _, name := range r.extraToolNames {
		overlays[name] = struct{}{}
	}
	catalogNames := e.deps.Catalog.Names()
	decisions := make([]session.RequestToolDecision, 0, len(catalogNames)+len(r.extraToolNames))
	for _, name := range catalogNames {
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
			candidate, _ := e.deps.Catalog.Lookup(name)
			if _, ok := candidate.(tool.Disclosable); ok {
				decision = session.RequestToolDisclosureHidden
			}
		}
		decisions = append(decisions, session.RequestToolDecision{
			Name: manifestLabel(name), Source: requestToolSource(name, decision == session.RequestToolShadowed), Decision: decision,
		})
	}
	for _, name := range r.extraToolNames {
		if _, exists := e.deps.Catalog.Lookup(name); exists {
			continue
		}
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

func requestPromptManifest(counter TokenCounter, system prompt.Layered, fragments []session.Message, metadata []prompt.InstructionManifest) []session.RequestPromptComponent {
	out := make([]session.RequestPromptComponent, 0, 2+len(fragments))
	appendComponent := func(kind, provenance string, body []byte, tokens int) {
		if len(body) == 0 {
			return
		}
		out = append(out, session.RequestPromptComponent{
			Kind: kind, Provenance: provenance, Bytes: len(body), EstimatedTokens: &tokens,
		})
	}
	appendComponent(session.RequestPromptSystem, session.RequestProvenanceStable, []byte(system.StablePrefix), counter.Count(system.StablePrefix))
	appendComponent(session.RequestPromptSystem, session.RequestProvenanceVolatile, []byte(system.VolatileSuffix), counter.Count(system.VolatileSuffix))
	for i, fragment := range fragments {
		kind, provenance := session.RequestProvenanceCustom, session.RequestProvenanceUnknown
		if i < len(metadata) {
			kind, provenance = metadata[i].Kind, metadata[i].Provenance
		}
		body, _ := json.Marshal(fragment)
		appendComponent(kind, provenance, body, counter.CountMessages([]session.Message{fragment}))
		if i < len(metadata) && provenance == session.RequestProvenanceRules && metadata[i].Rules != nil && len(out) > 0 {
			component := &out[len(out)-1]
			component.OmittedRules = metadata[i].Rules.OmittedCount
			for _, span := range metadata[i].Rules.Spans {
				if span.Start < 0 || span.End <= span.Start || span.End > len(fragment.Text) {
					continue
				}
				block := fragment.Text[span.Start:span.End]
				component.Rules = append(component.Rules, session.RequestRuleMetric{
					Name: span.Name, Origin: span.Origin, RenderedBytes: len(block),
					EstimatedTokens: counter.Count(block),
				})
			}
		}
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
