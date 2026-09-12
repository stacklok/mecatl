//go:build linux

package authfile

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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/providerid"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

const updateLockWait = 5 * time.Second

var updateTestHook func(string) error

func updateSupported() bool { return true }

func updateAPIKey(ctx context.Context, path string, update APIKeyUpdate) (CommitState, error) {
	operation := "set"
	if update.APIKey == nil {
		operation = "remove"
	}
	if !providerid.Valid(update.Provider) {
		return CommitNotApplied, errors.New("auth update for provider: invalid provider id")
	}
	switch update.Provider {
	case "mock", "openai-codex", "toolhive":
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: provider does not use API keys", operation, update.Provider)
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: %w", operation, update.Provider, err)
	}
	if update.APIKey != nil && !utf8.ValidString(*update.APIKey) {
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: API key is not valid UTF-8", operation, update.Provider)
	}
	parent, leaf, err := canonicalUpdateParent(path)
	if err != nil {
		return CommitNotApplied, fmt.Errorf("auth %s for provider %s: %w", operation, update.Provider, err)
	}
	state, err := updateInParent(ctx, parent, leaf, update)
	if err != nil {
		return state, fmt.Errorf("auth %s for provider %s: %w", operation, update.Provider, err)
	}
	return state, nil
}

func canonicalUpdateParent(path string) (string, string, error) {
	if path == "" {
		return "", "", errors.New("target path is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", errors.New("target path is unavailable")
	}
	parent := filepath.Dir(abs)
	physical, err := filepath.EvalSymlinks(parent)
	if errors.Is(err, os.ErrNotExist) && filepath.Clean(abs) == filepath.Clean(DefaultPath(xdgconfig.OSEnv)) {
		base := filepath.Dir(parent)
		physicalBase, baseErr := filepath.EvalSymlinks(base)
		if baseErr != nil {
			return "", "", errors.New("conventional config directory is unavailable")
		}
		physical = filepath.Join(physicalBase, filepath.Base(parent))
		if mkdirErr := os.Mkdir(physical, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return "", "", errors.New("create conventional credential directory")
		}
		err = nil
	}
	if err != nil {
		return "", "", errors.New("credential parent must already exist")
	}
	info, err := os.Lstat(physical)
	if err != nil || !privateDir(info) {
		return "", "", errors.New("credential parent must be an owner-only non-link directory")
	}
	return filepath.Clean(physical), filepath.Base(abs), nil
}

func privateDir(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && currentUID(stat.Uid)
}

func currentUID(uid uint32) bool {
	euid := os.Geteuid()
	return euid >= 0 && uid == uint32(euid) // #nosec G115 -- nonnegative is checked before conversion.
}

func privateRegular(stat *unix.Stat_t) bool {
	return stat != nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o777 == 0o600 && currentUID(stat.Uid)
}

type targetSnapshot struct {
	data       []byte
	dev, inode uint64
	exists     bool
}

//nolint:gocyclo // The linear commit protocol keeps every pre/post-rename state transition explicit.
func updateInParent(ctx context.Context, parent, leaf string, update APIKeyUpdate) (state CommitState, result error) {
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return CommitNotApplied, errors.New("open credential parent")
	}
	defer func() {
		if closeErr := unix.Close(parentFD); closeErr != nil && result == nil {
			if state == CommitDurable {
				state, result = CommitReplacementAppliedDurabilityUnknown, errors.New("close credential parent after replacement")
			} else {
				state, result = CommitNotApplied, errors.New("close credential parent")
			}
		}
	}()

	lockLeaf := leaf + ".lock"
	lockFD, err := openPrivateLeaf(parentFD, lockLeaf, true, unix.O_RDWR)
	if err != nil {
		return CommitNotApplied, errors.New("open credential lock")
	}
	defer func() { _ = unix.Close(lockFD) }()
	lockCtx, cancel := context.WithTimeout(ctx, updateLockWait)
	defer cancel()
	if err := lockCtx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("acquire credential lock: %w", err)
	}
	if err := lockFile(lockCtx, lockFD); err != nil {
		return CommitNotApplied, fmt.Errorf("acquire credential lock: %w", err)
	}
	defer func() { _ = unix.Flock(lockFD, unix.LOCK_UN) }()
	if updateTestHook != nil {
		if err := updateTestHook("after-lock"); err != nil {
			return CommitNotApplied, errors.New("prepare credential update")
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("credential update cancelled: %w", err)
	}

	before, err := readTarget(parentFD, leaf)
	if err != nil {
		return CommitNotApplied, err
	}
	out, noop, err := mutateAuth(before.data, update)
	if err != nil {
		return CommitNotApplied, err
	}
	if noop {
		return CommitNoop, nil
	}
	if len(out) > maxFileBytes {
		return CommitNotApplied, errors.New("updated auth document exceeds 16 KiB")
	}
	tempLeaf, tempFD, err := createTemp(parentFD, leaf)
	if err != nil {
		return CommitNotApplied, errors.New("create credential temporary file")
	}
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = unix.Close(tempFD)
		}
		_ = unix.Unlinkat(parentFD, tempLeaf, 0)
	}()
	if err := writeAll(tempFD, out); err != nil || unix.Fsync(tempFD) != nil {
		return CommitNotApplied, errors.New("write credential temporary file")
	}
	if updateTestHook != nil {
		if err := updateTestHook("after-temp-sync"); err != nil {
			return CommitNotApplied, errors.New("prepare credential replacement")
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("credential update cancelled: %w", err)
	}
	if err := unix.Close(tempFD); err != nil {
		return CommitNotApplied, errors.New("close credential temporary file")
	}
	tempOpen = false
	if updateTestHook != nil {
		if err := updateTestHook("before-compare"); err != nil {
			return CommitNotApplied, errors.New("prepare credential comparison")
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("credential update cancelled: %w", err)
	}
	current, err := readTarget(parentFD, leaf)
	if err != nil || !sameSnapshot(before, current) {
		return CommitNotApplied, errors.New("credential target changed before replacement")
	}
	if err := ctx.Err(); err != nil {
		return CommitNotApplied, fmt.Errorf("credential update cancelled: %w", err)
	}
	if err := unix.Renameat(parentFD, tempLeaf, parentFD, leaf); err != nil {
		return CommitNotApplied, errors.New("replace credential target")
	}
	if updateTestHook != nil {
		if err := updateTestHook("after-rename"); err != nil {
			return CommitReplacementAppliedDurabilityUnknown, errors.New("sync credential directory after replacement")
		}
	}
	if err := unix.Fsync(parentFD); err != nil {
		return CommitReplacementAppliedDurabilityUnknown, errors.New("sync credential directory after replacement")
	}
	return CommitDurable, nil
}

func lockFile(ctx context.Context, fd int) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func openPrivateLeaf(parentFD int, leaf string, create bool, flags int) (int, error) {
	fd, err := unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) && create {
		fd, err = unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(parentFD, leaf, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
	}
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !privateRegular(&stat) {
		_ = unix.Close(fd)
		return -1, errors.New("unsafe leaf ownership, mode, or type")
	}
	return fd, nil
}

func readTarget(parentFD int, leaf string) (targetSnapshot, error) {
	fd, err := openPrivateLeaf(parentFD, leaf, false, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return targetSnapshot{}, nil
	}
	if err != nil {
		return targetSnapshot{}, errors.New("read credential target")
	}
	file := os.NewFile(uintptr(fd), leaf)
	if file == nil {
		_ = unix.Close(fd)
		return targetSnapshot{}, errors.New("read credential target")
	}
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return targetSnapshot{}, errors.New("inspect credential target")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return targetSnapshot{}, errors.New("credential target exceeds 16 KiB or cannot be read")
	}
	return targetSnapshot{data: data, dev: stat.Dev, inode: stat.Ino, exists: true}, nil
}

