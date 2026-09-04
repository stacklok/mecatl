package app

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

const (
	defaultLearningCooldown = 10 * time.Minute
	defaultLearningWindow   = time.Hour
)

// LearningAutomaticConfig is the automatic-reflection budget applied by the
// configured durable admission ledger.
type LearningAutomaticConfig struct {
	Cooldown                   time.Duration
	Window                     time.Duration
	MaxReflections             int
	MaxTokens                  int
	MaxReflectionsPerPrincipal int
	MaxTokensPerPrincipal      int
}

func defaultLearningAutomaticConfig() LearningAutomaticConfig {
	return LearningAutomaticConfig{Cooldown: defaultLearningCooldown, Window: defaultLearningWindow,
		MaxReflections: 8, MaxTokens: 100000, MaxReflectionsPerPrincipal: 4, MaxTokensPerPrincipal: 50000}
}

// foldLearningMode resolves the operator setting, legacy compatibility flag, and
// admitted project tighten-only ceiling. Project config can lower autonomy, never raise it.
func foldLearningMode(cfg Config) (Config, error) {
	mode := cfg.LearningMode
	activation := cfg.SkillActivationPolicy.Effective()
	activationExplicit := cfg.SkillActivationPolicy.Valid()
	sensitivity := cfg.LearningSensitivity
	if sensitivity == learning.SensitivityUnset {
		sensitivity = learning.Balanced
	}
	automatic := defaultLearningAutomaticConfig()
	resolver, _ := cfg.permResolver.(*permconfig.Resolver)
	operator := resolver.OperatorLearning()
	operatorToken := ""
	if operator != nil {
		operatorToken = operator.Mode
		if operator.Sensitivity != "" {
			parsed, err := learning.ParseSensitivity(operator.Sensitivity)
			if err != nil {
				return cfg, err
			}
			sensitivity = parsed
		}
		if operator.Skills != nil && operator.Skills.Activation != "" {
			parsed, err := learning.ParseSkillActivationPolicy(operator.Skills.Activation)
			if err != nil {
				return cfg, err
			}
			activation, activationExplicit = parsed, true
		}
		if a := operator.Automatic; a != nil {
			automatic = LearningAutomaticConfig{Cooldown: a.Cooldown, Window: a.Window,
				MaxReflections: a.MaxReflections, MaxTokens: a.MaxTokens,
				MaxReflectionsPerPrincipal: a.MaxReflectionsPerPrincipal,
				MaxTokensPerPrincipal:      a.MaxTokensPerPrincipal}
		}
	}
	if operatorToken != "" {
		parsed, err := learning.ParseMode(operatorToken)
		if err != nil {
			return cfg, err
		}
		mode = parsed
	}
	if cfg.UserModelReview {
		if operatorToken != "" && mode != learning.Auto {
			return cfg, fmt.Errorf("learning: legacy --user-model-review conflicts with learning.mode=%s; remove the legacy flag or set learning.mode: auto", mode)
		}
		mode = learning.Auto
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"--user-model-review is deprecated; use learning.mode: auto in operator settings.yaml")
	}
	if !activationExplicit && mode == learning.Auto {
		activation = learning.SkillActivationValidated
	}
	cfg.operatorLearningMode = mode
	cfg.operatorLearningSensitivity = sensitivity
	cfg.operatorSkillActivationPolicy = activation
	cfg.LearningAutomatic = automatic
	resolved, resolvedSensitivity, resolvedActivation := learningPolicyForWorkspace(cfg, cfg.Workspace)
	cfg.LearningMode = resolved
	cfg.LearningSensitivity = resolvedSensitivity
	cfg.SkillActivationPolicy = resolvedActivation
	cfg.diag().Log(context.Background(), port.LevelInfo, "automatic learning policy resolved",
		"mode", resolved.String(), "sensitivity", resolvedSensitivity.String(), "skill_activation", resolvedActivation.String(), "skill_evaluator", cfg.SkillEvaluator != nil,
		"cooldown", automatic.Cooldown, "window", automatic.Window,
		"max_reflections", automatic.MaxReflections, "max_tokens", automatic.MaxTokens,
		"max_reflections_per_principal", automatic.MaxReflectionsPerPrincipal,
		"max_tokens_per_principal", automatic.MaxTokensPerPrincipal)
	if cfg.UserModelReviewInterval > 1 {
		cfg.diag().Log(context.Background(), port.LevelWarn, "--user-model-review-interval is deprecated; it now down-samples only admitted weighted reflections", "interval", cfg.UserModelReviewInterval)
	}
	return cfg, nil
}

//nolint:gocyclo // folds independent mode, sensitivity, and activation tighten-only axes
func learningPolicyForWorkspace(cfg Config, root string) (learning.Mode, learning.Sensitivity, learning.SkillActivationPolicy) {
	mode := cfg.operatorLearningMode
	sensitivity := cfg.operatorLearningSensitivity
	activation := cfg.operatorSkillActivationPolicy.Effective()
	resolver, _ := cfg.permResolver.(*permconfig.Resolver)
	if resolver == nil || !projectIngestionAdmittedForRoot(cfg, root) {
		return mode, sensitivity, activation
	}
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		cfg.diag().Log(context.Background(), port.LevelWarn, "learning: cannot read project policy; keeping operator policy", "workspace", root, "error", err)
		return mode, sensitivity, activation
	}
	for _, setting := range resolver.ProjectLearningSettings(ws) {
		if setting.Automatic != nil {
			cfg.diag().Log(context.Background(), port.LevelWarn, "learning: ignoring project automatic limits; learning.automatic is operator-only", "workspace", root)
		}
		if token := setting.Mode; token != "" {
			project, err := learning.ParseMode(token)
			if err != nil {
				cfg.diag().Log(context.Background(), port.LevelWarn, "learning: invalid project mode ignored", "workspace", root, "error", err)
			} else if project < mode {
				mode = project
			} else if project > mode {
				cfg.diag().Log(context.Background(), port.LevelWarn,
					"learning: ignoring project mode that would raise operator autonomy",
					"operator_mode", mode.String(), "project_mode", project.String(), "workspace", root)
			}
		}
		if setting.Skills != nil && setting.Skills.Activation != "" {
			project, err := learning.ParseSkillActivationPolicy(setting.Skills.Activation)
			if err != nil {
				cfg.diag().Log(context.Background(), port.LevelWarn, "learning: invalid project skill activation ignored", "workspace", root, "error", err)
			} else if activation == learning.SkillActivationValidated && project == learning.SkillActivationEvaluated {
				activation = project
			} else if project == learning.SkillActivationValidated && activation == learning.SkillActivationEvaluated {
				cfg.diag().Log(context.Background(), port.LevelWarn, "learning: ignoring project skill activation that would lower assurance", "workspace", root)
			}
		}
		if token := setting.Sensitivity; token != "" {
			project, err := learning.ParseSensitivity(token)
			if err != nil {
				cfg.diag().Log(context.Background(), port.LevelWarn, "learning: invalid project sensitivity ignored", "workspace", root, "error", err)
			} else if project < sensitivity {
				sensitivity = project
			} else if project > sensitivity {
				cfg.diag().Log(context.Background(), port.LevelWarn, "learning: ignoring project sensitivity that would raise automatic admission", "workspace", root)
			}
		}
	}
	return mode, sensitivity, activation
}
