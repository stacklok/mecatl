package microvm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
)

const (
	repositoryRegistryVersion = 1
	repositoryHealthTimeout   = 10 * time.Second

	repositoryRootFSStaging   = "staging"
	repositoryRootFSPublished = "published"
)

var (
	// ErrRepositoryVMUnknown means no generation has been admitted for the repository key.
	ErrRepositoryVMUnknown = errors.New("microvm repository generation is unknown")
	// ErrRepositoryVMInconsistent means the durable singleton cannot be reattached exactly.
	ErrRepositoryVMInconsistent = errors.New("microvm repository generation is inconsistent")
	// ErrRepositoryLogicalRootUnavailable means the authenticated guest could not
	// attach the assigned repository worktree. It intentionally carries no backend detail.
	ErrRepositoryLogicalRootUnavailable = errors.New("microvm repository logical root is unavailable")
)

// RepositoryIdentity is the canonical owner/repository key and its opaque,
// owner-confined state location.
type RepositoryIdentity struct {
	Owner              string
	GitCommonDirectory string
	Key                string
	StateDirectory     string
}

// ResolveRepositoryIdentity canonicalizes a checkout through Git and derives an
// opaque state identity. Repository-controlled path components never enter the
// state-directory suffix.
func ResolveRepositoryIdentity(ctx context.Context, owner, checkout, stateRoot string) (RepositoryIdentity, error) {
	if owner == "" {
		return RepositoryIdentity{}, errors.New("microvm repository owner is required")
	}
	canonicalStateRoot, err := canonicalStateRoot(stateRoot)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	canonicalCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("canonicalize microvm checkout: %w", err)
	}
	canonicalCheckout, err = filepath.Abs(canonicalCheckout)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("make microvm checkout absolute: %w", err)
	}
	output, err := gitexec.Run(ctx, canonicalCheckout, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("resolve canonical Git common directory: %w", err)
	}
	common := strings.TrimSpace(string(output))
	if !filepath.IsAbs(common) {
		return RepositoryIdentity{}, errors.New("git common directory is not absolute")
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	common = filepath.Clean(common)
	info, err := os.Stat(common)
	if err != nil || !info.IsDir() {
		return RepositoryIdentity{}, fmt.Errorf("validate Git common directory: %w", errors.Join(err, errors.New("not a directory")))
	}

	validated, err := newValidatedRepositoryIdentity(owner, common, canonicalStateRoot)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	return validated.value, nil
}

type validatedRepositoryIdentity struct {
	value      RepositoryIdentity
	components [4]string
}

func newValidatedRepositoryIdentity(owner, gitCommonDirectory, stateRoot string) (validatedRepositoryIdentity, error) {
	ownerKey := framedDigest("mecatl.microvm.owner.v1", owner)
	repositoryKey := framedDigest("mecatl.microvm.repository.v1", owner, gitCommonDirectory)
	components := [4]string{"owners", ownerKey, "repositories", repositoryKey}
	for _, component := range components {
		if !validOpaquePathComponent(component) {
			return validatedRepositoryIdentity{}, errors.New("derived microvm repository identity has an invalid path component")
		}
	}
	stateDirectory := filepath.Join(stateRoot, components[0], components[1], components[2], components[3])
	if !repositoryPathWithin(stateRoot, stateDirectory) {
		return validatedRepositoryIdentity{}, errors.New("derived microvm repository state path is not confined")
	}
	return validatedRepositoryIdentity{
		value: RepositoryIdentity{
			Owner:              owner,
			GitCommonDirectory: gitCommonDirectory,
			Key:                repositoryKey,
			StateDirectory:     stateDirectory,
		},
		components: components,
	}, nil
}

func (r *RepositoryVMRegistry) validateIdentity(identity RepositoryIdentity) (validatedRepositoryIdentity, error) {
	validated, err := newValidatedRepositoryIdentity(identity.Owner, identity.GitCommonDirectory, r.stateRoot)
	if err != nil {
		return validatedRepositoryIdentity{}, err
	}
	if identity.Key != validated.value.Key || identity.StateDirectory != validated.value.StateDirectory {
		return validatedRepositoryIdentity{}, errors.New("microvm repository identity is not canonical")
	}
	return validated, nil
}

func validOpaquePathComponent(component string) bool {
	return component != "" && component != "." && component != ".." && !strings.ContainsRune(component, filepath.Separator)
}

