// Package skillstore persists agent-owned skill lifecycle records in a flocked,
// crash-safe manifest with immutable content-addressed SKILL.md files.
package skillstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
)

const (
	manifestName = "manifest.json"
	lockName     = "skills.lock"
	versionsDir  = "versions"
	maxManifest  = 16 << 20
	lockRetry    = 5 * time.Millisecond
	lockTimeout  = 5 * time.Second
)

type skillRecord struct {
	Owner    string                  `json:"owner"`
	Name     string                  `json:"name"`
	Versions []learning.SkillVersion `json:"versions"`
}

type manifest struct {
	Format     string                                      `json:"format"`
	Partitions map[string]map[learning.SkillID]skillRecord `json:"partitions"`
	Receipts   map[string][]learning.SkillReceiptRecord    `json:"receipts,omitempty"`
}

// Store is a durable learning.SkillRepository. New is lazy: an empty repository
// does not create its directory until the first mutation.
type Store struct {
	mu    sync.Mutex
	dir   string
	path  string
	lock  *flock.Flock
	now   func() time.Time
	fault func(string) error // test-only crash-boundary injection; nil in production
}

var _ learning.SkillRepository = (*Store)(nil)

// New prepares a repository rooted at dir without creating it.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("skillstore: directory required")
	}
	abs, err := canonicalStoreDir(dir)
	if err != nil {
		return nil, err
	}
	return &Store{dir: abs, path: filepath.Join(abs, manifestName), lock: flock.New(filepath.Join(abs, lockName)), now: time.Now}, nil
}

// canonicalStoreDir rejects a symlink used as the configured repository root,
// but canonicalizes symlinked ancestors such as macOS's /var -> /private/var.
// The stored physical path lets the later layout checks protect the repository
// itself without rejecting an otherwise ordinary directory beneath /var.
func canonicalStoreDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("skillstore: symlink repository root rejected: %s", abs)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	var missing []string
	ancestor := abs
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("skillstore: no existing ancestor for %s", abs)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	physical, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		physical = filepath.Join(physical, missing[i])
	}
	return physical, nil
}

func rejectPathSymlinks(path string) error {
	clean := filepath.Clean(path)
	parts := []string{}
	for current := clean; ; current = filepath.Dir(current) {
		parts = append(parts, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		info, err := os.Lstat(parts[i])
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skillstore: symlink path rejected: %s", parts[i])
		}
	}
	return nil
}

func emptyManifest() manifest {
	return manifest{Format: "mecatl-skillstore/1", Partitions: map[string]map[learning.SkillID]skillRecord{}, Receipts: map[string][]learning.SkillReceiptRecord{}}
}

func partitionKey(p learning.SkillPartition) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func skillID(p learning.SkillPartition, owner, name string) learning.SkillID {
	raw, _ := json.Marshal([]string{p.Principal, p.Project, owner, name})
	sum := sha256.Sum256(raw)
	return learning.SkillID("skill-" + hex.EncodeToString(sum[:16]))
}

func revision() (learning.Revision, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return learning.Revision("local-" + hex.EncodeToString(value[:])), nil
}

func cloneSignal(value learning.Signal) learning.Signal {
	value.Evidence = append([]learning.EvidenceRef(nil), value.Evidence...)
	return value
}

func clone(value learning.SkillVersion) learning.SkillVersion {
	value.Provenance.ProposalIDs = append([]learning.ProposalID(nil), value.Provenance.ProposalIDs...)
	value.Provenance.EvidenceRefs = append([]learning.EvidenceRef(nil), value.Provenance.EvidenceRefs...)
	value.Provenance.Signals = append([]learning.Signal(nil), value.Provenance.Signals...)
	for i := range value.Provenance.Signals {
		value.Provenance.Signals[i] = cloneSignal(value.Provenance.Signals[i])
	}
	value.Evaluations = append([]learning.SkillEvaluation(nil), value.Evaluations...)
	for i := range value.Evaluations {
		value.Evaluations[i].FixtureIDs = append([]string(nil), value.Evaluations[i].FixtureIDs...)
	}
	value.Receipts = append([]learning.SkillReceipt(nil), value.Receipts...)
	return value
}

