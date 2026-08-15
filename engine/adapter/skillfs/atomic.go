package skillfs

//revive:disable:exported // atomic.go declares the live catalog contract as one unit

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type CatalogSnapshot struct {
	Generation uint64
	Metas      []tool.SkillMeta
	byName     map[string]catalogEntry
	external   tool.SkillSource
}

type catalogEntry struct {
	meta      tool.SkillMeta
	body      string
	learned   bool
	partition learning.SkillPartition
}

type partitionSnapshot struct {
	generation uint64
	entries    map[string]catalogEntry
}

// AtomicCatalog retains independent immutable generations for every caller/project
// partition. Publishing one partition never replaces another partition's view.
type AtomicCatalog struct {
	mu         sync.RWMutex
	external   map[string]catalogEntry
	source     tool.SkillSource
	partitions map[learning.SkillPartition]partitionSnapshot
	generation uint64
}

func NewAtomicCatalog(external []tool.SkillMeta, source tool.SkillSource, learned []learning.SkillVersion) *AtomicCatalog {
	c := &AtomicCatalog{external: externalEntries(external), source: source, partitions: map[learning.SkillPartition]partitionSnapshot{}}
	if len(learned) > 0 {
		parts := uniquePartitions(learned)
		c.RefreshPartitions(parts, learned)
	}
	return c
}

func externalEntries(metas []tool.SkillMeta) map[string]catalogEntry {
	out := make(map[string]catalogEntry, len(metas))
	for _, meta := range metas {
		meta = cloneMeta(meta)
		out[meta.Name] = catalogEntry{meta: meta}
	}
	return out
}

func uniquePartitions(values []learning.SkillVersion) []learning.SkillPartition {
	seen := map[learning.SkillPartition]bool{}
	var out []learning.SkillPartition
	for _, value := range values {
		if !seen[value.Partition] {
			seen[value.Partition] = true
			out = append(out, value.Partition)
		}
	}
	return out
}

// Refresh replaces only the partitions represented by learned. Call
// RefreshPartitions when an empty authoritative generation must be published.
func (c *AtomicCatalog) Refresh(_ []tool.SkillMeta, _ tool.SkillSource, learned []learning.SkillVersion) []learning.SkillVersion {
	return c.RefreshPartitions(uniquePartitions(learned), learned)
}

// RefreshPartitions atomically replaces the named partition generations while
// retaining every other caller/project partition and the deployment-owned entries.
func (c *AtomicCatalog) RefreshPartitions(partitions []learning.SkillPartition, learned []learning.SkillVersion) []learning.SkillVersion {
	grouped := make(map[learning.SkillPartition]map[string]catalogEntry, len(partitions))
	for _, partition := range partitions {
		grouped[partition] = map[string]catalogEntry{}
	}
	var conflicts []learning.SkillVersion
	for _, version := range learned {
		if version.State != learning.SkillActive {
			continue
		}
		if _, exists := c.external[version.Bundle.Name]; exists {
			conflicts = append(conflicts, version)
			continue
		}
		entries, selected := grouped[version.Partition]
		if !selected {
			continue
		}
		meta := tool.SkillMeta{Name: version.Bundle.Name, Description: version.Bundle.Description, Metadata: map[string]string{
			"mecatl.agent_owned": "true", "mecatl.owner_agent": version.OwnerAgent, "mecatl.active_version": string(version.Version),
		}}
		entries[meta.Name] = catalogEntry{meta: meta, body: version.Bundle.Body, learned: true, partition: version.Partition}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, partition := range partitions {
		c.generation++
		c.partitions[partition] = partitionSnapshot{generation: c.generation, entries: grouped[partition]}
	}
	return conflicts
}

// ClearPartitions fail-closes the named views after an uncertain durable read.
func (c *AtomicCatalog) ClearPartitions(partitions ...learning.SkillPartition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, partition := range partitions {
		c.generation++
		c.partitions[partition] = partitionSnapshot{generation: c.generation, entries: map[string]catalogEntry{}}
	}
}

// RevokePartition removes one learned name only from the named partition. It is
// serialized with publication by callers; unrelated principals are untouched.
func (c *AtomicCatalog) RevokePartition(partition learning.SkillPartition, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.partitions[partition]
	if !ok || !current.entries[name].learned {
		return
	}
	entries := make(map[string]catalogEntry, len(current.entries)-1)
	for key, entry := range current.entries {
		if key != name {
			entries[key] = entry
		}
	}
	c.generation++
	c.partitions[partition] = partitionSnapshot{generation: c.generation, entries: entries}
}

// RevokeLearned is retained for compatibility and revokes matching learned names
// in all retained partitions. New publication code must use RevokePartition.
func (c *AtomicCatalog) RevokeLearned(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for partition, current := range c.partitions {
		if !current.entries[name].learned {
			continue
		}
		entries := make(map[string]catalogEntry, len(current.entries)-1)
		for key, entry := range current.entries {
			if key != name {
				entries[key] = entry
			}
		}
		c.generation++
		c.partitions[partition] = partitionSnapshot{generation: c.generation, entries: entries}
	}
}

// View returns one immutable caller-bound snapshot. Later publications cannot
// change List/Spec/Execute consistency for a request already holding the view.
func (c *AtomicCatalog) View(partitions ...learning.SkillPartition) CatalogSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make(map[string]catalogEntry, len(c.external))
	for name, entry := range c.external {
		entries[name] = entry
	}
	var generation uint64
	for _, partition := range partitions {
		part := c.partitions[partition]
		if part.generation > generation {
			generation = part.generation
		}
		for name, entry := range part.entries {
			if _, external := c.external[name]; !external {
				entries[name] = entry
			}
		}
	}
	metas := make([]tool.SkillMeta, 0, len(entries))
	for _, entry := range entries {
		metas = append(metas, cloneMeta(entry.meta))
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return CatalogSnapshot{Generation: generation, Metas: metas, byName: entries, external: c.source}
}

func (c *AtomicCatalog) Snapshot() CatalogSnapshot { return c.View() }

func (c *AtomicCatalog) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return cloneMetas(c.View().Metas), nil
}

