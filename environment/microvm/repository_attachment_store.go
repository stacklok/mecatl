package microvm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

const repositoryAttachmentVersion = 2

type repositoryAttachmentRepository struct {
	Owner              string `json:"owner"`
	RepositoryKey      string `json:"repository_key"`
	GitCommonDirectory string `json:"git_common_directory"`
	Generation         uint32 `json:"generation"`
}

type repositoryAttachmentDocument struct {
	Version          int                            `json:"version"`
	Binding          control.Binding                `json:"binding"`
	Repository       repositoryAttachmentRepository `json:"repository"`
	WorktreePath     string                         `json:"worktree_path"`
	SourceRoot       string                         `json:"source_root"`
	MetadataPath     string                         `json:"metadata_path"`
	Branch           string                         `json:"branch"`
	Deleted          bool                           `json:"deleted,omitempty"`
	WorktreeRetained bool                           `json:"worktree_retained,omitempty"`
}

func (m *RepositoryAttachmentManager) loadRecords() error { //nolint:gocyclo // fixed hierarchy traversal keeps confinement explicit
	if m == nil || m.logical == nil || m.logical.registry == nil {
		return ErrEnvironmentUnavailable
	}
	root := m.logical.registry.stateRoot
	owners, err := privateAttachmentDirectories(filepath.Join(root, "owners"), "owner")
	if err != nil {
		return err
	}
	for _, owner := range owners {
		repositories, err := privateAttachmentDirectories(filepath.Join(root, "owners", owner, "repositories"), "repository")
		if err != nil {
			return err
		}
		for _, repository := range repositories {
			repositoryRoot := filepath.Join(root, "owners", owner, "repositories", repository)
			logicalRoot := filepath.Join(repositoryRoot, "logical")
			logicalIDs, err := privateAttachmentDirectories(logicalRoot, "logical")
			if err != nil {
				return err
			}
			for _, logicalID := range logicalIDs {
				legacy := filepath.Join(logicalRoot, logicalID, "attachment.json")
				if _, err := os.Lstat(legacy); err == nil {
					return errors.New("unsupported repository attachment metadata in guest-exported logical namespace")
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			attachmentsRoot := filepath.Join(repositoryRoot, "attachments")
			entries, err := os.ReadDir(attachmentsRoot)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name() == ".staging" {
					info, infoErr := entry.Info()
					if infoErr != nil {
						return infoErr
					}
					uid := os.Getuid()
					stat, ok := info.Sys().(*syscall.Stat_t)
					if uid < 0 || !ok || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || uint64(stat.Uid) != uint64(uid) {
						return errors.New("repository attachment staging namespace is unsafe")
					}
					continue
				}
				if entry.IsDir() {
					return errors.New("unsupported repository attachment directory layout; data was preserved")
				}
				name := entry.Name()
				if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(name, ".json") {
					return errors.New("repository attachment namespace contains an invalid committed entry")
				}
				logicalID := strings.TrimSuffix(name, ".json")
				decoded, decodeErr := hex.DecodeString(logicalID)
				if decodeErr != nil || len(decoded) != 16 {
					return errors.New("repository attachment namespace contains an invalid committed record name")
				}
				filename := filepath.Join(attachmentsRoot, name)
				record, err := readRepositoryAttachment(m.logical.registry, filename)
				if err != nil {
					return err
				}
				if m.records[record.binding.Ref] != nil {
					return errors.New("repository attachment inventory contains duplicate ref")
				}
				m.records[record.binding.Ref] = record
			}
		}
	}
	return nil
}

func privateAttachmentDirectories(root, namespace string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validOpaquePathComponent(entry.Name()) || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("repository attachment %s namespace contains an invalid entry", namespace)
		}
		result = append(result, entry.Name())
	}
	return result, nil
}

