package osfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Capture the actual fresh root, rather than relying on GC or descriptor counts.
// Each public operation must close it on return without closing the shared root.
func TestRelaxedReadOperationsCloseFreshRoots(t *testing.T) {
	for _, operation := range []string{"Read", "Stat", "ReadDir"} {
		for _, outcome := range []string{"success", "missing", "wrong-type", "oversized"} {
			if (outcome == "wrong-type" && operation == "Stat") || (outcome == "oversized" && operation != "Read") {
				continue
			}
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				base := t.TempDir()
				ws, err := NewWorkspace(filepath.Join(base, "workspace"), WithRelaxedReads())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ws.fs.r.Close() })
				outside := filepath.Join(base, "outside")
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(outside, "file.txt")
				if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws.Root(), "shared.txt"), []byte("shared"), 0o600); err != nil {
					t.Fatal(err)
				}
				target := file
				if operation == "ReadDir" {
					target = outside
				}
				switch outcome {
				case "missing":
					target = filepath.Join(outside, "missing")
				case "wrong-type":
					if operation == "Read" {
						target = outside
					} else {
						target = file
					}
				case "oversized":
					if err := os.Truncate(file, maxReadBytes+1); err != nil {
						t.Fatal(err)
					}
				}

				var opened []*os.Root
				ws.fs.openReadRoot = func(path string) (*os.Root, error) {
					r, err := os.OpenRoot(path)
					if err == nil {
						opened = append(opened, r)
						t.Cleanup(func() { _ = r.Close() })
					}
					return r, err
				}
				switch operation {
				case "Read":
					_, err = ws.Read(context.Background(), target)
				case "Stat":
					_, err = ws.Stat(context.Background(), target)
				case "ReadDir":
					_, err = ws.ReadDir(context.Background(), target)
				}
				if (err == nil) != (outcome == "success") {
					t.Fatalf("%s(%s) error = %v", operation, outcome, err)
				}
				if len(opened) != 1 || opened[0] == ws.fs.r {
					t.Fatalf("fresh roots = %v, want exactly one distinct root", opened)
				}
				if _, err := opened[0].Stat("."); !errors.Is(err, os.ErrClosed) {
					t.Errorf("fresh root after %s(%s): %v, want os.ErrClosed", operation, outcome, err)
				}

				// Every in-root consumer must keep the shared root alive too.
				if data, err := ws.Read(context.Background(), "shared.txt"); err != nil || string(data) != "shared" {
					t.Fatalf("shared Read = %q, %v", data, err)
				}
				if _, err := ws.Stat(context.Background(), "shared.txt"); err != nil {
					t.Fatalf("shared Stat: %v", err)
				}
				if entries, err := ws.ReadDir(context.Background(), "."); err != nil || len(entries) != 1 || entries[0].Name != "shared.txt" {
					t.Fatalf("shared ReadDir = %v, %v", entries, err)
				}
				if _, err := ws.fs.r.Stat("."); err != nil {
					t.Fatalf("shared root was closed: %v", err)
				}
				if len(opened) != 1 {
					t.Fatalf("in-root operations opened %d extra roots", len(opened)-1)
				}
			})
		}
	}
}
