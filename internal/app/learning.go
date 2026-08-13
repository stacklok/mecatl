package app

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// foldLearningMode resolves the operator setting, legacy compatibility flag, and
// admitted project tighten-only ceiling. Project config can lower autonomy, never raise it.
func foldLearningMode(cfg Config) (Config, error) {
	mode := cfg.LearningMode
	resolver, _ := cfg.permResolver.(*permconfig.Resolver)
	operatorToken := resolver.OperatorLearningMode()
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
	cfg.operatorLearningMode = mode
	resolved := learningModeForWorkspace(cfg, cfg.Workspace)
	cfg.LearningMode = resolved
	cfg.diag().Log(context.Background(), port.LevelInfo, "automatic learning mode resolved", "mode", resolved.String())
	return cfg, nil
}

func learningModeForWorkspace(cfg Config, root string) learning.Mode {
	mode := cfg.operatorLearningMode
	resolver, _ := cfg.permResolver.(*permconfig.Resolver)
	if resolver == nil || root == "" || !projectIngestionAdmitted(cfg) {
		return mode
	}
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		cfg.diag().Log(context.Background(), port.LevelWarn, "learning: cannot read project mode; keeping operator mode", "workspace", root, "error", err)
		return mode
	}
	for _, token := range resolver.ProjectLearningModes(ws) {
		project, err := learning.ParseMode(token)
		if err != nil {
			cfg.diag().Log(context.Background(), port.LevelWarn, "learning: invalid project mode ignored", "workspace", root, "error", err)
			continue
		}
		if project < mode {
			mode = project
		} else if project > mode {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"learning: ignoring project mode that would raise operator autonomy",
				"operator_mode", mode.String(), "project_mode", project.String(), "workspace", root)
		}
	}
	return mode
}
