package app

import (
	"context"
	"errors"
	"testing"

	agentfs "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBuildEngineContextFactoryForwardsFrozenKeysToCatalog(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"tenant"}, Kinds: kinds})
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{
		ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"},
		Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			return hcAssembler("context source"), nil, nil
		},
	}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{
		ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"},
		Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return &hcCommands{values: map[string]string{}}, func() error { return nil }, nil
		},
	}}
	cfg.permResolver = buildPermResolver(cfg)
	if _, err := validateHarnessPolicy(cfg); err != nil {
		t.Fatal(err)
	}
	prepareHarnessProcessBindings(&cfg)
	if err := resolveProcessHarnessInstructions(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := resolveProcessHarnessSnapshots(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	commandResolver, err := buildConfiguredCommandResolver(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.harnessResolver = commandResolver.(*harnessCommandResolver)
	if cfg.harnessContextClose != nil {
		t.Cleanup(cfg.harnessContextClose)
	}

	provider := mockllm.New()
	reg := regForTest(provider, providerMock, "mock")
	store := memstore.New()
	_, _, _, _, _, _, _, assets, _, closeMCP, err := buildEngine(t.Context(), cfg, reg, provider, store, store, agentfs.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeMCP)
	if assets.sessionContextFactory == nil {
		t.Fatal("buildEngine did not install its context-engine factory")
	}

	_, err = assets.sessionContextFactory(t.Context(), "context-collision", nil, nil, server.ProviderSelector{}, nil,
		server.ProfileDefault, cfg.Workspace, session.ModeDefault,
		[]tool.Tool{classificationNamedTool("Read")}, []string{"Read"}, "")
	var collision *server.WorkspaceEnrollmentCollisionError
	if !errors.As(err, &collision) || collision.Key != "Read" || !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("context-factory catalog collision = %v, want typed failed-precondition for Read", err)
	}
}
