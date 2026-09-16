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

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

const repositoryAttachmentVersion = 1

type repositoryAttachmentDocument struct {
	Version          int                `json:"version"`
	Binding          control.Binding    `json:"binding"`
	Repository       RepositoryVMRecord `json:"repository"`
	WorktreePath     string             `json:"worktree_path"`
	SourceRoot       string             `json:"source_root"`
	MetadataPath     string             `json:"metadata_path"`
	Branch           string             `json:"branch"`
	Deleted          bool               `json:"deleted,omitempty"`
	WorktreeRetained bool               `json:"worktree_retained,omitempty"`
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
			logicalRoot := filepath.Join(root, "owners", owner, "repositories", repository, "logical")
			logicalIDs, err := privateAttachmentDirectories(logicalRoot, "logical")
			if err != nil {
				return err
			}
			for _, logicalID := range logicalIDs {
				filename := filepath.Join(logicalRoot, logicalID, "attachment.json")
				if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return err
				}
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
	if len(parts) != 7 || parts[0] != "owners" || parts[2] != "repositories" || parts[4] != "logical" || parts[6] != "attachment.json" ||
		!validOpaquePathComponent(parts[1]) || !validOpaquePathComponent(parts[3]) {
		return nil, errors.New("repository attachment metadata is outside the confined logical namespace")
	}
	logicalID, err := hex.DecodeString(parts[5])
	if err != nil || len(logicalID) != 16 {
		return nil, errors.New("repository attachment metadata has an invalid logical identity")
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("repository attachment metadata is not a private regular file")
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
	logicalRoot := filepath.Dir(filename)
	repositoryRoot := filepath.Dir(filepath.Dir(logicalRoot))
	identity, identityErr := newValidatedRepositoryIdentity(document.Repository.Owner, document.Repository.GitCommonDirectory, stateRoot)
	if err != nil || identityErr != nil || binding.AssignedRoot != "" || binding.EnvironmentID != environmentID || generation != binding.Generation ||
		binding.Owner != document.Repository.Owner || binding.Generation != document.Repository.Generation ||
		environmentID != "logical-"+parts[5] || identity.value.StateDirectory != repositoryRoot || identity.value.Key != document.Repository.RepositoryKey ||
		document.WorktreePath != filepath.Join(logicalRoot, "worktree") || document.MetadataPath != filepath.Join(logicalRoot, "metadata") ||
		document.SourceRoot != document.Repository.GitCommonDirectory || !filepath.IsAbs(document.SourceRoot) || document.Branch != "mecatl/"+parts[5] ||
		document.Deleted && !document.WorktreeRetained {
		return nil, errors.New("repository attachment metadata is inconsistent")
	}
	if err := registry.validateRepositoryRecord(identity.value, document.Repository); err != nil {
		return nil, errors.New("repository attachment metadata names an invalid repository generation")
	}
	if err := claimValidate(binding); err != nil {
		return nil, control.ErrBindingMismatch
	}
	return &repositoryAttachmentRecord{
		binding: binding, repository: document.Repository, worktreePath: document.WorktreePath,
		sourceRoot: document.SourceRoot, metadataPath: document.MetadataPath, branch: document.Branch,
		deleted: document.Deleted, worktreeRetained: document.WorktreeRetained,
	}, nil
}

func persistRepositoryAttachment(record *repositoryAttachmentRecord) error {
	if record == nil {
		return errors.New("repository attachment record is nil")
	}
	document := repositoryAttachmentDocument{
		Version: repositoryAttachmentVersion, Binding: record.binding, Repository: record.repository,
		WorktreePath: record.worktreePath, SourceRoot: record.sourceRoot, MetadataPath: record.metadataPath, Branch: record.branch,
		Deleted: record.deleted, WorktreeRetained: record.worktreeRetained,
	}
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	return writeAttachmentFile(filepath.Dir(record.worktreePath), append(data, '\n'))
}

func writeAttachmentFile(directory string, data []byte) error {
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(directoryFD), directory)
	defer func() { _ = dir.Close() }()
	var name string
	var temporary *os.File
	for attempt := 0; attempt < 100; attempt++ {
		var value [8]byte
		if _, err := rand.Read(value[:]); err != nil {
			return err
		}
		name = ".attachment-" + hex.EncodeToString(value[:])
		fd, openErr := openatOpaque(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if openErr == nil {
			temporary = os.NewFile(uintptr(fd), filepath.Join(directory, name))
			break
		}
		if !errors.Is(openErr, unix.EEXIST) {
			return openErr
		}
	}
	if temporary == nil {
		return errors.New("allocate repository attachment metadata temporary file")
	}
	defer func() { _ = unix.Unlinkat(directoryFD, name, 0) }()
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
	if err := unix.Renameat(directoryFD, name, directoryFD, "attachment.json"); err != nil {
		return err
	}
	return dir.Sync()
}

func removeRepositoryAttachmentRecord(record *repositoryAttachmentRecord) error {
	if record == nil {
		return nil
	}
	directory := filepath.Dir(record.worktreePath)
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(directoryFD), directory)
	defer func() { _ = dir.Close() }()
	if err := unix.Unlinkat(directoryFD, "attachment.json", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return dir.Sync()
}