func readRepositoryAttachment(registry *RepositoryVMRegistry, filename string) (*repositoryAttachmentRecord, error) { //nolint:gocyclo // fail-closed metadata validation is clearest in one path
	stateRoot := registry.stateRoot
	relative, err := filepath.Rel(stateRoot, filename)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 6 || parts[0] != "owners" || parts[2] != "repositories" || parts[4] != "attachments" ||
		!validOpaquePathComponent(parts[1]) || !validOpaquePathComponent(parts[3]) || !strings.HasSuffix(parts[5], ".json") {
		return nil, errors.New("repository attachment metadata is outside the confined host-only namespace")
	}
	logicalName := strings.TrimSuffix(parts[5], ".json")
	logicalID, err := hex.DecodeString(logicalName)
	if err != nil || len(logicalID) != 16 {
		return nil, errors.New("repository attachment metadata has an invalid logical identity")
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename)
	defer func() { _ = file.Close() }()
	if err := validatePrivateAttachmentFile(file); err != nil {
		return nil, err
	}
	var document repositoryAttachmentDocument
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode repository attachment metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("repository attachment metadata has trailing content")
	}
	if document.Version != repositoryAttachmentVersion {
		return nil, fmt.Errorf("unsupported repository attachment metadata version %d", document.Version)
	}
	binding := document.Binding
	environmentID, generation, err := parseEnvironmentRef(EnvironmentRef{Kind: Kind, ID: binding.Ref})
	repositoryRoot := filepath.Dir(filepath.Dir(filename))
	logicalRoot := filepath.Join(repositoryRoot, "logical", logicalName)
	identity, identityErr := newValidatedRepositoryIdentity(document.Repository.Owner, document.Repository.GitCommonDirectory, stateRoot)
	if err != nil || identityErr != nil || binding.AssignedRoot != "" || binding.EnvironmentID != environmentID || generation != binding.Generation ||
		binding.Owner != document.Repository.Owner || binding.Generation != document.Repository.Generation ||
		environmentID != "logical-"+logicalName || identity.value.StateDirectory != repositoryRoot || identity.value.Key != document.Repository.RepositoryKey ||
		document.WorktreePath != filepath.Join(logicalRoot, "worktree") || document.MetadataPath != filepath.Join(logicalRoot, "metadata") ||
		document.SourceRoot != document.Repository.GitCommonDirectory || !filepath.IsAbs(document.SourceRoot) || document.Branch != "mecatl/"+logicalName ||
		document.Deleted && !document.WorktreeRetained {
		return nil, errors.New("repository attachment metadata is inconsistent")
	}
	repositoryDirectory, openErr := registry.openIdentityDirectory(identity, false)
	if openErr != nil {
		return nil, errors.New("repository attachment metadata names an unavailable repository")
	}
	current, readErr := readRepositoryRecord(repositoryDirectory)
	_ = repositoryDirectory.Close()
	if readErr != nil || current.Generation != document.Repository.Generation || registry.validateRepositoryDurableRecord(identity.value, current) != nil {
		return nil, errors.New("repository attachment metadata names an invalid repository generation")
	}
	if err := claimValidate(binding); err != nil {
		return nil, control.ErrBindingMismatch
	}
	return &repositoryAttachmentRecord{
		binding: binding, repository: current, worktreePath: document.WorktreePath,
		sourceRoot: document.SourceRoot, metadataPath: document.MetadataPath, branch: document.Branch,
		deleted: document.Deleted, worktreeRetained: document.WorktreeRetained,
	}, nil
}

