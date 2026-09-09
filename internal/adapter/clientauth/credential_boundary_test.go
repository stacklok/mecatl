//go:build linux || darwin

package clientauth

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBackendMetadataFilesystemMatrix(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "hardlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, backendMarker)
			original, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			saved := filepath.Join(root, "saved-marker")
			if err := os.Rename(marker, saved); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "mode":
				err = os.WriteFile(marker, original, 0644)
			case "symlink":
				err = os.Symlink(saved, marker)
			case "hardlink":
				err = os.Link(saved, marker)
			case "fifo":
				err = syscall.Mkfifo(marker, 0600)
			case "directory":
				err = os.Mkdir(marker, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreAuto); err == nil {
				t.Fatal("unsafe marker selected backend")
			}
			if s, _, err := OpenExistingCredentialStore(t.Context(), root); err == nil {
				_ = s.Close()
				t.Fatal("unsafe marker opened store")
			}
			after, err := os.ReadFile(saved)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("unsafe evidence rewritten")
			}
			if _, err := os.Stat(filepath.Join(root, "clientauth-plaintext")); !os.IsNotExist(err) {
				t.Fatal("unsafe marker created credential state")
			}
		})
	}
}

type backendOwnerInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i backendOwnerInfo) Sys() any { return &i.stat }

func TestBackendMetadataRejectsForeignOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if _, err := ResolveCredentialStore(t.Context(), root, CredentialStoreFile); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, backendMarker)} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !privateBackendInfo(info, info.IsDir()) {
			t.Fatal("fixture not admitted")
		}
		stat := *info.Sys().(*syscall.Stat_t)
		stat.Uid++
		if privateBackendInfo(backendOwnerInfo{info, stat}, info.IsDir()) {
			t.Fatal("foreign owner admitted")
		}
	}
}
