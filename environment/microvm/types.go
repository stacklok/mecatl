package microvm

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

const (
	// Kind is the durable microVM environment kind.
	Kind = "microvm"
	// GuestPrebootConfigPath is the guest's immutable repository boot config path.
	GuestPrebootConfigPath = "/etc/mecatl/guest-agent.json"
)

// EnvironmentState is the durable repository generation state.
type EnvironmentState string

const (
	// EnvironmentProvisioning means first-generation admission has begun.
	EnvironmentProvisioning EnvironmentState = "provisioning"
	// EnvironmentReady means the exact repository generation is healthy.
	EnvironmentReady EnvironmentState = "ready"
	// EnvironmentDestroyed is the inventory projection for a removed attachment.
	EnvironmentDestroyed EnvironmentState = "destroyed"
)

var (
	// ErrInvalidEnvironmentRef reports a malformed logical generation ref.
	ErrInvalidEnvironmentRef = errors.New("invalid microvm environment ref")
	// ErrEnvironmentUnavailable reports a repository generation that is not live.
	ErrEnvironmentUnavailable = errors.New("microvm environment generation is not live")
	// ErrInvalidFork reports a mismatched repository child.
	ErrInvalidFork = errors.New("invalid microvm child environment")
	// ErrMergeConflict reports parent/child overlap.
	ErrMergeConflict = errors.New("microvm child environment merge conflict")
)

// EnvironmentRef is the daemon-internal logical generation identity.
type EnvironmentRef struct{ Kind, ID string }

// ArtifactVerifier verifies a complete daemon-owned artifact request set.
type ArtifactVerifier interface {
	Verify(context.Context, map[ArtifactKind]ArtifactRequest) (VerifiedArtifacts, string, error)
}

// EnforcedProfileStatus is the daemon-authoritative placement policy projection.
type EnforcedProfileStatus struct{ Profile, GuestEgress, HostEgress string }

// RuntimeStatus identifies the exact live repository VM process.
type RuntimeStatus struct {
	Live            bool
	Generation      uint32
	VMID            string
	PID             int
	ProcessIdentity string
	Endpoint        string
}

// GuestPrebootConfig is the immutable config consumed by the guest agent.
type GuestPrebootConfig struct {
	DisableIPv6     bool                 `json:"disable_ipv6"`
	AgentEndpoint   string               `json:"agent_endpoint"`
	Binding         control.Binding      `json:"binding"`
	Capabilities    control.Capabilities `json:"capabilities"`
	MaxMessageBytes uint32               `json:"max_message_bytes"`
}

func parseEnvironmentRef(ref EnvironmentRef) (string, uint32, error) {
	if ref.Kind != Kind || strings.TrimSpace(ref.ID) != ref.ID {
		return "", 0, ErrInvalidEnvironmentRef
	}
	separator := strings.LastIndexByte(ref.ID, '@')
	if separator <= 0 || separator == len(ref.ID)-1 {
		return "", 0, ErrInvalidEnvironmentRef
	}
	environmentID := ref.ID[:separator]
	generation, err := strconv.ParseUint(ref.ID[separator+1:], 10, 32)
	if strings.TrimSpace(environmentID) != environmentID || err != nil || generation == 0 {
		return "", 0, ErrInvalidEnvironmentRef
	}
	return environmentID, uint32(generation), nil
}
