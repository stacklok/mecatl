package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

const (
	yamlStringTag  = "!!str"
	yamlMappingTag = "!!map"
)

type operatorLearningSettings struct {
	path   string
	remote bool

	// beforeLock/afterRead are test-only synchronization seams. Production leaves them nil.
	beforeLock func()
	afterRead  func()
}

func learningSettingsForConfig(cfg config) *operatorLearningSettings {
	return newOperatorLearningSettings(cfg.transportMode == modeConnect)
}

func newOperatorLearningSettings(remote bool) *operatorLearningSettings {
	if remote {
		return &operatorLearningSettings{remote: true}
	}
	base := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if base == "" {
		return nil
	}
	return &operatorLearningSettings{path: filepath.Join(base, "mecatl", "settings.yaml")}
}

// Advance atomically advances Off → Review → Auto → Off and returns display-ready
// labels plus the honest restart instruction. The entire read-modify-write holds a
// stable cross-process flock.
func (s *operatorLearningSettings) Advance() (fromLabel, toLabel, restart string, err error) {
	if s.remote {
		return "", "", "", errors.New("completed-trajectory learning cannot be changed while connected to a remote server; edit learning.mode in the server host's settings.yaml and restart that server")
	}
	if s.path == "" {
		return "", "", "", errors.New("operator settings path is unavailable")
	}
	if err := s.withLockedDocument(func(doc *yaml.Node) error {
		current, err := learningMode(doc)
		if err != nil {
			return err
		}
		currentSensitivity, err := learningSensitivity(doc)
		if err != nil {
			return err
		}
		currentActivation, err := learningActivation(doc, current)
		if err != nil {
			return err
		}
		if s.afterRead != nil {
			s.afterRead()
		}
		next := current.Next()
		nextActivation, err := learningActivation(doc, next)
		if err != nil {
			return err
		}
		setLearningMode(doc, next)
		fromLabel = learningModeLabel(current) + " (sensitivity " + learningSensitivityLabel(currentSensitivity) + ", skills " + currentActivation.String() + ")"
		toLabel = learningModeLabel(next) + " (sensitivity " + learningSensitivityLabel(currentSensitivity) + ", skills " + nextActivation.String() + ")"
		return nil
	}); err != nil {
		return "", "", "", err
	}
	return fromLabel, toLabel, "saved; restart mecatui for it to take effect", nil
}

func (s *operatorLearningSettings) AdvanceSensitivity() (fromLabel, toLabel, restart string, err error) {
	if s.remote {
		return "", "", "", errors.New("learning sensitivity cannot be changed while connected to a remote server; edit learning.sensitivity in the server host's settings.yaml and restart that server")
	}
	if s.path == "" {
		return "", "", "", errors.New("operator settings path is unavailable")
	}
	if err := s.withLockedDocument(func(doc *yaml.Node) error {
		current, err := learningSensitivity(doc)
		if err != nil {
			return err
		}
		currentMode, err := learningMode(doc)
		if err != nil {
			return err
		}
		activation, err := learningActivation(doc, currentMode)
		if err != nil {
			return err
		}
		if s.afterRead != nil {
			s.afterRead()
		}
		next := current.Next()
		setLearningMode(doc, currentMode)
		setLearningSensitivity(doc, next)
		fromLabel = learningSensitivityLabel(current) + " (mode " + learningModeLabel(currentMode) + ", skills " + activation.String() + ")"
		toLabel = learningSensitivityLabel(next) + " (mode " + learningModeLabel(currentMode) + ", skills " + activation.String() + ")"
		return nil
	}); err != nil {
		return "", "", "", err
	}
	return fromLabel, toLabel, "saved; restart mecatui for it to take effect", nil
}

func (s *operatorLearningSettings) withLockedDocument(mutate func(*yaml.Node) error) (err error) {
	if err := rejectSymlinkPath(s.path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create operator settings dir: %w", err)
	}
	if err := rejectSymlinkPath(s.path); err != nil {
		return err
	}
	lockPath := s.path + ".lock"
	if err := rejectSymlinkPath(lockPath); err != nil {
		return err
	}
	lock := flock.New(lockPath)
	if s.beforeLock != nil {
		s.beforeLock()
	}
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock operator settings: %w", err)
	}
	defer func() {
		if unlockErr := lock.Unlock(); err == nil && unlockErr != nil {
			err = fmt.Errorf("unlock operator settings: %w", unlockErr)
		}
		_ = lock.Close()
	}()

	doc, err := s.readDocument()
	if err != nil {
		return err
	}
	if err := mutate(doc); err != nil {
		return err
	}
	return s.writeDocument(doc)
}

func learningSensitivityLabel(value learning.Sensitivity) string {
	switch value {
	case learning.Conservative:
		return "Conservative"
	case learning.Eager:
		return "Eager"
	default:
		return "Balanced"
	}
}

func learningModeLabel(mode learning.Mode) string {
	switch mode {
	case learning.Review:
		return "Review"
	case learning.Auto:
		return "Auto"
	default:
		return "Off"
	}
}