func validatePrivateAttachmentDirectory(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("repository attachment directory is not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	uid := os.Getuid()
	if !ok || uid < 0 || uint64(stat.Uid) != uint64(uid) {
		return errors.New("repository attachment directory has the wrong owner")
	}
	return nil
}

func validatePrivateAttachmentFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("repository attachment metadata is not a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	uid := os.Getuid()
	if !ok || uid < 0 || uint64(stat.Uid) != uint64(uid) {
		return errors.New("repository attachment metadata has the wrong owner")
	}
	return nil
}

func persistRepositoryAttachment(record *repositoryAttachmentRecord) error {
	if record == nil {
		return errors.New("repository attachment record is nil")
	}
	document := repositoryAttachmentDocument{
		Version: repositoryAttachmentVersion, Binding: record.binding,
		Repository:   repositoryAttachmentRepository{Owner: record.repository.Owner, RepositoryKey: record.repository.RepositoryKey, GitCommonDirectory: record.repository.GitCommonDirectory, Generation: record.repository.Generation},
		WorktreePath: record.worktreePath, SourceRoot: record.sourceRoot, MetadataPath: record.metadataPath, Branch: record.branch,
		Deleted: record.deleted, WorktreeRetained: record.worktreeRetained,
	}
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	directory, logicalID, err := attachmentStoreLocation(record, true)
	if err != nil {
		return err
	}
	return writeAttachmentFile(directory, logicalID+".json", append(data, '\n'))
}

func attachmentStoreLocation(record *repositoryAttachmentRecord, create bool) (string, string, error) {
	logicalRoot := filepath.Dir(record.worktreePath)
	logicalID := filepath.Base(logicalRoot)
	decoded, err := hex.DecodeString(logicalID)
	if err != nil || len(decoded) != 16 || record.metadataPath != filepath.Join(logicalRoot, "metadata") {
		return "", "", errors.New("repository attachment paths are inconsistent")
	}
	repositoryRoot := filepath.Dir(filepath.Dir(logicalRoot))
	repositoryFD, err := unix.Open(repositoryRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = unix.Close(repositoryFD) }()
	if create {
		if err := unix.Mkdirat(repositoryFD, "attachments", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return "", "", err
		}
	} else {
		var stat unix.Stat_t
		if err := unix.Fstatat(repositoryFD, "attachments", &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			return filepath.Join(repositoryRoot, "attachments"), logicalID, nil
		} else if err != nil {
			return "", "", err
		}
	}
	attachmentsFD, err := openatOpaque(repositoryFD, "attachments", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", err
	}
	attachments := os.NewFile(uintptr(attachmentsFD), filepath.Join(repositoryRoot, "attachments"))
	defer func() { _ = attachments.Close() }()
	if err := validatePrivateAttachmentDirectory(attachments); err != nil {
		return "", "", err
	}
	return attachments.Name(), logicalID, nil
}

func writeAttachmentFile(directory, target string, data []byte) error { //nolint:gocyclo // linear no-follow publication transaction is intentionally explicit.
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(directoryFD), directory)
	defer func() { _ = dir.Close() }()
	if err := unix.Mkdirat(directoryFD, ".staging", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	stagingFD, err := openatOpaque(directoryFD, ".staging", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	staging := os.NewFile(uintptr(stagingFD), filepath.Join(directory, ".staging"))
	defer func() { _ = staging.Close() }()
	if err := validatePrivateAttachmentDirectory(staging); err != nil {
		return err
	}
	var name string
	var temporary *os.File
	for attempt := 0; attempt < 100; attempt++ {
		var value [8]byte
		if _, err := rand.Read(value[:]); err != nil {
			return err
		}
		name = "attachment-" + hex.EncodeToString(value[:])
		fd, openErr := openatOpaque(stagingFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if openErr == nil {
			temporary = os.NewFile(uintptr(fd), filepath.Join(staging.Name(), name))
			break
		}
		if !errors.Is(openErr, unix.EEXIST) {
			return openErr
		}
	}
	if temporary == nil {
		return errors.New("allocate repository attachment metadata temporary file")
	}
	defer func() { _ = unix.Unlinkat(stagingFD, name, 0) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := staging.Sync(); err != nil {
		return err
	}
	existing, err := openatExistingFile(directoryFD, target, unix.O_RDONLY)
	if err == nil {
		if err := validatePrivateAttachmentFile(existing); err != nil {
			_ = existing.Close()
			return err
		}
		if err := existing.Close(); err != nil {
			return err
		}
		if err := unix.Renameat(stagingFD, name, directoryFD, target); err != nil {
			return err
		}
	} else if errors.Is(err, unix.ENOENT) {
		if err := renameatNoReplace(stagingFD, name, directoryFD, target); err != nil {
			return err
		}
	} else {
		return err
	}
	if err := staging.Sync(); err != nil {
		return err
	}
	return dir.Sync()
}

func removeRepositoryAttachmentRecord(record *repositoryAttachmentRecord) error {
	if record == nil {
		return nil
	}
	directory, logicalID, err := attachmentStoreLocation(record, false)
	if err != nil {
		return err
	}
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(directoryFD), directory)
	defer func() { _ = dir.Close() }()
	target := logicalID + ".json"
	existing, err := openatExistingFile(directoryFD, target, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validatePrivateAttachmentFile(existing); err != nil {
		_ = existing.Close()
		return err
	}
	if err := existing.Close(); err != nil {
		return err
	}
	if err := unix.Unlinkat(directoryFD, target, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return dir.Sync()
}