func sameSnapshot(a, b targetSnapshot) bool {
	return a.exists == b.exists && a.dev == b.dev && a.inode == b.inode && bytes.Equal(a.data, b.data)
}

func createTemp(parentFD int, leaf string) (string, int, error) {
	for range 10 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, err
		}
		name := "." + leaf + "-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return name, fd, err
	}
	return "", -1, errors.New("temporary name collision")
}

func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

//nolint:gocyclo // Schema validation and the one targeted mutation stay together to preserve the AST safely.
func mutateAuth(data []byte, update APIKeyUpdate) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("providers: {}\n")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, false, errors.New("auth document is invalid or ambiguous")
	}
	root := doc.Mapping()
	providersNode, err := uniqueAuthValue(root, "providers", true)
	if err != nil {
		return nil, false, err
	}
	if providersNode == nil {
		return nil, false, errors.New("auth document is missing providers mapping")
	}
	providers, ok := providersNode.(*ast.MappingNode)
	if !ok {
		return nil, false, errors.New("auth providers must be a mapping")
	}
	var target *ast.MappingValueNode
	seen := map[string]bool{}
	for _, entry := range providers.Values {
		name, ok := authString(entry.Key)
		if !ok || !providerid.Valid(name) || seen[name] {
			return nil, false, errors.New("auth providers contain an invalid or duplicate id")
		}
		seen[name] = true
		mapping, ok := entry.Value.(*ast.MappingNode)
		if !ok {
			return nil, false, errors.New("auth provider entry must be a mapping")
		}
		if err := validateProviderMapping(mapping); err != nil {
			return nil, false, err
		}
		if name == update.Provider {
			target = entry
		}
	}
	if target == nil {
		if update.APIKey == nil {
			return data, true, nil
		}
		target = staticAuthEntry(update.Provider + ": {}\n")
		providers.Values = append(providers.Values, target)
	}
	mapping := target.Value.(*ast.MappingNode)
	for _, entry := range mapping.Values {
		key, _ := authString(entry.Key)
		if key == "oauth" {
			return nil, false, errors.New("target provider uses OAuth and cannot be mutated as an API-key record")
		}
	}
	var keyEntry *ast.MappingValueNode
	for _, entry := range mapping.Values {
		key, _ := authString(entry.Key)
		if key == "api_key" {
			keyEntry = entry
		}
	}
	if update.APIKey == nil {
		if keyEntry == nil {
			return data, true, nil
		}
		for i, entry := range mapping.Values {
			if entry == keyEntry {
				mapping.Values = append(mapping.Values[:i], mapping.Values[i+1:]...)
				break
			}
		}
	} else {
		if keyEntry != nil {
			current, ok := authString(keyEntry.Value)
			if !ok {
				return nil, false, errors.New("auth api_key must be a string")
			}
			if current == *update.APIKey {
				return data, true, nil
			}
			replacement := staticAuthEntry("api_key: " + quoteYAML(*update.APIKey) + "\n")
			if err := keyEntry.Replace(replacement.Value); err != nil {
				return nil, false, errors.New("replace auth api_key")
			}
		} else {
			mapping.Values = append(mapping.Values, staticAuthEntry("api_key: "+quoteYAML(*update.APIKey)+"\n"))
		}
	}
	out := []byte(doc.String())
	if err := validateUpdatedAuth(out); err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func validateUpdatedAuth(data []byte) error {
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil || doc.Mapping() == nil {
		return errors.New("updated auth document is invalid")
	}
	return nil
}