func (s *operatorLearningSettings) readDocument() (*yaml.Node, error) {
	if err := rejectSymlinkPath(s.path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read operator settings: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(b)) == 0 {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: yamlMappingTag}}}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("parse operator settings: invalid YAML")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("parse operator settings: multiple documents are not supported")
	}
	if err := validateSettingsDocument(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// validateSettingsDocument rejects ambiguous YAML before the atomic mutation.
// The branches mirror the strict nested schema and intentionally remain visible.
//
//nolint:gocyclo
func validateSettingsDocument(doc *yaml.Node) error {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return errors.New("parse operator settings: expected one YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Tag != yamlMappingTag {
		return errors.New("parse operator settings: top level must be a mapping")
	}
	if hasAlias(root) {
		return errors.New("parse operator settings: YAML aliases are not supported")
	}
	learningNode, err := uniqueMappingValue(root, "learning", false)
	if err != nil || learningNode == nil {
		return err
	}
	if learningNode.Kind != yaml.MappingNode || learningNode.Tag != yamlMappingTag {
		return errors.New("parse operator settings: learning must be a mapping")
	}
	for i := 0; i < len(learningNode.Content); i += 2 {
		key := learningNode.Content[i].Value
		if key != "mode" && key != "sensitivity" && key != "skills" && key != "automatic" {
			return fmt.Errorf("parse operator settings: unknown learning key %q", key)
		}
	}
	modeNode, err := uniqueMappingValue(learningNode, "mode", false)
	if err != nil {
		return err
	}
	if modeNode != nil {
		if modeNode.Kind != yaml.ScalarNode || modeNode.Tag != yamlStringTag {
			return errors.New("parse operator settings: learning.mode must be a string scalar")
		}
		if _, err := learning.ParseMode(modeNode.Value); err != nil {
			return fmt.Errorf("parse operator settings: %w", err)
		}
	}
	sensitivityNode, err := uniqueMappingValue(learningNode, "sensitivity", false)
	if err != nil {
		return err
	}
	if sensitivityNode != nil {
		if sensitivityNode.Kind != yaml.ScalarNode || sensitivityNode.Tag != yamlStringTag {
			return errors.New("parse operator settings: learning.sensitivity must be a string scalar")
		}
		if _, err := learning.ParseSensitivity(sensitivityNode.Value); err != nil {
			return fmt.Errorf("parse operator settings: %w", err)
		}
	}
	skillsNode, err := uniqueMappingValue(learningNode, "skills", false)
	if err != nil {
		return err
	}
	if skillsNode != nil {
		if skillsNode.Kind != yaml.MappingNode || skillsNode.Tag != yamlMappingTag {
			return errors.New("parse operator settings: learning.skills must be a mapping")
		}
		activationNode, activationErr := uniqueMappingValue(skillsNode, "activation", true)
		if activationErr != nil {
			return activationErr
		}
		if activationNode != nil {
			if activationNode.Kind != yaml.ScalarNode || activationNode.Tag != yamlStringTag {
				return errors.New("parse operator settings: learning.skills.activation must be a string scalar")
			}
			if _, parseErr := learning.ParseSkillActivationPolicy(activationNode.Value); parseErr != nil {
				return fmt.Errorf("parse operator settings: %w", parseErr)
			}
		}
	}
	automaticNode, err := uniqueMappingValue(learningNode, "automatic", false)
	if err != nil {
		return err
	}
	if automaticNode != nil {
		return validateLearningAutomatic(automaticNode)
	}
	return nil
}

func validateLearningAutomatic(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || node.Tag != yamlMappingTag {
		return errors.New("parse operator settings: learning.automatic must be a mapping")
	}
	allowed := map[string]bool{"cooldown": true, "window": true, "max_reflections": true, "max_tokens": true, "max_reflections_per_principal": true, "max_tokens_per_principal": true}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if seen[key] {
			return fmt.Errorf("parse operator settings: duplicate key %q", key)
		}
		seen[key] = true
		if !allowed[key] {
			return fmt.Errorf("parse operator settings: unknown learning.automatic key %q", key)
		}
		if key == "cooldown" || key == "window" {
			if value.Kind != yaml.ScalarNode || value.Tag != yamlStringTag {
				return fmt.Errorf("parse operator settings: learning.automatic.%s must be a duration string", key)
			}
			d, err := time.ParseDuration(value.Value)
			if err != nil || d < 0 {
				return fmt.Errorf("parse operator settings: invalid learning.automatic.%s", key)
			}
			if key == "window" && (d < time.Minute || d > 24*time.Hour) {
				return errors.New("parse operator settings: learning.automatic.window must be between 1m and 24h")
			}
			continue
		}
		var maximum int
		if err := value.Decode(&maximum); err != nil || maximum < 0 || maximum > 1_000_000_000 {
			return fmt.Errorf("parse operator settings: learning.automatic.%s must be a bounded nonnegative integer", key)
		}
	}
	return nil
}