func (s *Store) ensureDir() error {
	_, statErr := os.Lstat(s.dir)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if os.IsNotExist(statErr) {
		if err := syncDir(filepath.Dir(s.dir)); err != nil {
			return err
		}
	}
	return rejectPathSymlinks(s.dir)
}

func (s *Store) locked(ctx context.Context, write bool, fn func(*manifest) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !write {
		if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
			doc := emptyManifest()
			return fn(&doc)
		} else if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := rejectKnownSymlinks(s.dir); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	var ok bool
	var err error
	if write {
		ok, err = s.lock.TryLockContext(bounded, lockRetry)
	} else {
		ok, err = s.lock.TryRLockContext(bounded, lockRetry)
	}
	if err != nil || !ok {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return err
	}
	defer func() { _ = s.lock.Unlock() }()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if err = fn(&doc); err != nil {
		return err
	}
	if write {
		return s.save(doc)
	}
	return nil
}

func boundedReadRegular(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skillstore: non-regular file rejected: %s", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, learning.ErrSkillLimit
	}
	return raw, nil
}

func (s *Store) load() (manifest, error) {
	if err := rejectKnownSymlinks(s.dir); err != nil {
		return manifest{}, err
	}
	raw, err := boundedReadRegular(s.path, maxManifest)
	if os.IsNotExist(err) {
		return emptyManifest(), nil
	}
	if err != nil {
		return manifest{}, err
	}
	if len(raw) > maxManifest {
		return manifest{}, learning.ErrSkillLimit
	}
	var doc manifest
	if err = json.Unmarshal(raw, &doc); err != nil {
		return manifest{}, err
	}
	if doc.Format != "mecatl-skillstore/1" || doc.Partitions == nil {
		return manifest{}, fmt.Errorf("%w: invalid manifest format", learning.ErrInvalidSkill)
	}
	if doc.Receipts == nil {
		doc.Receipts = rebuildReceiptIndex(doc.Partitions)
	}
	if err = s.validateManifest(doc); err != nil {
		return manifest{}, err
	}
	return doc, nil
}

func rejectKnownSymlinks(dir string) error {
	for _, path := range []string{filepath.Join(dir, manifestName), filepath.Join(dir, lockName), filepath.Join(dir, versionsDir)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skillstore: symlink path rejected: %s", path)
		}
	}
	return nil
}

func (s *Store) validateManifest(doc manifest) error { //nolint:gocyclo // validates every persisted identity, bound, and lifecycle invariant
	for key, bucket := range doc.Partitions {
		if len(bucket) > learning.MaxSkillsPerPartition {
			return learning.ErrSkillLimit
		}
		activeNames := map[string]bool{}
		for id, record := range bucket {
			if len(record.Versions) == 0 || len(record.Versions) > learning.MaxSkillVersionsPerSkill {
				return learning.ErrSkillLimit
			}
			for i, value := range record.Versions {
				if id != value.ID || record.Owner != value.OwnerAgent || record.Name != value.Bundle.Name || key != partitionKey(value.Partition) || skillID(value.Partition, value.OwnerAgent, value.Bundle.Name) != value.ID || value.Revision == "" || !value.State.Valid() || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
					return fmt.Errorf("%w: corrupt skill identity or lifecycle", learning.ErrInvalidSkill)
				}
				if err := learning.ValidateSkillPartition(value.Partition, value.OwnerAgent); err != nil {
					return err
				}
				if err := learning.ValidateSkillBundle(value.Bundle); err != nil {
					return err
				}
				if err := learning.ValidateSkillProvenance(value.Provenance); err != nil {
					return err
				}
				if value.Disposition != value.Provenance.ValidationDisposition || value.Disposition != "" && !value.Disposition.Valid() {
					return fmt.Errorf("%w: corrupt validation disposition", learning.ErrInvalidSkill)
				}
				version, _ := learning.SkillVersionID(value.Bundle)
				if version != value.Version || len(value.Evaluations) > learning.MaxSkillEvaluations || len(value.Receipts) > learning.MaxSkillReceipts {
					return fmt.Errorf("%w: corrupt skill version", learning.ErrInvalidSkill)
				}
				if i > 0 && value.Supersedes != record.Versions[i-1].Version {
					return fmt.Errorf("%w: corrupt supersedes chain", learning.ErrInvalidSkill)
				}
				for _, evaluation := range value.Evaluations {
					if err := learning.ValidateSkillEvaluation(evaluation); err != nil {
						return err
					}
				}
				if value.State == learning.SkillActive {
					if activeNames[value.Bundle.Name] {
						return fmt.Errorf("%w: multiple active versions", learning.ErrInvalidSkill)
					}
					activeNames[value.Bundle.Name] = true
				}
				if err := s.validateVersionFile(value); err != nil {
					return err
				}
			}
		}
	}
	for key, history := range doc.Receipts {
		if len(history) > learning.MaxSkillReceiptHistory {
			return learning.ErrSkillLimit
		}
		if _, ok := doc.Partitions[key]; !ok && len(history) > 0 {
			return fmt.Errorf("%w: receipt partition", learning.ErrInvalidSkill)
		}
		seen := map[string]bool{}
		for _, record := range history {
			if record.ID == "" || seen[record.ID] || record.SkillID == "" || record.Version == "" || record.Receipt.At.IsZero() || !record.Receipt.From.Valid() || !record.Receipt.To.Valid() {
				return fmt.Errorf("%w: receipt index", learning.ErrInvalidSkill)
			}
			seen[record.ID] = true
		}
	}
	return nil
}

