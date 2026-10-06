package app

import (
	"context"
	"fmt"

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
}

func (g generationInstructions) Assemble(ctx context.Context) ([]session.Message, error) {
	messages, _, err := g.AssembleWithManifest(ctx)
	return messages, err
}
func (g generationInstructions) AssembleWithManifest(ctx context.Context) ([]session.Message, []prompt.InstructionManifest, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, nil, err
	}
	defer release()
	return prompt.AssembleWithManifest(ctx, g.source)
}

type childGenerationInstructions struct {
	generationInstructions
}

func (g childGenerationInstructions) Assemble(ctx context.Context) ([]session.Message, error) {
	messages, _, err := g.AssembleWithManifest(ctx)
	return messages, err
}

func (g childGenerationInstructions) AssembleWithManifest(ctx context.Context) ([]session.Message, []prompt.InstructionManifest, error) {
	release, err := g.borrow()
	if err != nil {
		return nil, nil, err
	}
	context.AfterFunc(ctx, release)
	// Cancellation releases the run hold, not an in-flight backend operation.
	return g.generationInstructions.AssembleWithManifest(ctx)
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
