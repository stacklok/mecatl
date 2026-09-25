package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// Process binders run once for the Build. Later session compositions borrow the
// same source while Build retains sole ownership of its cleanup.
func cacheProcessHarness[T any](registrations []HarnessSourceRegistration[T], snapshot func(context.Context, T) (T, error)) ([]HarnessSourceRegistration[T], func() error) {
	out := append([]HarnessSourceRegistration[T](nil), registrations...)
	var closers []func() error
	for i, registration := range out {
		if registration.Scope != HarnessSourceScopeProcess {
			continue
		}
		var mu sync.Mutex
		var source T
		var cleanup func() error
		var pending chan struct{}
		ready := false
		closed := false
		out[i].Bind = func(ctx context.Context, _ HarnessSourceScope) (T, func() error, error) {
			for {
				mu.Lock()
				if closed {
					mu.Unlock()
					var zero T
					return zero, nil, fmt.Errorf("process harness source %q is closed", registration.ID)
				}
				if ready {
					cached := source
					mu.Unlock()
					return cached, nil, nil
				}
				if pending != nil {
					done := pending
					mu.Unlock()
					select {
					case <-ctx.Done():
						var zero T
						return zero, nil, ctx.Err()
					case <-done:
						continue
					}
				}
				attempt := make(chan struct{})
				pending = attempt
				mu.Unlock()

				bound, boundCleanup, err := registration.Bind(ctx, HarnessSourceScope{})
				if err == nil && snapshot != nil {
					bound, err = snapshot(ctx, bound)
				}
				if err == nil {
					err = ctx.Err()
				}

				mu.Lock()
				pending = nil
				closedNow := closed
				if err == nil && !closedNow {
					source, cleanup, ready = bound, boundCleanup, true
				}
				close(attempt)
				mu.Unlock()

				if (err != nil || closedNow) && boundCleanup != nil {
					_ = boundCleanup()
				}
				if closedNow {
					var zero T
					return zero, nil, fmt.Errorf("process harness source %q is closed", registration.ID)
				}
				if err != nil {
					var zero T
					return zero, nil, err
				}
				return bound, nil, nil
			}
		}
		closers = append(closers, func() error {
			mu.Lock()
			closed = true
			owned := cleanup
			cleanup = nil
			mu.Unlock()
			if owned != nil {
				return owned()
			}
			return nil
		})
	}
	return out, func() error {
		closeHarnessCleanups(closers)
		return nil
	}
}

func prepareHarnessProcessBindings(cfg *Config) {
	var closers []func() error
	var closeProcess func() error
	cfg.HarnessInstructionSources, closeProcess = cacheProcessHarness(cfg.HarnessInstructionSources, nil)
	closers = append(closers, closeProcess)
	cfg.HarnessRulesSources, closeProcess = cacheProcessHarness(cfg.HarnessRulesSources, func(ctx context.Context, source prompt.RulesSource) (prompt.RulesSource, error) {
		if source == nil {
			return nil, fmt.Errorf("nil rules source")
		}
		rules, err := source.ListRules(ctx)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return frozenHarnessRules{rules: rules, err: err}, nil
	})
	closers = append(closers, closeProcess)
	cfg.HarnessSkillSources, closeProcess = cacheProcessHarness(cfg.HarnessSkillSources, func(ctx context.Context, source tool.SkillSource) (tool.SkillSource, error) {
		if source == nil {
			return nil, fmt.Errorf("nil skill source")
		}
		metas, err := source.ListSkills(ctx)
		if err != nil {
			return nil, err
		}
		winners := make(map[string]tool.SkillSource, len(metas))
		for _, meta := range metas {
			winners[meta.Name] = source
		}
		return &resolvedSkillSource{metas: metas, winners: winners}, nil
	})
	closers = append(closers, closeProcess)
	cfg.HarnessAgentDefSources, closeProcess = cacheProcessHarness(cfg.HarnessAgentDefSources, func(ctx context.Context, source tool.AgentDefSource) (tool.AgentDefSource, error) {
		if source == nil {
			return nil, fmt.Errorf("nil agent source")
		}
		defs, err := source.ListAgentDefs(ctx)
		return &resolvedAgentSource{defs: defs}, err
	})
	closers = append(closers, closeProcess)
	previousClose := cfg.harnessContextClose
	cfg.harnessContextClose = func() {
		closeHarnessCleanups(closers)
		if previousClose != nil {
			previousClose()
		}
	}
}

func harnessBindingScope(cfg Config, executionFiles bool) HarnessSourceScope {
	if cfg.harnessScope == nil {
		return HarnessSourceScope{}
	}
	scope := HarnessSourceScope{
		SessionID: cfg.harnessScope.SessionID,
		Principal: cfg.harnessScope.Principal.Clone(),
		Profile:   cfg.harnessScope.Profile,
	}
	if executionFiles {
		scope.AcquireExecutionFiles = cfg.harnessScope.AcquireExecutionFiles
	}
	return scope
}

type frozenHarnessRules struct {
	rules []prompt.Rule
	err   error
}

func (s frozenHarnessRules) ListRules(context.Context) ([]prompt.Rule, error) {
	return append([]prompt.Rule(nil), s.rules...), s.err
}

func (r *harnessCommandResolver) bindSessionSources(ctx context.Context, scope HarnessSourceScope) (*Config, func() error, error) {
	if r.sourceConfig == nil {
		return nil, nil, nil
	}
	cfg := *r.sourceConfig
	cfg.harnessScope = &scope
	cfg.harnessContextClose = nil
	if err := resolveProcessHarnessInstructions(ctx, &cfg); err != nil {
		return nil, nil, err
	}
	if err := resolveProcessHarnessSnapshots(ctx, &cfg); err != nil {
		if cfg.harnessContextClose != nil {
			cfg.harnessContextClose()
		}
		return nil, nil, err
	}
	return &cfg, func() error {
		if cfg.harnessContextClose != nil {
			cfg.harnessContextClose()
		}
		return nil
	}, nil
}

func replaceHarnessInstructions(base prompt.InstructionAssembler, cfg Config) prompt.InstructionAssembler {
	if multi, ok := base.(prompt.MultiAssembler); ok {
		children := make([]prompt.InstructionAssembler, 0, len(multi.Assemblers))
		for _, child := range multi.Assemblers {
			switch child.(type) {
			case policyInstructionAssembler, prompt.RootAssembler:
				children = append(children, cfg.harnessInstructions)
			case prompt.RulesAssembler:
				children = append(children, prompt.RulesAssembler{Src: cfg.harnessRules})
			default:
				children = append(children, child)
			}
		}
		return prompt.NewMultiAssembler(children...)
	}
	return prompt.NewMultiAssembler(cfg.harnessInstructions, prompt.RulesAssembler{Src: cfg.harnessRules})
}