func (s *Store) versionPath(version learning.VersionID) string {
	return filepath.Join(s.dir, versionsDir, string(version), skillfs.SkillFileName)
}

func renderBundle(bundle learning.SkillBundle) string {
	name, _ := json.Marshal(bundle.Name)
	description, _ := json.Marshal(bundle.Description)
	return "---\nname: " + string(name) + "\ndescription: " + string(description) + "\norigin: agent\n---\n\n" + strings.TrimSpace(bundle.Body) + "\n"
}

func (s *Store) validateVersionFile(value learning.SkillVersion) error {
	path := s.versionPath(value.Version)
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: invalid version directory", learning.ErrInvalidSkill)
	}
	info, err = os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: invalid version file", learning.ErrInvalidSkill)
	}
	raw, err := boundedReadRegular(path, learning.MaxSkillBodyBytes+learning.MaxSkillDescriptionBytes+1024)
	if err != nil {
		return err
	}
	if len(raw) > learning.MaxSkillBodyBytes+learning.MaxSkillDescriptionBytes+1024 || string(raw) != renderBundle(value.Bundle) {
		return fmt.Errorf("%w: immutable version content mismatch", learning.ErrInvalidSkill)
	}
	return nil
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Store) atomicWrite(kind, path string, mode os.FileMode, raw []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".skillstore-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = s.inject(kind + ".fsync")
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = s.inject(kind + ".rename"); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if err = s.inject(kind + ".dirsync"); err != nil {
		return err
	}
	return syncDir(dir)
}

func (s *Store) inject(step string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(step)
}