func (c *AtomicCatalog) SkillBody(ctx context.Context, name string) (string, error) {
	return snapshotSource{snapshot: ptrSnapshot(c.View())}.SkillBody(ctx, name)
}
func (c *AtomicCatalog) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	return snapshotSource{snapshot: ptrSnapshot(c.View())}.ListSkillAssets(ctx, name)
}
func (c *AtomicCatalog) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	return snapshotSource{snapshot: ptrSnapshot(c.View())}.ReadSkillAsset(ctx, skill, asset)
}
func ptrSnapshot(s CatalogSnapshot) *CatalogSnapshot { return &s }

func cloneMetas(in []tool.SkillMeta) []tool.SkillMeta {
	out := make([]tool.SkillMeta, len(in))
	for i := range in {
		out[i] = cloneMeta(in[i])
	}
	return out
}
func cloneMeta(in tool.SkillMeta) tool.SkillMeta {
	in.AllowedTools = append([]string(nil), in.AllowedTools...)
	if in.Metadata != nil {
		values := make(map[string]string, len(in.Metadata))
		for k, v := range in.Metadata {
			values[k] = v
		}
		in.Metadata = values
	}
	return in
}

type LiveTool struct {
	catalog    *AtomicCatalog
	partitions []learning.SkillPartition
}

var _ tool.Tool = LiveTool{}

// NewLiveTool deliberately binds the deployment-only view. Learned entries must
// be exposed through NewLiveToolForPartitions so Spec cannot leak another caller.
func NewLiveTool(catalog *AtomicCatalog) LiveTool { return LiveTool{catalog: catalog} }
func NewLiveToolForPartitions(catalog *AtomicCatalog, partitions ...learning.SkillPartition) LiveTool {
	return LiveTool{catalog: catalog, partitions: append([]learning.SkillPartition(nil), partitions...)}
}
func (t LiveTool) view() CatalogSnapshot { return t.catalog.View(t.partitions...) }
func (t LiveTool) Spec() tool.ToolSpec {
	snapshot := t.view()
	return NewTool(snapshot.Metas, snapshotSource{&snapshot}).Spec()
}
func (LiveTool) ReadOnly() bool { return true }
func (t LiveTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	snapshot := t.view()
	var args skillArgs
	if msg, ok := ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, msg), nil
	}
	if args.Asset != "" {
		if entry, ok := snapshot.byName[args.Name]; ok && entry.learned {
			return session.NewToolError(call.ID, fmt.Sprintf("learned skill %q is body-only and does not support asset requests", args.Name)), nil
		}
	}
	return NewTool(snapshot.Metas, snapshotSource{&snapshot}).Execute(ctx, call, env)
}

type snapshotSource struct{ snapshot *CatalogSnapshot }

func (s snapshotSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return cloneMetas(s.snapshot.Metas), nil
}
func (s snapshotSource) SkillBody(ctx context.Context, name string) (string, error) {
	entry, ok := s.snapshot.byName[name]
	if !ok {
		return "", tool.ErrSkillNotFound
	}
	if entry.learned {
		return entry.body, nil
	}
	if s.snapshot.external == nil {
		return "", tool.ErrSkillNotFound
	}
	return s.snapshot.external.SkillBody(ctx, name)
}
func (s snapshotSource) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	entry, ok := s.snapshot.byName[name]
	if !ok {
		return nil, tool.ErrSkillNotFound
	}
	if entry.learned {
		return nil, nil
	}
	if s.snapshot.external == nil {
		return nil, tool.ErrSkillNotFound
	}
	return s.snapshot.external.ListSkillAssets(ctx, name)
}
func (s snapshotSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	entry, ok := s.snapshot.byName[skill]
	if !ok {
		return nil, tool.ErrSkillAssetNotFound
	}
	if entry.learned {
		return nil, fmt.Errorf("learned skill %q is body-only and has no assets: %w", skill, tool.ErrSkillAssetNotFound)
	}
	if s.snapshot.external == nil {
		return nil, tool.ErrSkillAssetNotFound
	}
	return s.snapshot.external.ReadSkillAsset(ctx, skill, asset)
}
