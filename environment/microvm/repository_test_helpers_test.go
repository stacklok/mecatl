package microvm

import (
	"context"
	"path/filepath"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	return root
}

func testArtifactSnapshot(verified VerifiedArtifacts) RepositoryArtifactSnapshot {
	return func(context.Context) (VerifiedArtifacts, func(), error) {
		return verified, func() {}, nil
	}
}
