package app

import (
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// classifiedCatalog binds caller-separation metadata to successful tool
// registrations. The final comparison is against Catalog.Tools, so registering
// through the raw catalog cannot silently bypass the guard.
type classifiedCatalog struct {
	catalog *tool.Catalog
	entries map[string]server.ClassificationEntry
}

func newClassifiedCatalog() *classifiedCatalog {
	return &classifiedCatalog{
		catalog: tool.NewCatalog(),
		entries: make(map[string]server.ClassificationEntry),
	}
}

func (c *classifiedCatalog) classifyAdded(before map[string]struct{}, entry server.ClassificationEntry) {
	for _, registered := range c.catalog.Tools() {
		name := registered.Spec().Name
		if _, existed := before[name]; !existed {
			c.entries[name] = entry
		}
	}
}

func (c *classifiedCatalog) capture(entry server.ClassificationEntry, register func()) {
	before := c.names()
	register()
	c.classifyAdded(before, entry)
}

func (c *classifiedCatalog) captureEach(classify func(tool.Tool) (server.ClassificationEntry, bool), register func()) {
	before := c.names()
	register()
	for _, registered := range c.catalog.Tools() {
		name := registered.Spec().Name
		if _, existed := before[name]; existed {
			continue
		}
		if entry, ok := classify(registered); ok {
			c.entries[name] = entry
		}
	}
}

func (c *classifiedCatalog) register(t tool.Tool, entry *server.ClassificationEntry) error {
	if err := c.catalog.Register(t); err != nil {
		return err
	}
	if entry != nil {
		c.entries[t.Spec().Name] = *entry
	}
	return nil
}

func (c *classifiedCatalog) mustRegister(t tool.Tool, entry *server.ClassificationEntry) {
	if err := c.register(t, entry); err != nil {
		panic(err)
	}
}

func (c *classifiedCatalog) names() map[string]struct{} {
	names := make(map[string]struct{}, len(c.catalog.Tools()))
	for _, registered := range c.catalog.Tools() {
		names[registered.Spec().Name] = struct{}{}
	}
	return names
}

func (c *classifiedCatalog) snapshot() map[string]server.ClassificationEntry {
	out := make(map[string]server.ClassificationEntry, len(c.entries))
	for name, entry := range c.entries {
		out[name] = entry
	}
	return out
}

func (c *classifiedCatalog) validate(surface string) error {
	tools := c.catalog.Tools()
	names := make([]string, 0, len(tools))
	for _, registered := range tools {
		names = append(names, registered.Spec().Name)
	}
	if errs := server.ValidateClassifiedNames(surface, c.entries, names); len(errs) > 0 {
		return fmt.Errorf("caller-separation tool classification: %w", errors.Join(errs...))
	}
	return nil
}

func mustValidateClassifiedCatalog(c *classifiedCatalog, surface string, cleanup ...func() error) {
	if err := c.validate(surface); err != nil {
		for _, closeFn := range cleanup {
			if closeFn != nil {
				_ = closeFn()
			}
		}
		panic(err)
	}
}

func classification(kind server.AccessKind, rationale string) *server.ClassificationEntry {
	return &server.ClassificationEntry{Kind: kind, Rationale: rationale}
}

func coreToolClassification(t tool.Tool) (server.ClassificationEntry, bool) {
	switch t.Spec().Name {
	case "Read", listDirToolName, editToolName, writeToolName, "Copy", "Move", "Remove", "Grep", "Glob", "Shell":
		return server.ClassificationEntry{Kind: server.KindExempt, Rationale: "bound to the authorized session workspace and governed by project trust and tool permissions"}, true
	case "WebFetch", "WebSearch", "FetchMcpResource":
		return server.ClassificationEntry{Kind: server.KindExempt, Rationale: "stateless outbound read with no caller-owned durable resource or handle"}, true
	case "ShellStatus":
		return server.ClassificationEntry{Kind: server.KindDerived, Rationale: "reads only the current run's in-memory background command registry"}, true
	default:
		return server.ClassificationEntry{}, false
	}
}

func delegationToolClassification(t tool.Tool) (server.ClassificationEntry, bool) {
	switch t.Spec().Name {
	case "Subagent", "InspectSubagent", "InspectMember":
		return server.ClassificationEntry{Kind: server.KindCallerOwned, Rationale: "durable child or member handles are reauthorized against the persisted owner before access"}, true
	case "SubagentStatus", "Parallel", "Team":
		return server.ClassificationEntry{Kind: server.KindDerived, Rationale: "coordination state is derived from and contained within the current authorized run"}, true
	default:
		return server.ClassificationEntry{}, false
	}
}

func scheduleToolClassification(t tool.Tool) (server.ClassificationEntry, bool) {
	switch t.Spec().Name {
	case "Schedule", "ScheduleQuery":
		return server.ClassificationEntry{Kind: server.KindCallerOwned, Rationale: "schedule manager authorizes each durable schedule and origin through the verified caller"}, true
	default:
		return server.ClassificationEntry{}, false
	}
}

func skillToolClassification(t tool.Tool, assets catalogAssets, session catalogSession) (server.ClassificationEntry, bool) {
	switch t.Spec().Name {
	case "Skill":
		if len(session.skillPartitions) > 0 {
			return server.ClassificationEntry{Kind: server.KindCallerOwned, Rationale: "live learned-skill view is restricted to partitions derived from the verified caller"}, true
		}
		return server.ClassificationEntry{Kind: server.KindExempt, Rationale: "static skill source is deployment or project-trust scoped and exposes no caller-owned handle"}, true
	case "SkillDraft":
		if assets.learnedSkills != nil {
			return server.ClassificationEntry{Kind: server.KindCallerOwned, Rationale: "lifecycle drafts are written through the verified caller's learned-skill partition"}, true
		}
		return server.ClassificationEntry{Kind: server.KindExempt, Rationale: "legacy draft quarantine is workspace scoped and governed by project trust and tool permissions"}, true
	default:
		return server.ClassificationEntry{}, false
	}
}
