package mcpcredential

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAC08CanonicalCustodyMarkerInspectionNeverPresentsCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	if got := InspectMarker(root); got != MarkerMissing {
		t.Fatalf("missing root inspection = %q, want %q", got, MarkerMissing)
	}
	keyPath := filepath.Join(t.TempDir(), "key")
	selection, err := Resolve(context.Background(), root, Options{Requested: BackendFile, FilePath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	clear(selection.Key)
	if got := InspectMarker(root); got != MarkerPresent {
		t.Fatalf("valid marker inspection = %q, want %q", got, MarkerPresent)
	}
	if err := os.WriteFile(filepath.Join(root, markerName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := InspectMarker(root); got != MarkerRecovery {
		t.Fatalf("invalid marker inspection = %q, want %q", got, MarkerRecovery)
	}
}
