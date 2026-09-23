package microvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

const (
	guestAgentArtifactName = "mecatl-guest-agent"
	guestAgentInstallPath  = "usr/local/bin/mecatl-guest-agent"
)

var errRepositoryRootFSMaterialized = errors.New("repository rootfs is already materialized")

type repositoryRootFSMaterializer struct {
	mu               sync.Mutex
	materialized     bool
	prepareOwnership repositoryOwnershipPreparer
}

func newRepositoryRootFSMaterializer() *repositoryRootFSMaterializer {
	return &repositoryRootFSMaterializer{prepareOwnership: prepareRepositoryOwnership}
}

// Materialize clones the admitted Brood tree once, injects the independently
// admitted guest agent, and establishes the static guest runtime contract.
// The repository-VM generation owner must retain one materializer for the
// generation lifetime; per-boot configuration belongs to the later launch step.
func (m *repositoryRootFSMaterializer) Materialize(broodRoot, destination, guestArtifact string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.materialized {
		return errRepositoryRootFSMaterialized
	}
	if err := cloneRootFS(broodRoot, destination); err != nil {
		return err
	}
	defer func() {
		if !m.materialized {
			_ = os.RemoveAll(destination)
		}
	}()
	if err := injectGuestAgent(destination, guestArtifact); err != nil {
		return err
	}
	contract := guestexec.DefaultRuntimeContract()
	if err := establishGuestRuntimeContract(destination, contract); err != nil {
		return err
	}
	for _, guestPath := range []string{contract.Home, contract.Workdir} {
		relative := filepath.FromSlash(strings.TrimPrefix(guestPath, "/"))
		if err := m.prepareOwnership(context.Background(), destination, relative); err != nil {
			return fmt.Errorf("prepare guest ownership for runtime directory %s: %w", guestPath, err)
		}
	}
	m.materialized = true
	return nil
}

func injectGuestAgent(rootfs, artifactRoot string) (retErr error) {
	artifact, err := os.OpenRoot(artifactRoot)
	if err != nil {
		return fmt.Errorf("open verified guest-agent artifact: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, artifact.Close()) }()
	source, err := artifact.Open(guestAgentArtifactName)
	if err != nil {
		return fmt.Errorf("open verified guest agent: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, source.Close()) }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("verified guest agent is not an executable regular file")
	}

	destination, err := os.OpenRoot(rootfs)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, destination.Close()) }()
	parent := filepath.Dir(guestAgentInstallPath)
	if err := destination.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create guest-agent install directory: %w", err)
	}
	output, err := destination.OpenFile(guestAgentInstallPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("create injected guest agent: %w", err)
	}
	_, copyErr := io.Copy(output, source)
	return errors.Join(copyErr, output.Close(), destination.Chmod(guestAgentInstallPath, 0o755))
}

func establishGuestRuntimeContract(rootfs string, contract guestexec.RuntimeContract) (retErr error) {
	identity := guestexec.DefaultWorkloadIdentity()
	if contract.Identity != identity || identity.UID != 65532 || identity.GID != 65532 ||
		contract.Home == "" || contract.Workdir != "/workspace" || contract.Path == "" {
		return errors.New("guest runtime contract is incomplete")
	}
	root, err := os.OpenRoot(rootfs)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	paths := append([]string{contract.Home, contract.Workdir}, contract.CacheDirectories()...)
	for _, guestPath := range paths {
		relative := filepath.FromSlash(strings.TrimPrefix(guestPath, "/"))
		if relative == "." || relative == "" || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("guest runtime directory is not confined")
		}
		if err := root.MkdirAll(relative, 0o700); err != nil {
			return fmt.Errorf("create guest runtime directory %s: %w", guestPath, err)
		}
		if err := root.Chmod(relative, 0o700); err != nil {
			return fmt.Errorf("make guest runtime directory writable %s: %w", guestPath, err)
		}
	}
	return nil
}