func canonicalStateRoot(stateRoot string) (string, error) {
	if stateRoot == "" {
		return "", errors.New("microvm repository state root is required")
	}
	absolute, err := filepath.Abs(stateRoot)
	if err != nil {
		return "", fmt.Errorf("make microvm state root absolute: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", fmt.Errorf("create microvm state root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalize microvm state root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("microvm state root is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func framedDigest(domain string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func repositoryPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// RepositoryVMRuntime owns the repository generation's VM process. Start is
// called only after the provisioning record and private rootfs are durable.
type RepositoryVMRuntime interface {
	Start(context.Context, RepositoryVMRecord, VerifiedArtifacts, RepositoryBootAuthority) (RuntimeStatus, error)
	Health(context.Context, RepositoryVMRecord, RepositoryHealthChallenge) (RepositoryHealthResponse, error)
}

type repositoryRuntimeReconciler interface {
	Reconcile(context.Context, RepositoryVMRecord) error
}

// RepositoryArtifactIdentity is the durable immutable identity of one admitted
// launch artifact. Path points at the repository-private retained copy.
type RepositoryArtifactIdentity struct {
	Kind               ArtifactKind `json:"kind"`
	Digest             string       `json:"digest"`
	ManifestDigest     string       `json:"manifest_digest,omitempty"`
	DiscoveryReference string       `json:"discovery_reference,omitempty"`
	ResolutionEvidence string       `json:"resolution_evidence,omitempty"`
	Platform           string       `json:"platform,omitempty"`
	Path               string       `json:"path"`
}

// RepositoryArtifactSet is the complete durable admitted launch set.
type RepositoryArtifactSet struct {
	Runtime        RepositoryArtifactIdentity `json:"runtime"`
	Firmware       RepositoryArtifactIdentity `json:"firmware"`
	ExecutionImage RepositoryArtifactIdentity `json:"execution_image"`
	GuestAgent     RepositoryArtifactIdentity `json:"guest_agent"`
}

// ByKind returns the retained identity for kind, or its zero value.
func (s RepositoryArtifactSet) ByKind(kind ArtifactKind) RepositoryArtifactIdentity {
	switch kind {
	case ArtifactRuntime:
		return s.Runtime
	case ArtifactFirmware:
		return s.Firmware
	case ArtifactExecutionImage:
		return s.ExecutionImage
	case ArtifactGuestAgent:
		return s.GuestAgent
	default:
		return RepositoryArtifactIdentity{}
	}
}

func (s RepositoryArtifactSet) complete() bool {
	return s.Runtime.Kind == ArtifactRuntime && s.Firmware.Kind == ArtifactFirmware && s.ExecutionImage.Kind == ArtifactExecutionImage && s.GuestAgent.Kind == ArtifactGuestAgent
}

// RepositoryBootRecord is replaceable runtime state for one boot of a stable
// repository placement.
type RepositoryBootRecord struct {
	Generation      uint32 `json:"generation"`
	VMID            string `json:"vm_id"`
	Endpoint        string `json:"endpoint"`
	AuthorityDigest string `json:"authority_digest"`
	RunnerPID       int    `json:"runner_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
}

// RepositoryVMRecord is the durable singleton VM/rootfs identity for one key.
type RepositoryVMRecord struct {
	State                    EnvironmentState      `json:"state"`
	Owner                    string                `json:"owner"`
	RepositoryKey            string                `json:"repository_key"`
	GitCommonDirectory       string                `json:"git_common_directory"`
	Generation               uint32                `json:"generation"`
	RootFSPath               string                `json:"rootfs_path"`
	RootFSPhase              string                `json:"rootfs_phase"`
	RootFSStagingName        string                `json:"rootfs_staging_name,omitempty"`
	Artifacts                RepositoryArtifactSet `json:"artifacts"`
	PolicyRevision           string                `json:"policy_revision"`
	GuestEgressDigest        string                `json:"guest_egress_digest"`
	GuestAgentExecutableHash string                `json:"guest_agent_executable_hash"`
	Boot                     RepositoryBootRecord  `json:"boot"`

	// Boot mirrors retained for callers while this unmerged API transitions.
	VMID            string `json:"-"`
	Endpoint        string `json:"-"`
	AuthorityDigest string `json:"-"`
	RunnerPID       int    `json:"-"`
	ProcessIdentity string `json:"-"`
}

func (r *RepositoryVMRecord) syncBootFields() {
	r.VMID, r.Endpoint, r.AuthorityDigest = r.Boot.VMID, r.Boot.Endpoint, r.Boot.AuthorityDigest
	r.RunnerPID, r.ProcessIdentity = r.Boot.RunnerPID, r.Boot.ProcessIdentity
}

func (r RepositoryVMRecord) bootFieldsMatch() bool {
	return r.VMID == r.Boot.VMID && r.Endpoint == r.Boot.Endpoint && r.AuthorityDigest == r.Boot.AuthorityDigest &&
		r.RunnerPID == r.Boot.RunnerPID && r.ProcessIdentity == r.Boot.ProcessIdentity
}

func (r RepositoryVMRecord) bootGeneration() uint32 { return r.Boot.Generation }

type repositoryRegistryDocument struct {
	Version int                `json:"version"`
	Record  RepositoryVMRecord `json:"record"`
}

// RepositoryArtifactSnapshot returns a privately locked artifact view and its release.
// On error, the callback owns cleanup for anything it acquired and returns no cleanup
// responsibility to Ensure. On success, it returns a non-nil release function; Ensure
// invokes that function exactly once after every later success or failure, after rootfs
// materialization and runtime startup have finished consuming the snapshot.
type RepositoryArtifactSnapshot func(context.Context) (VerifiedArtifacts, func(), error)

// RepositoryVMRequest supplies first-use inputs. Artifacts is invoked only when
// no durable record exists; exact reattachment never validates or copies replacement bytes.
type RepositoryVMRequest struct {
	Owner     string
	Checkout  string
	Artifacts RepositoryArtifactSnapshot
}

// RepositoryVMResult reports the exact durable singleton selected by Ensure.
type RepositoryVMResult struct {
	Record     RepositoryVMRecord
	Reattached bool
}

// RepositoryVMRegistry owns durable singleton admission under one state root.
type RepositoryVMRegistry struct {
	stateRoot        string
	endpointRoot     string
	runtime          RepositoryVMRuntime
	artifactPolicy   string
	artifactRequests map[ArtifactKind]ArtifactRequest
	guestEgress      GuestEgressPolicy
	writeRecord      func(*repositoryDirectory, RepositoryVMRecord) error
}

// OpenRepositoryVMRegistry opens the repository lifecycle registry without
// admitting or reconciling any generation. endpointRoots optionally supplies a
// short owner-private runtime directory for Unix guest endpoints.
func OpenRepositoryVMRegistry(stateRoot string, runtime RepositoryVMRuntime, endpointRoots ...string) (*RepositoryVMRegistry, error) {
	if runtime == nil {
		return nil, errors.New("microvm repository runtime is required")
	}
	canonical, err := canonicalStateRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	endpointRoot := canonical
	if len(endpointRoots) > 1 {
		return nil, errors.New("microvm repository endpoint root is ambiguous")
	}
	if len(endpointRoots) == 1 {
		endpointRoot, err = canonicalStateRoot(endpointRoots[0])
		if err != nil {
			return nil, fmt.Errorf("open microvm repository endpoint root: %w", err)
		}
	}
	return &RepositoryVMRegistry{stateRoot: canonical, endpointRoot: endpointRoot, runtime: runtime, writeRecord: writeRepositoryRecord}, nil
}

// HasRecords reports whether any durable repository generation predates this
// registry instance. It reads only the fixed owner/repository hierarchy and
// fails closed on malformed entries.
func (r *RepositoryVMRegistry) HasRecords() (bool, error) {
	ownersRoot := filepath.Join(r.stateRoot, "owners")
	owners, err := os.ReadDir(ownersRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, owner := range owners {
		if !owner.IsDir() || owner.Type()&os.ModeSymlink != 0 || !validOpaquePathComponent(owner.Name()) {
			return false, errors.New("repository owner registry contains an invalid entry")
		}
		repositoriesRoot := filepath.Join(ownersRoot, owner.Name(), "repositories")
		repositories, readErr := os.ReadDir(repositoriesRoot)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return false, readErr
		}
		for _, repository := range repositories {
			if !repository.IsDir() || repository.Type()&os.ModeSymlink != 0 || !validOpaquePathComponent(repository.Name()) {
				return false, errors.New("repository registry contains an invalid entry")
			}
			info, statErr := os.Lstat(filepath.Join(repositoriesRoot, repository.Name(), "registry.json"))
			if statErr == nil {
				if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
					return false, errors.New("repository registry record is not a regular file")
				}
				return true, nil
			}
			if !errors.Is(statErr, os.ErrNotExist) {
				return false, statErr
			}
		}
	}
	return false, nil
}

// Ensure admits one generation on first use or reattaches only the exact healthy
// ready generation. Any partial, missing, or mismatched state fails without
// replacement or destructive reconciliation.
func (r *RepositoryVMRegistry) Ensure(ctx context.Context, request RepositoryVMRequest) (RepositoryVMResult, error) { //nolint:gocyclo // first-generation admission is one auditable transaction
	identity, err := ResolveRepositoryIdentity(ctx, request.Owner, request.Checkout, r.stateRoot)
	if err != nil {
		return RepositoryVMResult{}, err
	}
	validatedIdentity, err := r.validateIdentity(identity)
	if err != nil {
		return RepositoryVMResult{}, err
	}
	directory, err := r.openIdentityDirectory(validatedIdentity, true)
	if err != nil {
		return RepositoryVMResult{}, err
	}
	defer func() { _ = directory.Close() }()

	var result RepositoryVMResult
	err = r.withIdentityLock(ctx, directory, func() error {
		record, readErr := readRepositoryRecord(directory)
		switch {
		case readErr == nil:
			if err := r.reattach(ctx, directory, identity, record); err == nil {
				result = RepositoryVMResult{Record: record, Reattached: true}
				return nil
			} else if recoverErr := r.recover(ctx, directory, identity, &record); recoverErr != nil {
				return errors.Join(err, recoverErr)
			}
			result = RepositoryVMResult{Record: record, Reattached: true}
			return nil
		case !errors.Is(readErr, os.ErrNotExist):
			return readErr
		}

		if request.Artifacts == nil {
			return errors.New("repository first launch requires a locked artifact snapshot")
		}
		verified, release, err := request.Artifacts(ctx)
		if err != nil {
			return err
		}
		if release == nil {
			return errors.New("repository artifact snapshot omitted release")
		}
		defer release()
		if err := validateRepositoryArtifacts(verified); err != nil {
			return err
		}
		generation, err := randomGeneration()
		if err != nil {
			return err
		}
		bootGeneration, err := randomGeneration()
		if err != nil {
			return err
		}
		authority, err := newRepositoryBootAuthority()
		if err != nil {
			return fmt.Errorf("create repository boot authority: %w", err)
		}
		retained, artifactIdentities, err := retainRepositoryArtifacts(directory, verified)
		if err != nil {
			return fmt.Errorf("retain admitted repository artifacts: %w", err)
		}
		guestAgentHash, err := regularFileSHA256(filepath.Join(retained.GuestAgent.Path, guestAgentArtifactName))
		if err != nil {
			return fmt.Errorf("hash retained guest agent: %w", err)
		}
		stagingName, err := newRepositoryRootFSStagingName()
		if err != nil {
			return err
		}
		record = RepositoryVMRecord{
			State: EnvironmentProvisioning, Owner: identity.Owner, RepositoryKey: identity.Key,
			GitCommonDirectory: identity.GitCommonDirectory, Generation: generation,
			RootFSPath: filepath.Join(identity.StateDirectory, "rootfs"), RootFSPhase: repositoryRootFSStaging, RootFSStagingName: stagingName,
			Artifacts:      artifactIdentities,
			PolicyRevision: r.artifactPolicy, GuestEgressDigest: guestEgressFingerprint(r.guestEgress), GuestAgentExecutableHash: guestAgentHash,
			Boot: r.newBootRecord(identity, bootGeneration, authority),
		}
		record.syncBootFields()
		if err := r.writeRecord(directory, record); err != nil {
			return fmt.Errorf("admit repository generation: %w", err)
		}
		if err := writeRepositoryBootAuthority(directory, authority); err != nil {
			return fmt.Errorf("persist repository boot authority: %w", err)
		}
		if err := r.materializeAndPublishRootFS(directory, &record, retained); err != nil {
			return err
		}
		if err := unix.Mkdirat(int(directory.file.Fd()), "logical", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("create repository guest mount namespace: %w", err)
		}
		status, err := r.runtime.Start(ctx, record, retained, authority)
		if err != nil {
			return fmt.Errorf("start admitted repository generation: %w", err)
		}
		if err := validateProvisionalRuntime(record, status); err != nil {
			return errors.Join(err, r.abortStartedRuntime(ctx, record))
		}
		record.State = EnvironmentReady
		record.Boot.RunnerPID = status.PID
		record.Boot.ProcessIdentity = status.ProcessIdentity
		record.syncBootFields()
		if err := r.writeRecord(directory, record); err != nil {
			return errors.Join(fmt.Errorf("commit ready repository generation: %w", err), r.abortStartedRuntime(ctx, record))
		}
		result = RepositoryVMResult{Record: record}
		return nil
	})
	return result, err
}

func (r *RepositoryVMRegistry) recover(ctx context.Context, directory *repositoryDirectory, identity RepositoryIdentity, record *RepositoryVMRecord) error {
	if err := r.validateRepositoryDurableRecord(identity, *record); err != nil {
		return err
	}
	artifacts, err := loadRepositoryArtifacts(directory, *record)
	if err != nil {
		return fmt.Errorf("%w: retained repository artifacts: %v", ErrRepositoryVMInconsistent, err)
	}
	if record.State == EnvironmentProvisioning && record.RootFSPhase == repositoryRootFSStaging {
		if err := r.materializeAndPublishRootFS(directory, record, artifacts); err != nil {
			return err
		}
	}
	if err := validateRepositoryRootFS(directory, record.GuestAgentExecutableHash); err != nil {
		return fmt.Errorf("%w: %v", ErrRepositoryVMInconsistent, err)
	}
	if err := r.validateRecoveryConfiguration(*record); err != nil {
		return err
	}
	reconciler, ok := r.runtime.(repositoryRuntimeReconciler)
	if !ok {
		return fmt.Errorf("%w: repository runtime recovery is unavailable", ErrRepositoryVMInconsistent)
	}
	if err := reconciler.Reconcile(ctx, *record); err != nil {
		return fmt.Errorf("reconcile previous repository boot: %w", err)
	}
	bootGeneration, err := randomGeneration()
	if err != nil {
		return err
	}
	authority, err := newRepositoryBootAuthority()
	if err != nil {
		return err
	}
	record.State = EnvironmentProvisioning
	record.Boot = r.newBootRecord(identity, bootGeneration, authority)
	record.syncBootFields()
	if err := r.writeRecord(directory, *record); err != nil {
		return fmt.Errorf("persist replacement repository boot intent: %w", err)
	}
	if err := replaceRepositoryBootAuthority(directory, authority); err != nil {
		return fmt.Errorf("rotate repository boot authority: %w", err)
	}
	status, err := r.runtime.Start(ctx, *record, artifacts, authority)
	if err != nil {
		return fmt.Errorf("start replacement repository boot: %w", err)
	}
	if err := validateProvisionalRuntime(*record, status); err != nil {
		return errors.Join(err, r.abortStartedRuntime(ctx, *record))
	}
	record.State = EnvironmentReady
	record.Boot.RunnerPID, record.Boot.ProcessIdentity = status.PID, status.ProcessIdentity
	record.syncBootFields()
	if err := r.writeRecord(directory, *record); err != nil {
		return errors.Join(fmt.Errorf("commit replacement repository boot: %w", err), r.abortStartedRuntime(ctx, *record))
	}
	return nil
}

func newRepositoryRootFSStagingName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".rootfs-staging-" + hex.EncodeToString(random[:]), nil
}

func (r *RepositoryVMRegistry) materializeAndPublishRootFS(directory *repositoryDirectory, record *RepositoryVMRecord, artifacts VerifiedArtifacts) error {
	if record.RootFSPhase != repositoryRootFSStaging || !strings.HasPrefix(record.RootFSStagingName, ".rootfs-staging-") || !validOpaquePathComponent(record.RootFSStagingName) {
		return fmt.Errorf("%w: invalid repository rootfs publication transaction", ErrRepositoryVMInconsistent)
	}
	if info, err := directory.Lstat("rootfs"); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: private repository rootfs is not a directory", ErrRepositoryVMInconsistent)
		}
		if err := validateRepositoryRootFS(directory, record.GuestAgentExecutableHash); err != nil {
			return fmt.Errorf("%w: published repository rootfs is invalid: %v", ErrRepositoryVMInconsistent, err)
		}
		record.RootFSPhase = repositoryRootFSPublished
		record.RootFSStagingName = ""
		if err := r.writeRecord(directory, *record); err != nil {
			return fmt.Errorf("commit published repository rootfs: %w", err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private repository rootfs: %w", err)
	}

	stagingPath := filepath.Join(directory.path, record.RootFSStagingName)
	if info, err := os.Lstat(stagingPath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: repository rootfs staging path is invalid", ErrRepositoryVMInconsistent)
		}
		if err := os.RemoveAll(stagingPath); err != nil {
			return fmt.Errorf("reset incomplete repository rootfs staging transaction: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect repository rootfs staging transaction: %w", err)
	}
	materializer := newRepositoryRootFSMaterializer()
	if err := materializer.Materialize(artifacts.ExecutionImage.Path, stagingPath, artifacts.GuestAgent.Path); err != nil {
		return fmt.Errorf("materialize staged repository rootfs: %w", err)
	}
	if err := renameatNoReplace(int(directory.file.Fd()), record.RootFSStagingName, int(directory.file.Fd()), "rootfs"); err != nil {
		return fmt.Errorf("publish repository rootfs without overwrite: %w", err)
	}
	if err := directory.file.Sync(); err != nil {
		return fmt.Errorf("sync published repository rootfs: %w", err)
	}
	record.RootFSPhase = repositoryRootFSPublished
	record.RootFSStagingName = ""
	if err := r.writeRecord(directory, *record); err != nil {
		return fmt.Errorf("commit published repository rootfs: %w", err)
	}
	return nil
}

func (r *RepositoryVMRegistry) newBootRecord(identity RepositoryIdentity, generation uint32, authority RepositoryBootAuthority) RepositoryBootRecord {
	return RepositoryBootRecord{
		Generation: generation,
		VMID:       "repository-" + identity.Key[:16] + fmt.Sprintf("-%08x", generation),
		Endpoint:   r.repositoryEndpoint(identity, generation), AuthorityDigest: authority.digest(),
	}
}

func (r *RepositoryVMRegistry) validateRecoveryConfiguration(record RepositoryVMRecord) error {
	if r.artifactPolicy != "" && record.PolicyRevision != r.artifactPolicy {
		return fmt.Errorf("%w: configured artifact policy differs from admitted repository policy", ErrRepositoryVMInconsistent)
	}
	for kind, request := range r.artifactRequests {
		stored := record.Artifacts.ByKind(kind)
		if stored.Kind != kind || stored.Digest != request.Digest || stored.ManifestDigest != request.ManifestDigest || stored.Platform != request.Platform || stored.DiscoveryReference != request.DiscoveryReference || stored.ResolutionEvidence != request.ResolutionEvidence {
			return fmt.Errorf("%w: configured %s artifact differs from admitted repository artifact", ErrRepositoryVMInconsistent, kind)
		}
	}
	if record.GuestEgressDigest != guestEgressFingerprint(r.guestEgress) {
		return fmt.Errorf("%w: configured guest egress differs from admitted repository policy", ErrRepositoryVMInconsistent)
	}
	return nil
}

func (r *RepositoryVMRegistry) abortStartedRuntime(ctx context.Context, record RepositoryVMRecord) error {
	aborter, ok := r.runtime.(repositoryRuntimeAborter)
	if !ok {
		return errors.New("started repository runtime cannot be rolled back")
	}
	if err := aborter.Abort(ctx, record); err != nil {
		return fmt.Errorf("abort unpublished repository generation: %w", err)
	}
	return nil
}

func (r *RepositoryVMRegistry) reattach(ctx context.Context, directory *repositoryDirectory, identity RepositoryIdentity, record RepositoryVMRecord) error {
	if err := r.validateRepositoryRecord(identity, record); err != nil {
		return err
	}
	if err := validateRepositoryRootFS(directory, record.GuestAgentExecutableHash); err != nil {
		return fmt.Errorf("%w: %v", ErrRepositoryVMInconsistent, err)
	}
	authority, err := readRepositoryBootAuthority(directory)
	if err != nil || record.AuthorityDigest != authority.digest() {
		return fmt.Errorf("%w: repository boot authority is missing or inconsistent", ErrRepositoryVMInconsistent)
	}
	challenge, err := authority.healthChallenge(record)
	if err != nil {
		return fmt.Errorf("create repository health challenge: %w", err)
	}
	healthCtx, cancelHealth := context.WithTimeoutCause(ctx, repositoryHealthTimeout, errors.New("repository restart health phase timed out"))
	response, err := r.runtime.Health(healthCtx, record, challenge)
	cancelHealth()
	if err != nil {
		return fmt.Errorf("%w: repository restart health phase: %v", ErrRepositoryVMInconsistent, err)
	}
	if err := authority.VerifyHealth(record, challenge, response); err != nil {
		return fmt.Errorf("%w: repository health response is not authoritative", ErrRepositoryVMInconsistent)
	}
	if attacher, ok := r.runtime.(repositoryRuntimeAttacher); ok {
		if err := attacher.AttachRepository(record, authority); err != nil {
			return fmt.Errorf("%w: restore repository runtime authority: %v", ErrRepositoryVMInconsistent, err)
		}
	}
	return validateRepositoryRuntime(record, response.Status)
}

// Lookup returns the exact durable record without inspecting or changing runtime state.
func (r *RepositoryVMRegistry) Lookup(ctx context.Context, identity RepositoryIdentity) (RepositoryVMRecord, error) {
	validatedIdentity, err := r.validateIdentity(identity)
	if err != nil {
		return RepositoryVMRecord{}, err
	}
	directory, err := r.openIdentityDirectory(validatedIdentity, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepositoryVMRecord{}, ErrRepositoryVMUnknown
		}
		return RepositoryVMRecord{}, err
	}
	defer func() { _ = directory.Close() }()
	var record RepositoryVMRecord
	err = r.withIdentityLock(ctx, directory, func() error {
		var err error
		record, err = readRepositoryRecord(directory)
		if errors.Is(err, os.ErrNotExist) {
			return ErrRepositoryVMUnknown
		}
		if err != nil {
			return err
		}
		return r.validateRepositoryRecord(identity, record)
	})
	return record, err
}

// inspect authenticates the live runtime for an exact durable repository record
// without attaching handles or creating replacement state.
func (r *RepositoryVMRegistry) inspect(ctx context.Context, record RepositoryVMRecord) error {
	identity, err := newValidatedRepositoryIdentity(record.Owner, record.GitCommonDirectory, r.stateRoot)
	if err != nil || identity.value.Key != record.RepositoryKey {
		return ErrRepositoryVMInconsistent
	}
	directory, err := r.openIdentityDirectory(identity, false)
	if err != nil {
		return fmt.Errorf("%w: open repository generation: %v", ErrRepositoryVMInconsistent, err)
	}
	defer func() { _ = directory.Close() }()
	if err := r.validateRepositoryRecord(identity.value, record); err != nil {
		return err
	}
	authority, err := readRepositoryBootAuthority(directory)
	if err != nil || record.AuthorityDigest != authority.digest() {
		return fmt.Errorf("%w: repository boot authority is missing or inconsistent", ErrRepositoryVMInconsistent)
	}
	challenge, err := authority.healthChallenge(record)
	if err != nil {
		return err
	}
	healthCtx, cancelHealth := context.WithTimeoutCause(ctx, repositoryHealthTimeout, errors.New("repository inventory health phase timed out"))
	response, err := r.runtime.Health(healthCtx, record, challenge)
	cancelHealth()
	if err != nil {
		return err
	}
	if err := authority.VerifyHealth(record, challenge, response); err != nil {
		return fmt.Errorf("%w: repository health response is not authoritative", ErrRepositoryVMInconsistent)
	}
	return validateRepositoryRuntime(record, response.Status)
}

type repositoryDirectory struct {
	file *os.File
	path string
}

func (d *repositoryDirectory) Close() error { return d.file.Close() }

func openatOpaque(directoryFD int, component string, flags int, mode uint32) (int, error) {
	if !validOpaquePathComponent(component) {
		return -1, fmt.Errorf("openat path component %q is not opaque", component)
	}
	return unix.Openat(directoryFD, component, flags, mode)
}

func (d *repositoryDirectory) Lstat(name string) (os.FileInfo, error) {
	fd, err := openatOpaque(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), filepath.Join(d.path, name))
	defer func() { _ = file.Close() }()
	return file.Stat()
}

func (r *RepositoryVMRegistry) openIdentityDirectory(identity validatedRepositoryIdentity, create bool) (*repositoryDirectory, error) {
	rootFD, err := unix.Open(r.stateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open repository registry root without symlinks: %w", err)
	}
	current := os.NewFile(uintptr(rootFD), r.stateRoot)
	currentPath := r.stateRoot
	for _, component := range identity.components {
		if create {
			if err := unix.Mkdirat(int(current.Fd()), component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = current.Close()
				return nil, fmt.Errorf("create repository registry namespace %q: %w", component, err)
			}
		}
		nextFD, err := openatOpaque(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("open repository registry namespace %q without symlinks: %w", component, err)
		}
		next := os.NewFile(uintptr(nextFD), currentPath)
		info, statErr := next.Stat()
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			_ = next.Close()
			_ = current.Close()
			return nil, fmt.Errorf("repository registry namespace %q is not a private directory", component)
		}
		_ = current.Close()
		currentPath = filepath.Join(currentPath, component)
		current = next
	}
	return &repositoryDirectory{file: current, path: currentPath}, nil
}

func (*RepositoryVMRegistry) withIdentityLock(ctx context.Context, directory *repositoryDirectory, fn func() error) error {
	fd, err := openatOpaque(int(directory.file.Fd()), "registry.lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open repository generation lock without symlinks: %w", err)
	}
	lock := os.NewFile(uintptr(fd), filepath.Join(directory.path, "registry.lock"))
	defer func() { _ = lock.Close() }()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock repository generation: %w", err)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	return fn()
}

func (r *RepositoryVMRegistry) repositoryEndpoint(identity RepositoryIdentity, generation uint32) string {
	return filepath.Join(r.endpointRoot, "repository-"+identity.Key[:16]+fmt.Sprintf("-%08x.sock", generation))
}

func (r *RepositoryVMRegistry) validateRepositoryRecord(identity RepositoryIdentity, record RepositoryVMRecord) error {
	if record.State != EnvironmentReady || record.RunnerPID <= 0 || record.ProcessIdentity == "" {
		return ErrRepositoryVMInconsistent
	}
	return r.validateRepositoryDurableRecord(identity, record)
}

func validRepositoryRootFSState(record RepositoryVMRecord) bool {
	if record.RootFSPhase == repositoryRootFSPublished {
		return record.RootFSStagingName == ""
	}
	return record.State == EnvironmentProvisioning && record.RootFSPhase == repositoryRootFSStaging &&
		strings.HasPrefix(record.RootFSStagingName, ".rootfs-staging-") && validOpaquePathComponent(record.RootFSStagingName)
}

func (r *RepositoryVMRegistry) validateRepositoryDurableRecord(identity RepositoryIdentity, record RepositoryVMRecord) error {
	expectedRootFS := filepath.Join(identity.StateDirectory, "rootfs")
	expectedEndpoint := r.repositoryEndpoint(identity, record.bootGeneration())
	if (record.State != EnvironmentReady && record.State != EnvironmentProvisioning) || record.Owner != identity.Owner || record.RepositoryKey != identity.Key ||
		record.GitCommonDirectory != identity.GitCommonDirectory || record.Generation == 0 || record.bootGeneration() == 0 || record.VMID == "" ||
		record.RootFSPath != expectedRootFS || !validRepositoryRootFSState(record) || record.Endpoint != expectedEndpoint || record.AuthorityDigest == "" || !record.Artifacts.complete() || record.GuestEgressDigest == "" || record.GuestAgentExecutableHash == "" {
		return ErrRepositoryVMInconsistent
	}
	return nil
}

func validateRepositoryArtifacts(verified VerifiedArtifacts) error {
	for _, artifact := range verified.All() {
		if artifact.Kind == "" || artifact.Digest == "" || !filepath.IsAbs(artifact.Path) {
			return errors.New("complete verified repository artifacts are required")
		}
	}
	return nil
}

func retainRepositoryArtifacts(directory *repositoryDirectory, verified VerifiedArtifacts) (VerifiedArtifacts, RepositoryArtifactSet, error) {
	if err := validateRepositoryArtifacts(verified); err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	final := filepath.Join(directory.path, "launch-artifacts")
	if info, err := os.Lstat(final); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return VerifiedArtifacts{}, RepositoryArtifactSet{}, errors.New("retained artifact root is invalid")
		}
		identities := artifactIdentitiesAt(final, verified)
		record := RepositoryVMRecord{Artifacts: identities}
		loaded, loadErr := loadRepositoryArtifacts(directory, record)
		return loaded, identities, loadErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	name := ".launch-artifacts-" + hex.EncodeToString(random[:])
	staging := filepath.Join(directory.path, name)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	retained, err := snapshotArtifacts(verified, staging)
	if err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	if err := os.Rename(staging, final); err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	if err := directory.file.Sync(); err != nil {
		return VerifiedArtifacts{}, RepositoryArtifactSet{}, err
	}
	identities := artifactIdentitiesAt(final, retained)
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		identity := identities.ByKind(kind)
		artifact := retained.ByKind(kind)
		artifact.Path = identity.Path
		switch kind {
		case ArtifactRuntime:
			retained.Runtime = artifact
		case ArtifactFirmware:
			retained.Firmware = artifact
		case ArtifactExecutionImage:
			retained.ExecutionImage = artifact
		case ArtifactGuestAgent:
			retained.GuestAgent = artifact
		}
	}
	return retained, identities, nil
}

func artifactIdentitiesAt(root string, artifacts VerifiedArtifacts) RepositoryArtifactSet {
	var identities RepositoryArtifactSet
	for _, artifact := range artifacts.All() {
		identity := RepositoryArtifactIdentity{
			Kind: artifact.Kind, Digest: artifact.Digest, ManifestDigest: artifact.ManifestDigest,
			DiscoveryReference: artifact.DiscoveryReference, ResolutionEvidence: artifact.ResolutionEvidence,
			Platform: artifact.Platform, Path: filepath.Join(root, string(artifact.Kind)),
		}
		switch artifact.Kind {
		case ArtifactRuntime:
			identities.Runtime = identity
		case ArtifactFirmware:
			identities.Firmware = identity
		case ArtifactExecutionImage:
			identities.ExecutionImage = identity
		case ArtifactGuestAgent:
			identities.GuestAgent = identity
		}
	}
	return identities
}

func loadRepositoryArtifacts(directory *repositoryDirectory, record RepositoryVMRecord) (VerifiedArtifacts, error) {
	root := filepath.Join(directory.path, "launch-artifacts")
	fd, err := openatOpaque(int(directory.file.Fd()), "launch-artifacts", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return VerifiedArtifacts{}, err
	}
	_ = unix.Close(fd)
	var result VerifiedArtifacts
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		identity := record.Artifacts.ByKind(kind)
		expectedPath := filepath.Join(root, string(kind))
		if identity.Kind != kind || identity.Path != expectedPath || identity.Digest == "" {
			return VerifiedArtifacts{}, errors.New("retained artifact identity is incomplete")
		}
		digest, err := digestTree(expectedPath)
		if err != nil || digest != identity.Digest {
			return VerifiedArtifacts{}, ErrCorruptCacheEntry
		}
		artifact := VerifiedArtifact{Kind: kind, Digest: identity.Digest, ManifestDigest: identity.ManifestDigest, DiscoveryReference: identity.DiscoveryReference, ResolutionEvidence: identity.ResolutionEvidence, Platform: identity.Platform, Path: expectedPath}
		switch kind {
		case ArtifactRuntime:
			result.Runtime = artifact
		case ArtifactFirmware:
			result.Firmware = artifact
		case ArtifactExecutionImage:
			result.ExecutionImage = artifact
		case ArtifactGuestAgent:
			result.GuestAgent = artifact
		}
	}
	return result, nil
}

func regularFileSHA256(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("artifact executable is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneArtifactRequests(requests map[ArtifactKind]ArtifactRequest) map[ArtifactKind]ArtifactRequest {
	if len(requests) == 0 {
		return nil
	}
	cloned := make(map[ArtifactKind]ArtifactRequest, len(requests))
	for kind, request := range requests {
		cloned[kind] = request
	}
	return cloned
}

func cloneGuestEgressPolicy(policy GuestEgressPolicy) GuestEgressPolicy {
	return GuestEgressPolicy{Mode: policy.normalizedMode(), Allow: append([]EgressDestination(nil), policy.Allow...)}
}

func guestEgressFingerprint(policy GuestEgressPolicy) string {
	policy = cloneGuestEgressPolicy(policy)
	encoded, _ := json.Marshal(policy)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateRepositoryRootFS(directory *repositoryDirectory, expectedGuestAgentHash string) error {
	rootFD, err := openatOpaque(int(directory.file.Fd()), "rootfs", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("private repository rootfs is missing or invalid")
	}
	root := os.NewFile(uintptr(rootFD), filepath.Join(directory.path, "rootfs"))
	defer func() { _ = root.Close() }()
	current := root
	components := strings.Split(guestAgentInstallPath, string(filepath.Separator))
	for index, component := range components {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := openatOpaque(int(current.Fd()), component, flags, 0)
		if err != nil {
			if current != root {
				_ = current.Close()
			}
			return errors.New("private repository rootfs has no executable guest agent")
		}
		next := os.NewFile(uintptr(fd), filepath.Join(directory.path, "rootfs", filepath.Join(components[:index+1]...)))
		if current != root {
			_ = current.Close()
		}
		current = next
	}
	defer func() { _ = current.Close() }()
	info, err := current.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("private repository rootfs has no executable guest agent")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, current); err != nil || hex.EncodeToString(hash.Sum(nil)) != expectedGuestAgentHash {
		return errors.New("private repository rootfs guest agent identity changed")
	}
	return nil
}

func validateProvisionalRuntime(record RepositoryVMRecord, status RuntimeStatus) error {
	if !status.Live || status.Generation != record.bootGeneration() || status.VMID != record.VMID || status.Endpoint != record.Endpoint || status.PID <= 0 || status.ProcessIdentity == "" {
		return ErrRepositoryVMInconsistent
	}
	return nil
}

func validateRepositoryRuntime(record RepositoryVMRecord, status RuntimeStatus) error {
	if err := validateProvisionalRuntime(record, status); err != nil || status.PID != record.RunnerPID || status.ProcessIdentity != record.ProcessIdentity {
		return ErrRepositoryVMInconsistent
	}
	return nil
}

func randomGeneration() (uint32, error) {
	var bytes [4]byte
	for {
		if _, err := rand.Read(bytes[:]); err != nil {
			return 0, fmt.Errorf("allocate repository generation: %w", err)
		}
		if generation := binary.BigEndian.Uint32(bytes[:]); generation != 0 {
			return generation, nil
		}
	}
}

func readRepositoryBootAuthority(directory *repositoryDirectory) (RepositoryBootAuthority, error) {
	fd, err := openatOpaque(int(directory.file.Fd()), "authority.key", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return RepositoryBootAuthority{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.path, "authority.key"))
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != repositoryAuthorityBytes {
		return RepositoryBootAuthority{}, errors.New("repository boot authority is not a private regular file")
	}
	value, err := io.ReadAll(io.LimitReader(file, repositoryAuthorityBytes+1))
	if err != nil {
		return RepositoryBootAuthority{}, err
	}
	return repositoryBootAuthorityFromBytes(value)
}

func writeRepositoryBootAuthority(directory *repositoryDirectory, authority RepositoryBootAuthority) error {
	fd, err := openatOpaque(int(directory.file.Fd()), "authority.key", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.path, "authority.key"))
	if _, err := file.Write(authority.bytes()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return directory.file.Sync()
}

func replaceRepositoryBootAuthority(directory *repositoryDirectory, authority RepositoryBootAuthority) error {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".authority-" + hex.EncodeToString(random[:])
	fd, err := openatOpaque(int(directory.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.path, name))
	defer func() { _ = unix.Unlinkat(int(directory.file.Fd()), name, 0) }()
	if _, err := file.Write(authority.bytes()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.file.Fd()), name, int(directory.file.Fd()), "authority.key"); err != nil {
		return err
	}
	return directory.file.Sync()
}

func readRepositoryRecord(directory *repositoryDirectory) (RepositoryVMRecord, error) {
	fd, err := openatOpaque(int(directory.file.Fd()), "registry.json", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return RepositoryVMRecord{}, &os.PathError{Op: "open", Path: "registry.json", Err: err}
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.path, "registry.json"))
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return RepositoryVMRecord{}, fmt.Errorf("repository generation registry is not a regular file: %w", err)
	}
	var document repositoryRegistryDocument
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return RepositoryVMRecord{}, fmt.Errorf("decode repository generation registry: %w", err)
	}
	if document.Version != repositoryRegistryVersion {
		return RepositoryVMRecord{}, fmt.Errorf("unsupported repository generation registry version %d; local microVM data was preserved, but this is incompatible local-development state; use the matching build to export needed work before replacing any state", document.Version)
	}
	document.Record.syncBootFields()
	return document.Record, nil
}

func writeRepositoryRecord(directory *repositoryDirectory, record RepositoryVMRecord) error {
	// Boot is the sole mutable source of truth. Reject a stale compatibility
	// mirror instead of silently overwriting either representation.
	if !record.bootFieldsMatch() {
		return errors.New("repository boot compatibility fields conflict with the authoritative boot record")
	}
	data, err := json.Marshal(repositoryRegistryDocument{Version: repositoryRegistryVersion, Record: record})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var name string
	var temporary *os.File
	for attempt := 0; attempt < 100; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		name = ".registry-" + hex.EncodeToString(random[:])
		fd, openErr := openatOpaque(int(directory.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if openErr == nil {
			temporary = os.NewFile(uintptr(fd), filepath.Join(directory.path, name))
			break
		}
		if !errors.Is(openErr, unix.EEXIST) {
			return openErr
		}
	}
	if temporary == nil {
		return errors.New("allocate repository registry temporary file")
	}
	defer func() { _ = unix.Unlinkat(int(directory.file.Fd()), name, 0) }()
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
	if err := unix.Renameat(int(directory.file.Fd()), name, int(directory.file.Fd()), "registry.json"); err != nil {
		return err
	}
	return directory.file.Sync()
}
