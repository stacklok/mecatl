package mcpbroker

import (
	"errors"
	"testing"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestContinuityProfileConstructionFailsClosedOnlyWhenProtectedCustodyIsConfigured(t *testing.T) {
	config := ToolHiveConfig{Profiles: []ToolHiveProfile{{Name: "protected", Auth: authOAuth, OAuth: &ToolHiveOAuth{}}}}
	construction := toolHiveConstruction{providerByBackend: map[string]string{}}
	if _, _, err := continuityProfileForProcess(config, "https://broker.example", construction); err != nil {
		t.Fatalf("legacy profile construction = %v", err)
	}
	config.ProtectedStorage = &ProtectedStorageConfig{}
	if _, _, err := continuityProfileForProcess(config, "https://broker.example", construction); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("protected continuity profile construction = %v, want unavailable", err)
	}
}
