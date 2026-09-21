package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestBuildUserModelStoreDisabledReturnsNilInterface guards the typed-nil
// discipline on catalogAssets (see the field doc): buildUserModelStore returns
// the tool.MemoryStore INTERFACE, so its disabled paths must yield an untyped
// nil — a typed-nil *memory.Store smuggled into the interface would make every
// downstream `!= nil` check (registerMemoryFamilies, buildInstructionAssembler,
// maybeWrapUserModelReview, userModelLister) wrongly treat the disabled store
// as wired. Two of the three nil paths are covered (the xdg no-dir path needs
// env surgery and is the same `return nil` shape).
func TestBuildUserModelStoreDisabledReturnsNilInterface(t *testing.T) {
	t.Run("disabled via --no-user-model", func(t *testing.T) {
		cfg := Config{NoUserModel: true}
		if got := buildUserModelStore(cfg); got != nil {
			t.Fatalf("buildUserModelStore(NoUserModel) = %#v, want a nil interface value", got)
		}
	})

	t.Run("unopenable store dir", func(t *testing.T) {
		// UserModelDir pointing at an existing regular FILE makes memory.New's
		// MkdirAll fail, exercising the fail-soft could-not-open path.
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("occupied"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		cfg := Config{UserModelDir: file}
		if got := buildUserModelStore(cfg); got != nil {
			t.Fatalf("buildUserModelStore(UserModelDir=regular file) = %#v, want a nil interface value", got)
		}
	})
}

func TestBuildCatalogOwnershipEnforcedRejectsRemoteMemoryDriver(t *testing.T) {
	_, _, _, _, _, err := buildCatalog(context.Background(), Config{
		MemoryStoreURL:    "grpc://memory.example",
		OwnershipEnforced: true,
	}, nil, nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "local MemoryDir") {
		t.Fatalf("buildCatalog remote memory driver error = %v, want local-memory rejection", err)
	}
}

func TestBuildUserModelStoreOwnershipEnforcedPartitionsCallers(t *testing.T) {
	store := buildUserModelStore(Config{UserModelDir: t.TempDir(), OwnershipEnforced: true})
	if store == nil {
		t.Fatal("buildUserModelStore returned nil")
	}
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "alice"})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "bob"})
	rememberProfile(t, alice, store, tool.MemoryEntry{Key: "user/preference", Value: "alice"})
	if _, ok, err := store.Recall(bob, "user/preference"); err != nil || ok {
		t.Fatalf("Bob Recall = (%t, %v), want absent", ok, err)
	}
}