func validateProviderMapping(mapping *ast.MappingNode) error {
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := authString(entry.Key)
		if !ok || seen[key] || (key != "api_key" && key != "oauth") {
			return errors.New("auth provider entry has duplicate or unknown schema")
		}
		seen[key] = true
		if key == "api_key" {
			if _, ok := authString(entry.Value); !ok {
				return errors.New("auth api_key must be a string")
			}
			continue
		}
		oauth, ok := entry.Value.(*ast.MappingNode)
		if !ok {
			return errors.New("auth oauth must be a mapping")
		}
		oauthSeen := map[string]bool{}
		for _, field := range oauth.Values {
			name, ok := authString(field.Key)
			if !ok || oauthSeen[name] || (name != "access_token" && name != "account_id" && name != "expires_at") {
				return errors.New("auth oauth has duplicate or unknown schema")
			}
			oauthSeen[name] = true
			if _, ok := authString(field.Value); !ok {
				return errors.New("auth oauth values must be strings")
			}
		}
	}
	return nil
}

func uniqueAuthValue(mapping *ast.MappingNode, wanted string, rejectUnknown bool) (ast.Node, error) {
	var found ast.Node
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := authString(entry.Key)
		if !ok || seen[key] || (rejectUnknown && key != wanted) {
			return nil, errors.New("auth root has duplicate or unknown schema")
		}
		seen[key] = true
		if key == wanted {
			found = entry.Value
		}
	}
	return found, nil
}

func authString(node ast.Node) (string, bool) {
	if tagged, ok := node.(*ast.TagNode); ok && tagged.Start != nil && tagged.Start.Value == "!!str" {
		if parserToken := tagged.Value.GetToken(); parserToken != nil {
			return parserToken.Value, true
		}
	}
	value, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func staticAuthEntry(text string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(text))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static auth YAML")
	}
	return doc.Mapping().Values[0]
}

func quoteYAML(value string) string {
	var b bytes.Buffer
	b.WriteByte('\'')
	for _, r := range value {
		if r == '\'' {
			b.WriteString("''")
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
