//go:build linux || darwin

package permconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

var defaultUpdateTestHook func(string) error

func preflightDefaultsUpdateTarget(path string) error {
	if path == "" {
		return errors.New("path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("path is unavailable")
	}
	parentPath := filepath.Dir(abs)
	parent, err := filepath.EvalSymlinks(parentPath)
	conventional := filepath.Join(xdgconfig.UserConfigDir(xdgconfig.OSEnv), UserSettingsRelPath)
	if errors.Is(err, os.ErrNotExist) && filepath.Clean(abs) == filepath.Clean(conventional) {
		base, baseErr := filepath.EvalSymlinks(filepath.Dir(parentPath))
		if baseErr != nil {
			return errors.New("conventional config directory is unavailable")
		}
		physical := filepath.Join(base, filepath.Base(parentPath))
		if _, statErr := os.Lstat(physical); errors.Is(statErr, os.ErrNotExist) {
			return nil
		} else if statErr != nil {
			return errors.New("settings parent is unavailable")
		}
		parent, err = physical, nil
	}
	if err != nil {
		return errors.New("parent must already exist")
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("open settings parent")
	}
	defer func() { _ = unix.Close(parentFD) }()
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil || !settingsPrivateDir(&parentStat) {
		return errors.New("settings parent must be an owner-only directory")
	}
	target, err := readSettingsTarget(parentFD, filepath.Base(abs))
	if err != nil || !target.exists {
		return err
	}
	_, _, err = mutateDefaults(target.data, DefaultUpdate{Provider: "openai", Model: "preflight"})
	return err
}

// UpdateDefaults preserves the settings AST while changing only
// models.default_provider and models.default.
//
//nolint:gocyclo // The linear preserving-write protocol keeps outcome classification explicit.
func UpdateDefaults(ctx context.Context, path string, update DefaultUpdate) (state authfile.CommitState, result error) {
	if !providerIDPattern.MatchString(update.Provider) || strings.TrimSpace(update.Model) == "" {
		return authfile.CommitNotApplied, errors.New("settings default update: invalid provider or model")
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	parent, leaf, err := canonicalSettingsParent(path)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: open parent")
	}
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil || !settingsPrivateDir(&parentStat) {
		_ = unix.Close(parentFD)
		return authfile.CommitNotApplied, errors.New("settings default update: parent must be an owner-only directory")
	}
	defer func() {
		if closeErr := unix.Close(parentFD); closeErr != nil && result == nil {
			if state == authfile.CommitDurable {
				state, result = authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: close parent after replacement")
			} else {
				state, result = authfile.CommitNotApplied, errors.New("settings default update: close parent")
			}
		}
	}()

	before, err := readSettingsTarget(parentFD, leaf)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	out, noop, err := mutateDefaults(before.data, update)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	if noop {
		return authfile.CommitNoop, nil
	}
	if len(out) > maxConfigBytes {
		return authfile.CommitNotApplied, errors.New("settings default update: output exceeds size limit")
	}
	tempLeaf, tempFD, err := createSettingsTemp(parentFD)
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: create temporary file")
	}
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = unix.Close(tempFD)
		}
		_ = unix.Unlinkat(parentFD, tempLeaf, 0)
	}()
	if err := writeSettingsAll(tempFD, out); err != nil || unix.Fsync(tempFD) != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: write temporary file")
	}
	if defaultUpdateTestHook != nil {
		if err := defaultUpdateTestHook("after-temp-sync"); err != nil {
			return authfile.CommitNotApplied, errors.New("settings default update: prepare replacement")
		}
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	if err := unix.Close(tempFD); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: close temporary file")
	}
	tempOpen = false
	if defaultUpdateTestHook != nil {
		if err := defaultUpdateTestHook("before-compare"); err != nil {
			return authfile.CommitNotApplied, errors.New("settings default update: prepare comparison")
		}
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	current, err := readSettingsTarget(parentFD, leaf)
	if err != nil || !sameSettingsTarget(before, current) {
		return authfile.CommitNotApplied, errors.New("Configuration changed while this command was running; no changes were made. Review the file and retry.")
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings default update: %w", err)
	}
	if err := unix.Renameat(parentFD, tempLeaf, parentFD, leaf); err != nil {
		return authfile.CommitNotApplied, errors.New("settings default update: replace target")
	}
	if defaultUpdateTestHook != nil {
		if err := defaultUpdateTestHook("after-rename"); err != nil {
			return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: sync parent after replacement")
		}
	}
	if err := unix.Fsync(parentFD); err != nil {
		return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings default update: sync parent after replacement")
	}
	return authfile.CommitDurable, nil
}

type settingsTarget struct {
	data       []byte
	dev, inode uint64
	exists     bool
}