func (s *Store) ensureVersion(bundle learning.SkillBundle, version learning.VersionID) error {
	base := filepath.Join(s.dir, versionsDir)
	_, baseErr := os.Lstat(base)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if os.IsNotExist(baseErr) {
		if err := syncDir(s.dir); err != nil {
			return err
		}
	}
	if err := rejectKnownSymlinks(s.dir); err != nil {
		return err
	}
	dir := filepath.Join(base, string(version))
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skillstore: invalid version path %s", dir)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err = os.Mkdir(dir, 0o700); err != nil {
		return err
	} else if err = syncDir(base); err != nil {
		return err
	}
	path := filepath.Join(dir, skillfs.SkillFileName)
	raw := []byte(renderBundle(bundle))
	if existing, err := boundedReadRegular(path, int64(len(raw))); err == nil {
		if string(existing) != string(raw) {
			return fmt.Errorf("%w: content-address collision", learning.ErrInvalidSkill)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.atomicWrite("body", path, 0o600, raw)
}

func (s *Store) save(doc manifest) error {
	if err := s.validateManifest(doc); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxManifest {
		return learning.ErrSkillLimit
	}
	if err = s.atomicWrite("manifest", s.path, 0o600, raw); err != nil {
		return fmt.Errorf("skillstore: save manifest: %w", err)
	}
	return nil
}

func mergeStrings[T ~string](dst, src []T, limit int) ([]T, error) {
	seen := make(map[T]bool, len(dst)+len(src))
	out := append([]T(nil), dst...)
	for _, value := range out {
		seen[value] = true
	}
	for _, value := range src {
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	if len(out) > limit {
		return nil, learning.ErrSkillLimit
	}
	return out, nil
}

func mergeProvenance(dst, src learning.SkillProvenance) (learning.SkillProvenance, error) {
	if dst.Origin != src.Origin && dst.Origin != "" && src.Origin != "" {
		return dst, learning.ErrInvalidSkill
	}
	if dst.Origin == "" {
		dst.Origin = src.Origin
	}
	if dst.ValidationDisposition == learning.ValidationSimilarStageHint || src.ValidationDisposition == learning.ValidationSimilarStageHint {
		dst.ValidationDisposition = learning.ValidationSimilarStageHint
	} else if dst.ValidationDisposition == "" {
		dst.ValidationDisposition = src.ValidationDisposition
	}
	var err error
	dst.ProposalIDs, err = mergeStrings(dst.ProposalIDs, src.ProposalIDs, learning.MaxSkillProposals)
	if err != nil {
		return dst, err
	}
	seen := map[string]bool{}
	for _, value := range dst.EvidenceRefs {
		raw, _ := json.Marshal(value)
		seen["e"+string(raw)] = true
	}
	for _, value := range src.EvidenceRefs {
		raw, _ := json.Marshal(value)
		if !seen["e"+string(raw)] {
			dst.EvidenceRefs = append(dst.EvidenceRefs, value)
			seen["e"+string(raw)] = true
		}
	}
	if len(dst.EvidenceRefs) > learning.MaxSkillEvidence {
		return dst, learning.ErrSkillLimit
	}
	for _, value := range dst.Signals {
		raw, _ := json.Marshal(value)
		seen["s"+string(raw)] = true
	}
	for _, value := range src.Signals {
		raw, _ := json.Marshal(value)
		if !seen["s"+string(raw)] {
			dst.Signals = append(dst.Signals, cloneSignal(value))
			seen["s"+string(raw)] = true
		}
	}
	if len(dst.Signals) > learning.MaxSkillSignals {
		return dst, learning.ErrSkillLimit
	}
	return dst, nil
}

func locate(bucket map[learning.SkillID]skillRecord, owner string, id learning.SkillID, version learning.VersionID) (skillRecord, int, error) {
	record, ok := bucket[id]
	if !ok {
		return skillRecord{}, 0, learning.ErrSkillNotFound
	}
	if record.Owner != owner {
		return skillRecord{}, 0, learning.ErrSkillOwnerMismatch
	}
	if version == "" {
		return record, len(record.Versions) - 1, nil
	}
	for i := range record.Versions {
		if record.Versions[i].Version == version {
			return record, i, nil
		}
	}
	return skillRecord{}, 0, learning.ErrSkillNotFound
}

// CreateDraft creates an immutable body version or merges provenance into its exact duplicate.
func (s *Store) CreateDraft(ctx context.Context, p learning.SkillPartition, owner string, bundle learning.SkillBundle, provenance learning.SkillProvenance) (out learning.SkillVersion, err error) {
	if err = learning.ValidateSkillPartition(p, owner); err != nil {
		return
	}
	if err = learning.ValidateSkillBundle(bundle); err != nil {
		return
	}
	if err = learning.ValidateSkillProvenance(provenance); err != nil {
		return
	}
	version, _ := learning.SkillVersionID(bundle)
	err = s.locked(ctx, true, func(doc *manifest) error {
		key := partitionKey(p)
		bucket := doc.Partitions[key]
		if bucket == nil {
			bucket = map[learning.SkillID]skillRecord{}
		}
		id := skillID(p, owner, bundle.Name)
		for existingID, record := range bucket {
			if record.Name != bundle.Name {
				continue
			}
			if record.Owner != owner || existingID != id {
				return learning.ErrSkillOwnerMismatch
			}
			for i := range record.Versions {
				if record.Versions[i].Version != version {
					continue
				}
				merged, mergeErr := mergeProvenance(record.Versions[i].Provenance, provenance)
				if mergeErr != nil {
					return mergeErr
				}
				before, _ := json.Marshal(record.Versions[i].Provenance)
				after, _ := json.Marshal(merged)
				if string(before) != string(after) {
					rev, revErr := revision()
					if revErr != nil {
						return revErr
					}
					record.Versions[i].Provenance = merged
					record.Versions[i].Disposition = merged.ValidationDisposition
					record.Versions[i].Revision = rev
					record.Versions[i].UpdatedAt = s.now().UTC()
					bucket[id] = record
				}
				out = clone(record.Versions[i])
				return nil
			}
			if len(record.Versions) >= learning.MaxSkillVersionsPerSkill {
				return learning.ErrSkillLimit
			}
			if err := s.ensureVersion(bundle, version); err != nil {
				return err
			}
			rev, revErr := revision()
			if revErr != nil {
				return revErr
			}
			now := s.now().UTC()
			value := learning.SkillVersion{ID: id, Version: version, Revision: rev, State: learning.SkillDraft, OwnerAgent: owner, Partition: p, Bundle: bundle, Provenance: provenance, Disposition: provenance.ValidationDisposition, Supersedes: record.Versions[len(record.Versions)-1].Version, CreatedAt: now, UpdatedAt: now}
			record.Versions = append(record.Versions, clone(value))
			bucket[id] = record
			doc.Partitions[key] = bucket
			out = clone(value)
			return nil
		}
		if len(bucket) >= learning.MaxSkillsPerPartition {
			return learning.ErrSkillLimit
		}
		if err := s.ensureVersion(bundle, version); err != nil {
			return err
		}
		rev, revErr := revision()
		if revErr != nil {
			return revErr
		}
		now := s.now().UTC()
		value := learning.SkillVersion{ID: id, Version: version, Revision: rev, State: learning.SkillDraft, OwnerAgent: owner, Partition: p, Bundle: bundle, Provenance: provenance, Disposition: provenance.ValidationDisposition, CreatedAt: now, UpdatedAt: now}
		bucket[id] = skillRecord{Owner: owner, Name: bundle.Name, Versions: []learning.SkillVersion{clone(value)}}
		doc.Partitions[key] = bucket
		out = clone(value)
		return nil
	})
	return
}

// Get returns one exact version, or the latest version when version is empty.
func (s *Store) Get(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID) (out learning.SkillVersion, found bool, err error) {
	if err = learning.ValidateSkillPartition(p, owner); err != nil {
		return
	}
	err = s.locked(ctx, false, func(doc *manifest) error {
		record, index, locateErr := locate(doc.Partitions[partitionKey(p)], owner, id, version)
		if errors.Is(locateErr, learning.ErrSkillNotFound) {
			return nil
		}
		if locateErr != nil {
			return locateErr
		}
		out, found = clone(record.Versions[index]), true
		return nil
	})
	return
}

// List returns latest versions in deterministic skill-id order.
func (s *Store) List(ctx context.Context, p learning.SkillPartition, options learning.SkillList) (page learning.SkillPage, err error) {
	if options.Limit <= 0 {
		options.Limit = learning.DefaultSkillPageSize
	}
	if options.Limit > learning.MaxSkillPageSize {
		return page, learning.ErrSkillLimit
	}
	if options.State != "" && !options.State.Valid() {
		return page, learning.ErrInvalidSkill
	}
	err = s.locked(ctx, false, func(doc *manifest) error {
		bucket := doc.Partitions[partitionKey(p)]
		ids := make([]learning.SkillID, 0, len(bucket))
		selected := make(map[learning.SkillID]learning.SkillVersion, len(bucket))
		for id, record := range bucket {
			value := record.Versions[len(record.Versions)-1]
			if options.State != "" {
				found := false
				for i := len(record.Versions) - 1; i >= 0; i-- {
					if record.Versions[i].State == options.State {
						value, found = record.Versions[i], true
						break
					}
				}
				if !found {
					continue
				}
			}
			if id > options.After && (options.Name == "" || record.Name == options.Name) && (options.OwnerAgent == "" || record.Owner == options.OwnerAgent) {
				ids = append(ids, id)
				selected[id] = value
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for i, id := range ids {
			if i == options.Limit {
				page.Next = page.Versions[len(page.Versions)-1].ID
				break
			}
			page.Versions = append(page.Versions, clone(selected[id]))
		}
		return nil
	})
	return
}

func appendReceipt(value *learning.SkillVersion, operation string, from, to learning.SkillState, now time.Time) {
	value.Receipts = append(value.Receipts, learning.SkillReceipt{Operation: operation, From: from, To: to, Version: value.Version, At: now})
	if len(value.Receipts) > learning.MaxSkillReceipts {
		value.Receipts = append([]learning.SkillReceipt(nil), value.Receipts[len(value.Receipts)-learning.MaxSkillReceipts:]...)
	}
}

func rebuildReceiptIndex(partitions map[string]map[learning.SkillID]skillRecord) map[string][]learning.SkillReceiptRecord {
	out := make(map[string][]learning.SkillReceiptRecord, len(partitions))
	for key, bucket := range partitions {
		var history []learning.SkillReceiptRecord
		for id, record := range bucket {
			for versionIndex, version := range record.Versions {
				for receiptIndex, receipt := range version.Receipts {
					raw := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d\x00%d", receipt.At.UnixNano(), id, receipt.Version, receipt.Operation, versionIndex, receiptIndex)
					sum := sha256.Sum256([]byte(raw))
					history = append(history, learning.SkillReceiptRecord{ID: hex.EncodeToString(sum[:16]), SkillID: id, Name: record.Name, OwnerAgent: record.Owner, Version: version.Version, Receipt: receipt})
				}
			}
		}
		sortReceiptHistory(history)
		if len(history) > learning.MaxSkillReceiptHistory {
			history = append([]learning.SkillReceiptRecord(nil), history[len(history)-learning.MaxSkillReceiptHistory:]...)
		}
		out[key] = history
	}
	return out
}

func sortReceiptHistory(history []learning.SkillReceiptRecord) {
	sort.SliceStable(history, func(i, j int) bool {
		a, b := history[i], history[j]
		if !a.Receipt.At.Equal(b.Receipt.At) {
			return a.Receipt.At.Before(b.Receipt.At)
		}
		if a.SkillID != b.SkillID {
			return a.SkillID < b.SkillID
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.ID < b.ID
	})
}

func appendReceiptHistory(doc *manifest, key string, id learning.SkillID, record skillRecord, before []learning.SkillReceipt, had []bool) {
	history := doc.Receipts[key]
	for i := range record.Versions {
		if len(record.Versions[i].Receipts) == 0 {
			continue
		}
		receipt := record.Versions[i].Receipts[len(record.Versions[i].Receipts)-1]
		if i < len(had) && had[i] && receipt == before[i] {
			continue
		}
		raw := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%d", receipt.At.UnixNano(), id, receipt.Version, receipt.Operation, len(history))
		sum := sha256.Sum256([]byte(raw))
		history = append(history, learning.SkillReceiptRecord{ID: hex.EncodeToString(sum[:16]), SkillID: id, Name: record.Name, OwnerAgent: record.Owner, Version: record.Versions[i].Version, Receipt: receipt})
	}
	sortReceiptHistory(history)
	if len(history) > learning.MaxSkillReceiptHistory {
		history = append([]learning.SkillReceiptRecord(nil), history[len(history)-learning.MaxSkillReceiptHistory:]...)
	}
	doc.Receipts[key] = history
}

// ListSkillReceipts pages the bounded durable receipt index without scanning skill versions.
func (s *Store) ListSkillReceipts(ctx context.Context, p learning.SkillPartition, options learning.SkillReceiptList) (page learning.SkillReceiptPage, err error) {
	if options.Limit <= 0 {
		options.Limit = learning.DefaultSkillPageSize
	}
	if options.Limit > learning.MaxSkillPageSize {
		return page, learning.ErrSkillLimit
	}
	err = s.locked(ctx, false, func(doc *manifest) error {
		history := doc.Receipts[partitionKey(p)]
		start := 0
		if options.After != "" {
			start = -1
			for i := range history {
				if history[i].ID == options.After {
					start = i + 1
					break
				}
			}
			if start < 0 {
				return learning.ErrSkillCursor
			}
		}
		end := min(start+options.Limit, len(history))
		page.Records = append([]learning.SkillReceiptRecord(nil), history[start:end]...)
		if end < len(history) && end > start {
			page.Next = history[end-1].ID
		}
		return nil
	})
	return page, err
}

func (s *Store) update(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, fn func(*skillRecord, int, time.Time) error) (out learning.SkillVersion, err error) {
	if err = learning.ValidateSkillPartition(p, owner); err != nil {
		return
	}
	err = s.locked(ctx, true, func(doc *manifest) error {
		key := partitionKey(p)
		bucket := doc.Partitions[key]
		record, index, locateErr := locate(bucket, owner, id, version)
		if locateErr != nil {
			return locateErr
		}
		receiptBefore := make([]learning.SkillReceipt, len(record.Versions))
		receiptHad := make([]bool, len(record.Versions))
		for i := range record.Versions {
			if n := len(record.Versions[i].Receipts); n > 0 {
				receiptBefore[i] = record.Versions[i].Receipts[n-1]
				receiptHad[i] = true
			}
		}
		if record.Versions[index].Revision != expected {
			return learning.ErrSkillConflict
		}
		now := s.now().UTC()
		if err := fn(&record, index, now); err != nil {
			return err
		}
		rev, revErr := revision()
		if revErr != nil {
			return revErr
		}
		record.Versions[index].Revision = rev
		record.Versions[index].UpdatedAt = now
		appendReceiptHistory(doc, key, id, record, receiptBefore, receiptHad)
		bucket[id] = record
		doc.Partitions[key] = bucket
		out = clone(record.Versions[index])
		return nil
	})
	return
}

// RecordEvaluation appends a bounded evaluation under CAS.
func (s *Store) RecordEvaluation(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, evaluation learning.SkillEvaluation) (learning.SkillVersion, error) {
	if err := learning.ValidateSkillEvaluation(evaluation); err != nil {
		return learning.SkillVersion{}, err
	}
	return s.update(ctx, p, owner, id, version, expected, func(record *skillRecord, index int, now time.Time) error {
		value := &record.Versions[index]
		if value.State != learning.SkillDraft && value.State != learning.SkillEvaluated {
			return learning.ErrSkillTransition
		}
		if len(value.Evaluations) >= learning.MaxSkillEvaluations {
			return learning.ErrSkillLimit
		}
		if evaluation.At.IsZero() {
			evaluation.At = now
		}
		from := value.State
		value.Evaluations = append(value.Evaluations, evaluation)
		if evaluation.Verdict == learning.EvaluationFail || evaluation.Verdict == learning.EvaluationError {
			value.State = learning.SkillRejected
		} else {
			value.State = learning.SkillEvaluated
		}
		appendReceipt(value, "record_evaluation", from, value.State, now)
		return nil
	})
}

// Stage moves a non-failing evaluated version to staged under CAS.
func (s *Store) Stage(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(record *skillRecord, index int, now time.Time) error {
		value := &record.Versions[index]
		if value.State != learning.SkillEvaluated || len(value.Evaluations) == 0 || value.Evaluations[len(value.Evaluations)-1].Verdict == learning.EvaluationFail {
			return learning.ErrSkillTransition
		}
		value.State = learning.SkillStaged
		appendReceipt(value, "stage", learning.SkillEvaluated, value.State, now)
		return nil
	})
}

// Activate activates a staged PASS version and archives the prior active version atomically.
func (s *Store) Activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.activate(ctx, p, owner, id, version, expected, false)
}

// ActivateValidated atomically activates only an evidence-backed, accepted/exact,
// staged ABSTAIN version.
func (s *Store) ActivateValidated(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.activate(ctx, p, owner, id, version, expected, true)
}

func (s *Store) activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision, validated bool) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(record *skillRecord, index int, now time.Time) error {
		value := &record.Versions[index]
		if value.State != learning.SkillStaged || len(value.Evaluations) == 0 {
			return learning.ErrSkillTransition
		}
		verdict := value.Evaluations[len(value.Evaluations)-1].Verdict
		if (!validated && verdict != learning.EvaluationPass) || (validated && (verdict != learning.EvaluationAbstain || len(value.Provenance.EvidenceRefs) == 0 || (value.Disposition != learning.ValidationAccept && value.Disposition != learning.ValidationExactDuplicate) || value.Provenance.Origin == learning.SkillProvenanceLegacyModel)) {
			return learning.ErrSkillTransition
		}
		for i := range record.Versions {
			if i != index && record.Versions[i].State == learning.SkillActive {
				from := record.Versions[i].State
				rev, err := revision()
				if err != nil {
					return err
				}
				record.Versions[i].State = learning.SkillArchived
				record.Versions[i].Revision = rev
				record.Versions[i].UpdatedAt = now
				appendReceipt(&record.Versions[i], "superseded", from, learning.SkillArchived, now)
			}
		}
		value.State = learning.SkillActive
		operation := "activate"
		if validated {
			operation = "activate_validated"
		}
		appendReceipt(value, operation, learning.SkillStaged, value.State, now)
		return nil
	})
}