func uniqueMappingValue(mapping *yaml.Node, wanted string, rejectUnknown bool) (*yaml.Node, error) {
	var found *yaml.Node
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag {
			return nil, errors.New("parse operator settings: mapping keys must be strings")
		}
		if _, ok := seen[key.Value]; ok {
			return nil, fmt.Errorf("parse operator settings: duplicate key %q", key.Value)
		}
		seen[key.Value] = struct{}{}
		if rejectUnknown && key.Value != wanted {
			return nil, fmt.Errorf("parse operator settings: unknown learning key %q", key.Value)
		}
		if key.Value == wanted {
			found = mapping.Content[i+1]
		}
	}
	return found, nil
}

func hasAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return true
	}
	for _, child := range node.Content {
		if hasAlias(child) {
			return true
		}
	}
	return false
}

func learningMode(doc *yaml.Node) (learning.Mode, error) {
	root := doc.Content[0]
	learningNode, err := uniqueMappingValue(root, "learning", false)
	if err != nil {
		return learning.Off, err
	}
	if learningNode == nil {
		return learning.Off, nil
	}
	modeNode, err := uniqueMappingValue(learningNode, "mode", false)
	if err != nil || modeNode == nil {
		return learning.Off, err
	}
	return learning.ParseMode(modeNode.Value)
}

func learningSensitivity(doc *yaml.Node) (learning.Sensitivity, error) {
	root := doc.Content[0]
	learningNode, err := uniqueMappingValue(root, "learning", false)
	if err != nil || learningNode == nil {
		return learning.Balanced, err
	}
	node, err := uniqueMappingValue(learningNode, "sensitivity", false)
	if err != nil || node == nil {
		return learning.Balanced, err
	}
	return learning.ParseSensitivity(node.Value)
}

func learningActivation(doc *yaml.Node, mode learning.Mode) (learning.SkillActivationPolicy, error) {
	root := doc.Content[0]
	learningNode, err := uniqueMappingValue(root, "learning", false)
	if err != nil || learningNode == nil {
		if mode == learning.Auto {
			return learning.SkillActivationValidated, err
		}
		return learning.SkillActivationEvaluated, err
	}
	skillsNode, err := uniqueMappingValue(learningNode, "skills", false)
	if err != nil || skillsNode == nil {
		if mode == learning.Auto {
			return learning.SkillActivationValidated, err
		}
		return learning.SkillActivationEvaluated, err
	}
	node, err := uniqueMappingValue(skillsNode, "activation", true)
	if err != nil || node == nil {
		if mode == learning.Auto {
			return learning.SkillActivationValidated, err
		}
		return learning.SkillActivationEvaluated, err
	}
	return learning.ParseSkillActivationPolicy(node.Value)
}

func setLearningSensitivity(doc *yaml.Node, value learning.Sensitivity) {
	root := doc.Content[0]
	learningNode, _ := uniqueMappingValue(root, "learning", false)
	if learningNode == nil {
		learningNode = &yaml.Node{Kind: yaml.MappingNode, Tag: yamlMappingTag}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "learning"}, learningNode)
	}
	node, _ := uniqueMappingValue(learningNode, "sensitivity", false)
	if node == nil {
		learningNode.Content = append(learningNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "sensitivity"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: value.String()})
		return
	}
	node.Value, node.Tag = value.String(), yamlStringTag
}

func setLearningMode(doc *yaml.Node, mode learning.Mode) {
	root := doc.Content[0]
	learningNode, _ := uniqueMappingValue(root, "learning", false)
	if learningNode == nil {
		learningNode = &yaml.Node{Kind: yaml.MappingNode, Tag: yamlMappingTag}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "learning"}, learningNode)
	}
	modeNode, _ := uniqueMappingValue(learningNode, "mode", false)
	if modeNode == nil {
		learningNode.Content = append(learningNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "mode"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: mode.String()})
		return
	}
	modeNode.Value = mode.String()
	modeNode.Tag = yamlStringTag
}

func (s *operatorLearningSettings) writeDocument(doc *yaml.Node) error {
	if err := rejectSymlinkPath(s.path); err != nil {
		return err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode operator settings: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode operator settings: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*.yaml")
	if err != nil {
		return fmt.Errorf("create operator settings temp file: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write operator settings: %w", err)
	}
	if _, err := tmp.Write(out.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write operator settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write operator settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write operator settings: %w", err)
	}
	if err := rejectSymlinkPath(s.path); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace operator settings: %w", err)
	}
	return nil
}

// rejectSymlinkPath rejects a symlink at the settings file or any existing parent
// component. The mutation never follows a user-controlled link or replaces one.
func rejectSymlinkPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve operator settings path: %w", err)
	}
	volume := filepath.VolumeName(abs)
	current := volume + string(filepath.Separator)
	rel := strings.TrimPrefix(abs, current)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect operator settings path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("operator settings path %q contains a symlink; refusing to modify it", current)
		}
	}
	return nil
}