func canonicalSettingsParent(path string) (string, string, error) {
	if path == "" {
		return "", "", errors.New("path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", errors.New("path is unavailable")
	}
	parentPath := filepath.Dir(abs)
	parent, err := filepath.EvalSymlinks(parentPath)
	conventional := filepath.Join(xdgconfig.UserConfigDir(xdgconfig.OSEnv), UserSettingsRelPath)
	if errors.Is(err, os.ErrNotExist) && filepath.Clean(abs) == filepath.Clean(conventional) {
		base, baseErr := filepath.EvalSymlinks(filepath.Dir(parentPath))
		if baseErr != nil {
			return "", "", errors.New("conventional config directory is unavailable")
		}
		baseFD, openErr := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return "", "", errors.New("open conventional config directory")
		}
		created := false
		if mkdirErr := unix.Mkdirat(baseFD, filepath.Base(parentPath), 0o700); mkdirErr == nil {
			created = true
		} else if !errors.Is(mkdirErr, unix.EEXIST) {
			_ = unix.Close(baseFD)
			return "", "", errors.New("create conventional settings directory")
		}
		if created {
			if defaultUpdateTestHook != nil {
				if hookErr := defaultUpdateTestHook("before-created-parent-sync"); hookErr != nil {
					_ = unix.Close(baseFD)
					return "", "", errors.New("sync conventional config directory")
				}
			}
			if syncErr := unix.Fsync(baseFD); syncErr != nil {
				_ = unix.Close(baseFD)
				return "", "", errors.New("sync conventional config directory")
			}
			if defaultUpdateTestHook != nil {
				if hookErr := defaultUpdateTestHook("before-created-parent-close"); hookErr != nil {
					_ = unix.Close(baseFD)
					return "", "", errors.New("close conventional config directory")
				}
			}
		}
		if closeErr := unix.Close(baseFD); closeErr != nil {
			return "", "", errors.New("close conventional config directory")
		}
		parent = filepath.Join(base, filepath.Base(parentPath))
		err = nil
	}
	if err != nil {
		return "", "", errors.New("parent must already exist")
	}
	return filepath.Clean(parent), filepath.Base(abs), nil
}

func readSettingsTarget(parentFD int, leaf string) (settingsTarget, error) {
	fd, err := openSettingsLeaf(parentFD, leaf, false, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return settingsTarget{}, nil
	}
	if err != nil {
		return settingsTarget{}, errors.New("unsafe target file")
	}
	file := os.NewFile(uintptr(fd), leaf)
	if file == nil {
		_ = unix.Close(fd)
		return settingsTarget{}, errors.New("read target file")
	}
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return settingsTarget{}, errors.New("inspect target file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return settingsTarget{}, errors.New("target exceeds size limit or cannot be read")
	}
	return settingsTarget{data: data, dev: uint64(stat.Dev), inode: uint64(stat.Ino), exists: true}, nil
}

func openSettingsLeaf(parentFD int, leaf string, create bool, flags int) (int, error) {
	fd, err := unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) && create {
		fd, err = unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		}
	}
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !settingsPrivateFile(&stat) {
		_ = unix.Close(fd)
		return -1, errors.New("unsafe ownership, mode, or type")
	}
	return fd, nil
}

func settingsPrivateDir(stat *unix.Stat_t) bool {
	if stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		return false
	}
	euid := os.Geteuid()
	return euid >= 0 && stat.Uid == uint32(euid) // #nosec G115 -- nonnegative is checked before conversion.
}

func settingsPrivateFile(stat *unix.Stat_t) bool {
	if stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 {
		return false
	}
	euid := os.Geteuid()
	return euid >= 0 && stat.Uid == uint32(euid) // #nosec G115 -- nonnegative is checked before conversion.
}

func sameSettingsTarget(a, b settingsTarget) bool {
	return a.exists == b.exists && a.dev == b.dev && a.inode == b.inode && bytes.Equal(a.data, b.data)
}

func createSettingsTemp(parentFD int) (string, int, error) {
	for range 10 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, err
		}
		name := ".settings-default-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return name, fd, err
	}
	return "", -1, errors.New("temporary name collision")
}

func writeSettingsAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
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
		entry := defaultStaticEntry("models:\n  default_provider: " + defaultQuote(update.Provider) + "\n  default: " + defaultQuote(update.Model) + "\n")
		doc.Mapping().Values = append(doc.Mapping().Values, entry)
		out := []byte(doc.String())
		if err := ValidateYAML(out); err != nil {
			return nil, false, errors.New("updated settings document is invalid")
		}
		return out, false, nil
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
	replacement := defaultNestedEntry(key, value)
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

func defaultNestedEntry(key, value string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte("models:\n  " + key + ": " + defaultQuote(value) + "\n"))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static nested settings YAML")
	}
	models, ok := doc.Mapping().Values[0].Value.(*ast.MappingNode)
	if !ok || len(models.Values) != 1 {
		panic("invalid static nested settings mapping")
	}
	return models.Values[0]
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
