package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type harnessGeneration struct {
	resolver *harnessCommandResolver
	entry    *commandBindingEntry
}

func (g harnessGeneration) borrow() (func(), error) {
	g.resolver.mu.Lock()
	defer g.resolver.mu.Unlock()
	// Retirement bars resolver-level borrows, but the engine holding this
	// generation's owner lease may continue using it while that lease drains.
	if g.entry.refs == 0 || g.entry.binding == nil {
		return nil, fmt.Errorf("harness source generation is retired")
	}
	g.entry.refs++
	return g.resolver.release(g.entry), nil
}

func retainHarnessGeneration(cfg Config) (func() error, error) {
	generation, ok := cfg.harnessInstructions.(generationInstructions)
	if !ok {
		return nil, nil
	}
	release, err := generation.borrow()
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}

type generationCommands struct {
	harnessGeneration
	source server.CommandSourceBinding
}

func (g generationCommands) List(ctx context.Context) ([]prompt.Command, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, err
	}
	defer release()
	return g.source.List(ctx)
}
func (g generationCommands) Expand(ctx context.Context, input string) (string, bool, error) {
	release, err := g.borrow()
	if err != nil {
		return input, false, err
	}
	defer release()
	return g.source.Expand(ctx, input)
}

type generationInstructions struct {
	harnessGeneration
	source prompt.InstructionAssembler
	// filterSource retains every selected root when a combine source is split for per-run caching.
	filterSource  prompt.InstructionAssembler
	executionRoot string
	inherited     map[string]string
}

func (g generationInstructions) TargetScoped() bool {
	return g.source != nil && g.source.TargetScoped()
}

func (g generationInstructions) Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []prompt.InstructionManifest, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, nil, err
	}
	defer release()
	bound, selected := bindInstructionRoots(g.source, g.entry)
	if g.filterSource != nil {
		_, selected = bindInstructionRoots(g.filterSource, g.entry)
	}
	if state != nil {
		eligible := state.Scopes[:0]
		for _, scope := range state.Scopes {
			if successor, ok := g.inherited[scope.SourceID]; ok {
				scope.SourceID = successor
			}
			if selected[scope.SourceID] {
				eligible = append(eligible, scope)
			}
		}
		state.Scopes = eligible
	}
	return prompt.AssembleWithManifest(ctx, bound, directories, state, maxContentBytes)
}

// inheritedInstructionSources admits only corresponding selected roots from a live
// parent binding; session-scoped rebindings that choose another reader do not inherit.
func (r *harnessCommandResolver) inheritedInstructionSources(source session.SessionID, owner *session.Principal, profile string, destination *commandBindingEntry) map[string]string {
	if source == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parent := r.entries[source]
	if parent == nil || parent.retired || parent.binding == nil || destination == nil || destination.retired ||
		!sameHarnessPrincipal(parent.principal, owner) || parent.profile != profile ||
		!sameHarnessPrincipal(destination.principal, owner) || destination.profile != profile {
		return nil
	}
	old := parent.binding.(*resolvedCommandBinding).context
	newBinding := destination.binding.(*resolvedCommandBinding).context
	if old == nil || newBinding == nil {
		return nil
	}
	if !correspondingInstructionRoots(old.harnessInstructions, newBinding.harnessInstructions, nil, false) {
		return nil
	}
	oldRoots, _ := bindInstructionRoots(old.harnessInstructions, parent)
	newRoots, _ := bindInstructionRoots(newBinding.harnessInstructions, destination)
	mapping := make(map[string]string)
	if !correspondingInstructionRoots(oldRoots, newRoots, mapping, false) {
		return nil
	}
	return mapping
}

func correspondingInstructionRoots(old, current prompt.InstructionAssembler, mapping map[string]string, repository bool) bool {
	switch a := old.(type) {
	case prompt.RootAssembler:
		b, ok := current.(prompt.RootAssembler)
		return ok && correspondingRootAssembler(a, b, mapping, repository)
	case fixedInstructionAssembler:
		b, ok := current.(fixedInstructionAssembler)
		return ok && correspondingFixedInstructionRoots(a, b, mapping)
	case policyInstructionAssembler:
		b, ok := current.(policyInstructionAssembler)
		return ok && a.mode == b.mode && correspondingInstructionChildren(a.sources, b.sources, mapping)
	case prompt.MultiAssembler:
		b, ok := current.(prompt.MultiAssembler)
		return ok && correspondingInstructionChildren(a.Assemblers, b.Assemblers, mapping)
	default:
		return old == nil && current == nil
	}
}

