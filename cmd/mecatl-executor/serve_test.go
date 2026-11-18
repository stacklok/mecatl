package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
)

func TestFileServiceOverOSFSConformance(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		root := t.TempDir()
		srv, err := newFileServer(root, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return fileconformance.Target{
			Conn: fileconformance.Dial(t, srv),
			Root: root,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(root, p))
				},
				Write: func(p string, data []byte) error {
					return os.WriteFile(filepath.Join(root, p), data, 0o600)
				},
			},
		}
	})
}

func TestFileServiceOverOSFSBoundsFileSize(t *testing.T) {
	root := t.TempDir()
	srv, err := newFileServer(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	ws := fileconformance.NewWorkspace(fileconformance.Dial(t, srv), root)
	big := bytes.Repeat([]byte("x"), 5)
	if err := os.WriteFile(filepath.Join(root, "big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(context.Background(), "big"); err == nil {
		t.Fatal("read of an oversized file succeeded")
	}
	if _, err := ws.CreateFile(context.Background(), "new", []byte("12345")); err == nil {
		t.Fatal("oversized create succeeded")
	}
}

func TestServeRejectsInvalidMaxFileBytes(t *testing.T) {
	for _, value := range []string{"0", "5242881"} {
		args := []string{serveMode, "-listen", "127.0.0.1:0", "-max-file-bytes", value}
		err := runServeMode(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "max-file-bytes") {
			t.Fatalf("-max-file-bytes %s: err = %v", value, err)
		}
	}
}
