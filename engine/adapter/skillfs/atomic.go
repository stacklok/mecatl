package skillfs

//revive:disable:exported // atomic.go declares the live catalog contract as one unit

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type CatalogSnapshot struct {
	Generation uint64
	Metas      []tool.SkillMeta
	byName     map[string]catalogEntry
}

type catalogEntry struct {
	meta    tool.SkillMeta
	body    string
	learned bool
}

type AtomicCatalog struct {
	generation atomic.Uint64
	current    atomic.Pointer[CatalogSnapshot]
	external   tool.SkillSource
}

func NewAtomicCatalog(external []tool.SkillMeta, source tool.SkillSource, learned []learning.SkillVersion) *AtomicCatalog {
	c := &AtomicCatalog{external: source}
	c.Refresh(external, source, learned)
	return c
}

func (c *AtomicCatalog) Refresh(external []tool.SkillMeta, source tool.SkillSource, learned []learning.SkillVersion) []learning.SkillVersion {
	entries := make(map[string]catalogEntry, len(external)+len(learned))
	for _, meta := range external {
		meta = cloneMeta(meta)
		entries[meta.Name] = catalogEntry{meta: meta}
	}
	var conflicts []learning.SkillVersion
	for _, version := range learned {
		if version.State != learning.SkillActive {
			continue
		}
		if _, ok := entries[version.Bundle.Name]; ok {
			conflicts = append(conflicts, version)
			continue
		}
		meta := tool.SkillMeta{Name: version.Bundle.Name, Description: version.Bundle.Description, Metadata: map[string]string{
			"mecatl.agent_owned": "true", "mecatl.owner_agent": version.OwnerAgent, "mecatl.active_version": string(version.Version),
		}}
		entries[meta.Name] = catalogEntry{meta: meta, body: version.Bundle.Body, learned: true}
	}
	metas := make([]tool.SkillMeta, 0, len(entries))
	for _, entry := range entries {
		metas = append(metas, cloneMeta(entry.meta))
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	c.external = source
	c.current.Store(&CatalogSnapshot{Generation: c.generation.Add(1), Metas: metas, byName: entries})
	return conflicts
}

func (c *AtomicCatalog) Snapshot() CatalogSnapshot {
	s := c.load()
	return CatalogSnapshot{Generation: s.Generation, Metas: cloneMetas(s.Metas)}
}

func (c *AtomicCatalog) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return cloneMetas(c.load().Metas), nil
}

func (c *AtomicCatalog) SkillBody(ctx context.Context, name string) (string, error) {
	entry, ok := c.load().byName[name]
	if !ok {
		return "", tool.ErrSkillNotFound
	}
	if entry.learned {
		return entry.body, nil
	}
	if c.external == nil {
		return "", tool.ErrSkillNotFound
	}
	return c.external.SkillBody(ctx, name)
}

func (c *AtomicCatalog) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	entry, ok := c.load().byName[name]
	if !ok {
		return nil, tool.ErrSkillNotFound
	}
	if entry.learned {
		return nil, nil
	}
	if c.external == nil {
		return nil, tool.ErrSkillNotFound
	}
	return c.external.ListSkillAssets(ctx, name)
}

func (c *AtomicCatalog) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	entry, ok := c.load().byName[skill]
	if !ok {
		return nil, tool.ErrSkillAssetNotFound
	}
	if entry.learned {
		return nil, fmt.Errorf("learned skill %q is body-only and has no assets: %w", skill, tool.ErrSkillAssetNotFound)
	}
	if c.external == nil {
		return nil, tool.ErrSkillAssetNotFound
	}
	return c.external.ReadSkillAsset(ctx, skill, asset)
}

func (c *AtomicCatalog) load() *CatalogSnapshot {
	if s := c.current.Load(); s != nil {
		return s
	}
	return &CatalogSnapshot{byName: map[string]catalogEntry{}}
}

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

type LiveTool struct{ catalog *AtomicCatalog }

var _ tool.Tool = LiveTool{}

func NewLiveTool(catalog *AtomicCatalog) LiveTool { return LiveTool{catalog: catalog} }

func (t LiveTool) Spec() tool.ToolSpec {
	s := t.catalog.load()
	return NewTool(s.Metas, t.catalog).Spec()
}

func (LiveTool) ReadOnly() bool { return true }

func (t LiveTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args skillArgs
	if msg, ok := ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, msg), nil
	}
	if args.Asset != "" {
		if entry, ok := t.catalog.load().byName[args.Name]; ok && entry.learned {
			return session.NewToolError(call.ID, fmt.Sprintf("learned skill %q is body-only and does not support asset requests", args.Name)), nil
		}
	}
	s := t.catalog.load()
	return NewTool(s.Metas, t.catalog).Execute(ctx, call, env)
}