func correspondingRootAssembler(old, current prompt.RootAssembler, mapping map[string]string, repository bool) bool {
	if old.Source == nil || current.Source == nil || old.SourceID == "" ||
		(old.SourceID != current.SourceID && mapping == nil) || old.SourcePrefix != current.SourcePrefix {
		return false
	}
	if !repository && (reflect.TypeOf(old.Source) != reflect.TypeOf(current.Source) ||
		!reflect.TypeOf(old.Source).Comparable() || old.Source != current.Source) {
		return false
	}
	if mapping != nil {
		mapping[old.SourceID] = current.SourceID
	}
	return true
}

func correspondingFixedInstructionRoots(old, current fixedInstructionAssembler, mapping map[string]string) bool {
	return old.id == current.id && old.projectAdmitted == current.projectAdmitted &&
		old.repositoryBinding == current.repositoryBinding && reflect.DeepEqual(old.provenance, current.provenance) &&
		correspondingInstructionRoots(old.inner, current.inner, mapping, old.repositoryBinding && current.repositoryBinding)
}

func correspondingInstructionChildren(old, current []prompt.InstructionAssembler, mapping map[string]string) bool {
	if len(old) != len(current) {
		return false
	}
	for i := range old {
		if !correspondingInstructionRoots(old[i], current[i], mapping, false) {
			return false
		}
	}
	return true
}

// The command resolver's published generation is the stable binding revision for
// this live session. Hash its identity so neither session IDs nor backend paths
// enter the logical manifest or model-facing scope metadata.
func bindInstructionRoots(a prompt.InstructionAssembler, entry *commandBindingEntry) (prompt.InstructionAssembler, map[string]bool) {
	selected := make(map[string]bool)
	var bind func(prompt.InstructionAssembler, HarnessSourceID) prompt.InstructionAssembler
	bind = func(source prompt.InstructionAssembler, registration HarnessSourceID) prompt.InstructionAssembler {
		switch v := source.(type) {
		case prompt.RootAssembler:
			if v.Source != nil && v.SourceID != "" {
				digest := sha256.Sum256([]byte(string(entry.id) + "\x00" + strconv.FormatUint(entry.revision, 10) + "\x00" + string(registration) + "\x00" + v.SourceID + "\x00" + v.SourcePrefix))
				v.SourceID = "binding:" + hex.EncodeToString(digest[:16])
				selected[v.SourceID] = true
			}
			return v
		case *prompt.RootAssembler:
			if v != nil {
				return bind(*v, registration)
			}
		case prompt.MultiAssembler:
			children := make([]prompt.InstructionAssembler, len(v.Assemblers))
			for i, child := range v.Assemblers {
				children[i] = bind(child, registration)
			}
			return prompt.MultiAssembler{Assemblers: children}
		case *prompt.MultiAssembler:
			if v != nil {
				return bind(*v, registration)
			}
		case fixedInstructionAssembler:
			v.inner = bind(v.inner, v.id)
			return v
		case policyInstructionAssembler:
			children := make([]prompt.InstructionAssembler, len(v.sources))
			for i, child := range v.sources {
				children[i] = bind(child, registration)
			}
			v.sources = children
			return v
		}
		return source
	}
	return bind(a, ""), selected
}

type childUnmappedInstructions struct{ source prompt.InstructionAssembler }

func (childUnmappedInstructions) TargetScoped() bool { return false }

func (g childUnmappedInstructions) Assemble(ctx context.Context, _ []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []prompt.InstructionManifest, error) {
	messages, rows, err := prompt.AssembleWithManifest(ctx, g.source, []string{"."}, state, maxContentBytes)
	if err != nil {
		return nil, nil, err
	}
	messages = append(messages, session.NewUserMessage("Project instructions: child workspace scope mapping unavailable; nested guidance is not automatically loaded. Selected root guidance remains available; ordinary tools remain available."))
	rows = append(rows, prompt.InstructionManifest{Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceProject})
	return messages, rows, nil
}

// childGenerationInstructions keeps the admitted parent's source generation alive.
type childGenerationInstructions struct {
	generationInstructions
}

func (g childGenerationInstructions) Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []prompt.InstructionManifest, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, nil, err
	}
	context.AfterFunc(ctx, release)
	return g.generationInstructions.Assemble(ctx, directories, state, maxContentBytes)
}

type generationSkills struct {
	harnessGeneration
	source tool.SkillSource
}

func (g generationSkills) ListSkills(ctx context.Context) ([]tool.SkillMeta, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, err
	}
	defer release()
	return g.source.ListSkills(ctx)
}
func (g generationSkills) SkillBody(ctx context.Context, name string) (string, error) {
	release, err := g.borrow()
	if err != nil {
		return "", err
	}
	defer release()
	return g.source.SkillBody(ctx, name)
}
func (g generationSkills) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, err
	}
	defer release()
	return g.source.ListSkillAssets(ctx, name)
}
func (g generationSkills) ReadSkillAsset(ctx context.Context, name, asset string) ([]byte, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, err
	}
	defer release()
	return g.source.ReadSkillAsset(ctx, name, asset)
}
