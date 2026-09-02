package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestADR_0281_DefaultManagedRootUsesSystemTemp(t *testing.T) {
	base := t.TempDir()
	got, err := resolveTemporaryStorage(Config{}, func(key string) string {
		if key == "TMPDIR" {
			return base
		}
		return ""
	}, "linux")
	if err != nil {
		t.Fatalf("resolveTemporaryStorage: %v", err)
	}
	if got.Mode != temporaryStorageManaged {
		t.Fatalf("default mode = %q, want managed", got.Mode)
	}
	if want := filepath.Join(base, "mecatl"); got.ManagedRoot != want {
		t.Fatalf("managed root = %q, want %q", got.ManagedRoot, want)
	}
	if got.SystemTempDir != base {
		t.Fatalf("system temp dir = %q, want inherited %q", got.SystemTempDir, base)
	}
}

func TestADR_0281_TemporaryStorageConfigValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveTemporaryStorage(Config{temporaryStorage: temporaryStorageConfig{
		ManagedRoot: root, SystemTempDir: "relative-system", CommandReapAfter: 30 * 24 * time.Hour,
		ReapInterval: 24 * time.Hour, ReapTimeout: time.Hour, ShutdownReapTimeout: 5 * time.Minute,
	}}, func(string) string { return t.TempDir() }, "linux")
	if err != nil {
		t.Fatalf("resolveTemporaryStorage: %v", err)
	}
	if got.ManagedRoot != root || got.CommandReapAfter != 30*24*time.Hour || got.ReapTimeout != time.Hour {
		t.Fatalf("resolved temporary storage = %+v", got)
	}

	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTemporaryStorage(Config{temporaryStorage: temporaryStorageConfig{ManagedRoot: root}}, func(string) string { return t.TempDir() }, "linux"); err == nil {
		t.Fatal("permissive absolute managed root accepted")
	}
}

func TestADR_0281_ManagedModeLinuxAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     temporaryStorageMode
		platform string
		wantErr  bool
	}{
		{name: "managed linux", mode: temporaryStorageManaged, platform: "linux"},
		{name: "managed non-linux", mode: temporaryStorageManaged, platform: "darwin", wantErr: true},
		{name: "system non-linux", mode: temporaryStorageSystem, platform: "darwin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveTemporaryStorage(Config{temporaryStorage: temporaryStorageConfig{Mode: tc.mode, CommandReapAfter: time.Hour, ReapInterval: time.Hour, ReapTimeout: 5 * time.Minute, ShutdownReapTimeout: time.Minute}}, func(string) string { return t.TempDir() }, tc.platform)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveTemporaryStorage() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
