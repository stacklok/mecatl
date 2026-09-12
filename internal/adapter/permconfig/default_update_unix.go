//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package permconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml/ast"
	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

var defaultUpdateTestHook func(string) error

// UpdateDefaults preserves the settings AST while changing only
// models.default_provider and models.default.
//
//nolint:gocyclo // The linear preserving-write protocol keeps outcome classification explicit.
func UpdateDefaults(ctx context.Context, path string, update DefaultUpdate) (authfile.CommitState, error) {
	if !ValidProviderID(update.Provider) || strings.TrimSpace(update.Model) == "" {
		return authfile.CommitNotApplied, errors.New("settings default update: invalid provider or model")
	}
	if path == "" {
		return authfile.CommitNotApplied, errors.New("settings default update: path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: path is unavailable")
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: create parent")
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := lockCtx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: acquire lock: %w", err)
	}
	lockPath := abs + ".lock"
	if err := validateSettingsLeaf(lockPath, true); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: unsafe lock file")
	}
	lock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil || !locked {
		if lockCtx.Err() != nil {
			err = lockCtx.Err()
		}
		if err == nil {
			err = errors.New("lock not acquired")
		}
		_ = lock.Close()
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock(); _ = lock.Close() }()
	if err := validateSettingsLeaf(lockPath, false); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: unsafe lock file")
	}

	before, err := readSettingsTarget(abs)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	out, noop, err := mutateDefaults(before.data, update)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	if noop {
		return authfile.CommitNoop, nil
	}
	if len(out) > maxConfigBytes {
		return authfile.CommitNotApplied, errors.New("settings default update: output exceeds size limit")
	}
	tmp, err := os.CreateTemp(parent, ".settings-default-*.tmp")
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: create temporary file")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return authfile.CommitNotApplied, errors.New("settings default update: protect temporary file")
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return authfile.CommitNotApplied, errors.New("settings default update: write temporary file")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return authfile.CommitNotApplied, errors.New("settings default update: sync temporary file")
	}
	if err := tmp.Close(); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: close temporary file")
	}
	if defaultUpdateTestHook != nil {
		if err := defaultUpdateTestHook("before-compare"); err != nil {
			return authfile.CommitNotApplied, errors.New("settings default update: prepare comparison")
		}
	}
	current, err := readSettingsTarget(abs)
	if err != nil || !sameSettingsTarget(before, current) {
		return authfile.CommitNotApplied, errors.New("settings default update: target changed before replacement")
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: replace target")
	}
	if defaultUpdateTestHook != nil {
		if err := defaultUpdateTestHook("after-rename"); err != nil {
			return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: sync parent after replacement")
		}
	}
	dir, err := os.Open(parent)
	if err != nil {
		return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: open parent after replacement")
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil || closeErr != nil {
		return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: sync or close parent after replacement")
	}
	return authfile.CommitDurable, nil
}

type settingsTarget struct {
	data   []byte
	info   os.FileInfo
	exists bool
}

func readSettingsTarget(path string) (settingsTarget, error) {
	if err := validateSettingsLeaf(path, false); errors.Is(err, os.ErrNotExist) {
		return settingsTarget{}, nil
	} else if err != nil {
		return settingsTarget{}, errors.New("unsafe target file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return settingsTarget{}, errors.New("read target file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !settingsPrivateFile(info) {
		return settingsTarget{}, errors.New("unsafe target file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return settingsTarget{}, errors.New("target exceeds size limit or cannot be read")
	}
	return settingsTarget{data: data, info: info, exists: true}, nil
}

func validateSettingsLeaf(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if createErr == nil {
			createErr = file.Close()
		}
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return createErr
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !settingsPrivateFile(info) {
		return errors.New("unsafe ownership, mode, or type")
	}
	return nil
}

func settingsPrivateFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	euid := os.Geteuid()
	return euid >= 0 && stat.Uid == uint32(euid) // #nosec G115 -- nonnegative is checked before conversion.
}

func sameSettingsTarget(a, b settingsTarget) bool {
	if a.exists != b.exists || !bytes.Equal(a.data, b.data) {
		return false
	}
	if !a.exists {
		return true
	}
	return os.SameFile(a.info, b.info)
}

func mutateDefaults(data []byte, update DefaultUpdate) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}\n")
	}
	if err := ValidateYAML(data); err != nil {
		return nil, false, errors.New("settings document is invalid")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, false, errors.New("settings document is invalid or ambiguous")
	}
	modelsNode, err := uniqueDefaultValue(doc.Mapping(), "models")
	if err != nil {
		return nil, false, err
	}
	if modelsNode == nil {
		entry := defaultStaticEntry("models: {}\n")
		doc.Mapping().Values = append(doc.Mapping().Values, entry)
		modelsNode = entry.Value
	}
	models, ok := modelsNode.(*ast.MappingNode)
	if !ok {
		return nil, false, errors.New("models must be a mapping")
	}
	providerNode, err := uniqueDefaultValue(models, "default_provider")
	if err != nil {
		return nil, false, err
	}
	modelNode, err := uniqueDefaultValue(models, "default")
	if err != nil {
		return nil, false, err
	}
	provider, providerOK := defaultString(providerNode)
	model, modelOK := defaultString(modelNode)
	if providerNode != nil && !providerOK || modelNode != nil && !modelOK {
		return nil, false, errors.New("models defaults must be string scalars")
	}
	if provider == update.Provider && model == update.Model {
		return data, true, nil
	}
	setDefaultScalar(models, "default_provider", update.Provider)
	setDefaultScalar(models, "default", update.Model)
	out := []byte(doc.String())
	if err := ValidateYAML(out); err != nil {
		return nil, false, errors.New("updated settings document is invalid")
	}
	return out, false, nil
}

func uniqueDefaultValue(mapping *ast.MappingNode, wanted string) (ast.Node, error) {
	var found ast.Node
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := defaultString(entry.Key)
		if !ok || seen[key] {
			return nil, errors.New("settings document has duplicate or non-string mapping key")
		}
		seen[key] = true
		if key == wanted {
			found = entry.Value
		}
	}
	return found, nil
}

func setDefaultScalar(mapping *ast.MappingNode, key, value string) {
	replacement := defaultStaticEntry(key + ": " + defaultQuote(value) + "\n")
	for _, entry := range mapping.Values {
		existing, _ := defaultString(entry.Key)
		if existing == key {
			_ = entry.Replace(replacement.Value)
			return
		}
	}
	mapping.Values = append(mapping.Values, replacement)
}

func defaultString(node ast.Node) (string, bool) {
	value, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func defaultStaticEntry(text string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(text))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static settings YAML")
	}
	return doc.Mapping().Values[0]
}

func defaultQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
