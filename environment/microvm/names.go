package microvm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
)

// ResourceNames is one collision-free, confined set of session resource names.
type ResourceNames struct {
	Identity     EnvironmentIdentity
	WorktreePath string
	MetadataPath string
	Branch       string
}

// OpaqueIdentityAllocator prevents caller-controlled session and repository names
// from becoming filesystem, socket, VM, or Git-ref names.
type OpaqueIdentityAllocator struct {
	mu          sync.Mutex
	stateRoot   string
	runtimeRoot string
	random      io.Reader
	serial      uint32
}

// NewOpaqueIdentityAllocator constructs an allocator rooted in private daemon dirs.
func NewOpaqueIdentityAllocator(stateRoot, runtimeRoot string, random io.Reader) (*OpaqueIdentityAllocator, error) {
	if !absoluteClean(stateRoot) || !absoluteClean(runtimeRoot) {
		return nil, errors.New("microvm resource roots must be absolute and clean")
	}
	if random == nil {
		random = rand.Reader
	}
	return &OpaqueIdentityAllocator{stateRoot: stateRoot, runtimeRoot: runtimeRoot, random: random}, nil
}

// Allocate implements IdentityAllocator without exposing caller-controlled names.
func (a *OpaqueIdentityAllocator) Allocate(sessionID string) (EnvironmentIdentity, error) {
	names, err := a.AllocateResources(sessionID, "")
	return names.Identity, err
}

// AllocateResources returns an independently-named VM, endpoint, worktree,
// metadata directory, branch, environment ref generation, and cleanup key.
func (a *OpaqueIdentityAllocator) AllocateResources(sessionID, repositoryName string) (ResourceNames, error) {
	if a == nil || a.random == nil || sessionID == "" {
		return ResourceNames{}, errors.New("microvm opaque identity allocation is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serial == ^uint32(0) {
		return ResourceNames{}, errors.New("microvm environment generation space exhausted")
	}
	a.serial++
	generation := a.serial
	entropy := make([]byte, 32)
	if _, err := io.ReadFull(a.random, entropy); err != nil {
		return ResourceNames{}, fmt.Errorf("read microvm identity entropy: %w", err)
	}
	hash := sha256.New()
	_, _ = hash.Write(entropy)
	var serial [4]byte
	binary.BigEndian.PutUint32(serial[:], generation)
	_, _ = hash.Write(serial[:])
	_, _ = hash.Write([]byte(sessionID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(repositoryName))
	token := hex.EncodeToString(hash.Sum(nil)[:16])

	identity := EnvironmentIdentity{
		EnvironmentID: "env-" + token,
		VMID:          "vm-" + token,
		Endpoint:      filepath.Join(a.runtimeRoot, "endpoint-"+token+".sock"),
		Generation:    generation,
	}
	return ResourceNames{
		Identity: identity, WorktreePath: filepath.Join(a.stateRoot, "worktrees", "wt-"+token),
		MetadataPath: filepath.Join(a.stateRoot, "metadata", "meta-"+token), Branch: "mecatl/branch-" + token,
	}, nil
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}