// Reject marks a non-active version rejected under CAS.
func (s *Store) Reject(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(record *skillRecord, index int, now time.Time) error {
		value := &record.Versions[index]
		if value.State == learning.SkillActive || value.State == learning.SkillArchived || value.State == learning.SkillRejected {
			return learning.ErrSkillTransition
		}
		from := value.State
		value.State = learning.SkillRejected
		appendReceipt(value, "reject", from, value.State, now)
		return nil
	})
}

// Archive archives an active version under CAS.
func (s *Store) Archive(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, expected learning.Revision) (learning.SkillVersion, error) {
	return s.update(ctx, p, owner, id, version, expected, func(record *skillRecord, index int, now time.Time) error {
		value := &record.Versions[index]
		if value.State != learning.SkillActive {
			return learning.ErrSkillTransition
		}
		from := value.State
		value.State = learning.SkillArchived
		appendReceipt(value, "archive", from, value.State, now)
		return nil
	})
}

// Rollback atomically archives the current active version and reactivates an archived target.
func (s *Store) Rollback(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, expected learning.Revision, target learning.VersionID) (out learning.SkillVersion, err error) {
	if err = learning.ValidateSkillPartition(p, owner); err != nil {
		return
	}
	err = s.locked(ctx, true, func(doc *manifest) error {
		key := partitionKey(p)
		bucket := doc.Partitions[key]
		record, ok := bucket[id]
		if !ok {
			return learning.ErrSkillNotFound
		}
		if record.Owner != owner {
			return learning.ErrSkillOwnerMismatch
		}
		active, targetIndex := -1, -1
		for i := range record.Versions {
			if record.Versions[i].State == learning.SkillActive {
				active = i
			}
			if record.Versions[i].Version == target {
				targetIndex = i
			}
		}
		if active < 0 || record.Versions[active].Revision != expected {
			return learning.ErrSkillConflict
		}
		if targetIndex < 0 {
			return learning.ErrSkillNotFound
		}
		if !rollbackEligible(record.Versions[targetIndex]) {
			return learning.ErrSkillTransition
		}
		now := s.now().UTC()
		receiptBefore := make([]learning.SkillReceipt, len(record.Versions))
		receiptHad := make([]bool, len(record.Versions))
		for i := range record.Versions {
			if n := len(record.Versions[i].Receipts); n > 0 {
				receiptBefore[i], receiptHad[i] = record.Versions[i].Receipts[n-1], true
			}
		}
		appendReceipt(&record.Versions[active], "rollback_from", learning.SkillActive, learning.SkillArchived, now)
		record.Versions[active].State = learning.SkillArchived
		record.Versions[active].UpdatedAt = now
		record.Versions[active].Revision, err = revision()
		if err != nil {
			return err
		}
		appendReceipt(&record.Versions[targetIndex], "rollback_to", learning.SkillArchived, learning.SkillActive, now)
		record.Versions[targetIndex].State = learning.SkillActive
		record.Versions[targetIndex].UpdatedAt = now
		record.Versions[targetIndex].Revision, err = revision()
		if err != nil {
			return err
		}
		appendReceiptHistory(doc, key, id, record, receiptBefore, receiptHad)
		bucket[id] = record
		doc.Partitions[key] = bucket
		out = clone(record.Versions[targetIndex])
		return nil
	})
	return
}

func rollbackEligible(v learning.SkillVersion) bool {
	if v.State != learning.SkillArchived {
		return false
	}
	for _, receipt := range v.Receipts {
		if receipt.To == learning.SkillActive && (receipt.Operation == "activate" || receipt.Operation == "activate_validated" || receipt.Operation == "rollback_to") {
			return true
		}
	}
	return false
}
