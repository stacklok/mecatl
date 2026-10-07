package mcpbrokerserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// SessionAPIConfig explicitly selects the scratch PoC's ownerless deployment
// partition. It is not a human identity assertion and has no implicit fallback.
type SessionAPIConfig struct {
	Mode       string `json:"mode"`
	Deployment string `json:"deployment"`
}

func (cfg *SessionAPIConfig) Validate(profiles []mcpbroker.ToolHiveProfile, protectedStorage bool) error {
	if cfg == nil {
		return errors.New("session API requires explicit OWNERLESS mode and deployment identifier")
	}
	if cfg.Mode != "OWNERLESS" || len(cfg.Deployment) == 0 || len(cfg.Deployment) > 128 || strings.TrimSpace(cfg.Deployment) != cfg.Deployment {
		return errors.New("session API requires explicit OWNERLESS mode and deployment identifier")
	}
	for _, ch := range cfg.Deployment {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return errors.New("session API deployment identifier is invalid")
		}
	}
	if !protectedStorage {
		return errors.New("session API requires durable Redis")
	}
	for _, profile := range profiles {
		if profile.Auth != "oauth" && profile.Auth != "none" {
			return errors.New("session API profile authentication is invalid")
		}
	}
	return nil
}

type verifiedSessionWorkloadKey struct{}

func verifiedSessionWorkload(ctx context.Context) *session.Principal {
	p, _ := ctx.Value(verifiedSessionWorkloadKey{}).(*session.Principal)
	return p
}

func ownerlessPartition(deployment string) func(context.Context) ([32]byte, error) {
	return func(ctx context.Context) ([32]byte, error) {
		workload, err := contract.ContinuityPrincipalPartition(contract.ContinuityPartitionWorkload, verifiedSessionWorkload(ctx))
		if err != nil {
			return [32]byte{}, err
		}
		// JSON array framing and a distinct domain prevent claim/role collisions.
		framed, err := json.Marshal([]any{"mecatl:poc:ownerless-deployment:v1", deployment, workload})
		if err != nil {
			return [32]byte{}, err
		}
		return sha256.Sum256(framed), nil
	}
}
