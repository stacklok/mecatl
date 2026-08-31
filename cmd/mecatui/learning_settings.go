package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml/ast"
	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

type operatorLearningSettings struct {
	path   string
	remote bool

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
	if err := s.withLockedDocument(func(doc *yamldiag.Document) error {
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
	if err := s.withLockedDocument(func(doc *yamldiag.Document) error {
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

func (s *operatorLearningSettings) withLockedDocument(mutate func(*yamldiag.Document) error) (err error) {
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

func (s *operatorLearningSettings) readDocument() (*yamldiag.Document, error) {
	if err := rejectSymlinkPath(s.path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read operator settings: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}\n")
	}
	doc, err := yamldiag.ParseSettingsDocument(b)
	if err != nil {
		return nil, errors.New(yamldiag.FormatDocumentError("parse operator settings: invalid YAML", err))
	}
	if err := validateSettingsDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

//nolint:gocyclo
func validateSettingsDocument(doc *yamldiag.Document) error {
	if doc == nil || doc.Mapping() == nil {
		return errors.New("parse operator settings: expected one YAML document")
	}
	root := doc.Mapping()
	learningNode, err := uniqueMappingValue(root, "learning", false)
	if err != nil || learningNode == nil {
		return err
	}
	learningMap, ok := learningNode.(*ast.MappingNode)
	if !ok {
		return errors.New("parse operator settings: learning must be a mapping")
	}
	for _, entry := range learningMap.Values {
		key, ok := scalarValue(entry.Key)
		if !ok || (key != "mode" && key != "sensitivity" && key != "skills" && key != "automatic") {
			return errors.New("parse operator settings: unknown learning key")
		}
	}
	mode, err := uniqueMappingValue(learningMap, "mode", false)
	if err != nil {
		return err
	}
	if mode != nil {
		if value, ok := scalarValue(mode); !ok {
			return errors.New("parse operator settings: learning.mode must be a string scalar")
		} else if _, err := learning.ParseMode(value); err != nil {
			return errors.New("parse operator settings: invalid learning.mode")
		}
	}
	sensitivity, err := uniqueMappingValue(learningMap, "sensitivity", false)
	if err != nil {
		return err
	}
	if sensitivity != nil {
		if value, ok := scalarValue(sensitivity); !ok {
			return errors.New("parse operator settings: learning.sensitivity must be a string scalar")
		} else if _, err := learning.ParseSensitivity(value); err != nil {
			return errors.New("parse operator settings: invalid learning.sensitivity")
		}
	}
	skills, err := uniqueMappingValue(learningMap, "skills", false)
	if err != nil {
		return err
	}
	if skills != nil {
		skillsMap, ok := skills.(*ast.MappingNode)
		if !ok {
			return errors.New("parse operator settings: learning.skills must be a mapping")
		}
		activation, err := uniqueMappingValue(skillsMap, "activation", true)
		if err != nil {
			return err
		}
		if activation != nil {
			if value, ok := scalarValue(activation); !ok {
				return errors.New("parse operator settings: learning.skills.activation must be a string scalar")
			} else if _, err := learning.ParseSkillActivationPolicy(value); err != nil {
				return errors.New("parse operator settings: invalid learning.skills.activation")
			}
		}
	}
	automatic, err := uniqueMappingValue(learningMap, "automatic", false)
	if err != nil {
		return err
	}
	if automatic != nil {
		return validateLearningAutomatic(automatic)
	}
	return nil
}

func validateLearningAutomatic(node ast.Node) error {
	mapping, ok := node.(*ast.MappingNode)
	if !ok {
		return errors.New("parse operator settings: learning.automatic must be a mapping")
	}
	allowed := map[string]bool{"cooldown": true, "window": true, "max_reflections": true, "max_tokens": true, "max_reflections_per_principal": true, "max_tokens_per_principal": true}
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := scalarValue(entry.Key)
		if !ok {
			return errors.New("parse operator settings: mapping keys must be strings")
		}
		if seen[key] {
			return errors.New("parse operator settings: duplicate learning.automatic key")
		}
		seen[key] = true
		if !allowed[key] {
			return errors.New("parse operator settings: unknown learning.automatic key")
		}
		value, ok := automaticScalarValue(entry.Value)
		if !ok {
			return fmt.Errorf("parse operator settings: learning.automatic.%s must be a bounded nonnegative integer", key)
		}
		if key == "cooldown" || key == "window" {
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 {
				return fmt.Errorf("parse operator settings: invalid learning.automatic.%s", key)
			}
			if key == "window" && (d < time.Minute || d > 24*time.Hour) {
				return errors.New("parse operator settings: learning.automatic.window must be between 1m and 24h")
			}
			continue
		}
		maximum, err := strconv.Atoi(value)
		if err != nil || maximum < 0 || maximum > 1_000_000_000 {
			return fmt.Errorf("parse operator settings: learning.automatic.%s must be a bounded nonnegative integer", key)
		}
	}
	return nil
}

func automaticScalarValue(node ast.Node) (string, bool) {
	if value, ok := scalarValue(node); ok {
		return value, true
	}
	integer, ok := node.(*ast.IntegerNode)
	if !ok {
		return "", false
	}
	switch value := integer.Value.(type) {
	case int64:
		return strconv.FormatInt(value, 10), true
	case uint64:
		return strconv.FormatUint(value, 10), true
	default:
		return "", false
	}
}

func scalarValue(node ast.Node) (string, bool) {
	value, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func uniqueMappingValue(mapping *ast.MappingNode, wanted string, rejectUnknown bool) (ast.Node, error) {
	var found ast.Node
	seen := make(map[string]struct{}, len(mapping.Values))
	for _, entry := range mapping.Values {
		key, ok := scalarValue(entry.Key)
		if !ok {
			return nil, errors.New("parse operator settings: mapping keys must be strings")
		}
		if _, ok := seen[key]; ok {
			return nil, errors.New("parse operator settings: duplicate mapping key")
		}
		seen[key] = struct{}{}
		if rejectUnknown && key != wanted {
			return nil, errors.New("parse operator settings: unknown learning key")
		}
		if key == wanted {
			found = entry.Value
		}
	}
	return found, nil
}

func learningMode(doc *yamldiag.Document) (learning.Mode, error) {
	learningNode, err := uniqueMappingValue(doc.Mapping(), "learning", false)
	if err != nil || learningNode == nil {
		return learning.Off, err
	}
	mapping, ok := learningNode.(*ast.MappingNode)
	if !ok {
		return learning.Off, errors.New("parse operator settings: learning must be a mapping")
	}
	node, err := uniqueMappingValue(mapping, "mode", false)
	if err != nil || node == nil {
		return learning.Off, err
	}
	value, ok := scalarValue(node)
	if !ok {
		return learning.Off, errors.New("parse operator settings: learning.mode must be a string scalar")
	}
	return learning.ParseMode(value)
}
func learningSensitivity(doc *yamldiag.Document) (learning.Sensitivity, error) {
	learningNode, err := uniqueMappingValue(doc.Mapping(), "learning", false)
	if err != nil || learningNode == nil {
		return learning.Balanced, err
	}
	mapping, ok := learningNode.(*ast.MappingNode)
	if !ok {
		return learning.Balanced, errors.New("parse operator settings: learning must be a mapping")
	}
	node, err := uniqueMappingValue(mapping, "sensitivity", false)
	if err != nil || node == nil {
		return learning.Balanced, err
	}
	value, ok := scalarValue(node)
	if !ok {
		return learning.Balanced, errors.New("parse operator settings: learning.sensitivity must be a string scalar")
	}
	return learning.ParseSensitivity(value)
}
func learningActivation(doc *yamldiag.Document, mode learning.Mode) (learning.SkillActivationPolicy, error) {
	fallback := learning.SkillActivationEvaluated
	if mode == learning.Auto {
		fallback = learning.SkillActivationValidated
	}
	learningNode, err := uniqueMappingValue(doc.Mapping(), "learning", false)
	if err != nil || learningNode == nil {
		return fallback, err
	}
	mapping, ok := learningNode.(*ast.MappingNode)
	if !ok {
		return fallback, errors.New("parse operator settings: learning must be a mapping")
	}
	skills, err := uniqueMappingValue(mapping, "skills", false)
	if err != nil || skills == nil {
		return fallback, err
	}
	skillsMap, ok := skills.(*ast.MappingNode)
	if !ok {
		return fallback, errors.New("parse operator settings: learning.skills must be a mapping")
	}
	node, err := uniqueMappingValue(skillsMap, "activation", true)
	if err != nil || node == nil {
		return fallback, err
	}
	value, ok := scalarValue(node)
	if !ok {
		return fallback, errors.New("parse operator settings: learning.skills.activation must be a string scalar")
	}
	return learning.ParseSkillActivationPolicy(value)
}

func parsedMappingValue(data string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(data))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static YAML")
	}
	return doc.Mapping().Values[0]
}
func setLearningSensitivity(doc *yamldiag.Document, value learning.Sensitivity) {
	setLearningValue(doc, "sensitivity", value.String())
}
func setLearningMode(doc *yamldiag.Document, mode learning.Mode) {
	setLearningValue(doc, "mode", mode.String())
}
func setLearningValue(doc *yamldiag.Document, key, value string) {
	learningNode, _ := uniqueMappingValue(doc.Mapping(), "learning", false)
	if learningNode == nil {
		entry := parsedMappingValue("learning: {}\n")
		doc.Mapping().Values = append(doc.Mapping().Values, entry)
		learningNode = entry.Value
	}
	mapping := learningNode.(*ast.MappingNode)
	replacement := parsedMappingValue(key + ": " + value + "\n")
	for _, entry := range mapping.Values {
		existing, _ := scalarValue(entry.Key)
		if existing == key {
			_ = entry.Replace(replacement.Value)
			return
		}
	}
	mapping.Values = append(mapping.Values, replacement)
}

func (s *operatorLearningSettings) writeDocument(doc *yamldiag.Document) error {
	if err := rejectSymlinkPath(s.path); err != nil {
		return err
	}
	out := []byte(doc.String())
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
	if _, err := tmp.Write(out); err != nil {
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
