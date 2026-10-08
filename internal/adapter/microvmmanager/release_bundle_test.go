package microvmmanager

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

func TestExtractReleaseBundleRootEntries(t *testing.T) {
	for _, name := range []string{".", "./"} {
		t.Run(name, func(t *testing.T) {
			root := privateTempDir(t)
			archive := filepath.Join(root, "bundle.tar.gz")
			writeReleaseHeaderFixture(t, archive, tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o777})
			destination := filepath.Join(root, "unpacked")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1234567890, 0)
			if err := os.Chtimes(destination, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := extractReleaseBundle(root, archive, destination); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o700 || !info.ModTime().Equal(stamp) {
				t.Fatalf("root metadata changed: %v", info)
			}
			absent := filepath.Join(root, "absent")
			if err := extractReleaseBundle(root, archive, absent); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(absent); !os.IsNotExist(err) {
				t.Fatalf("root entry created destination: %v", err)
			}
		})
	}
}

func TestExtractReleaseBundleRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name   string
		header tar.Header
		want   string
	}{
		{"dot regular", tar.Header{Name: ".", Typeflag: tar.TypeReg}, "unsafe release bundle path"},
		{"slash regular", tar.Header{Name: "./", Typeflag: tar.TypeReg}, "unsafe release bundle path"},
		{"dot symlink", tar.Header{Name: ".", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, "unsafe release bundle path"},
		{"slash symlink", tar.Header{Name: "./", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, "unsafe release bundle path"},
		{"dot hardlink", tar.Header{Name: ".", Typeflag: tar.TypeLink, Linkname: "../outside"}, "unsafe release bundle path"},
		{"slash hardlink", tar.Header{Name: "./", Typeflag: tar.TypeLink, Linkname: "../outside"}, "unsafe release bundle path"},
		{"dot data", tar.Header{Name: ".", Typeflag: tar.TypeDir, Size: 1}, "unsafe release bundle path"},
		{"slash data", tar.Header{Name: "./", Typeflag: tar.TypeDir, Size: 1}, "unsafe release bundle path"},
		{"parent cancellation", tar.Header{Name: "a/..", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"repeated dot", tar.Header{Name: "././", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"repeated slash", tar.Header{Name: ".//", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"empty", tar.Header{Name: "", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"absolute root", tar.Header{Name: "/", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"absolute file", tar.Header{Name: "/outside", Typeflag: tar.TypeReg}, "unsafe release bundle path"},
		{"parent", tar.Header{Name: "..", Typeflag: tar.TypeDir}, "unsafe release bundle path"},
		{"escape", tar.Header{Name: "../outside", Typeflag: tar.TypeReg}, "unsafe release bundle path"},
		{"nested escape", tar.Header{Name: "a/../../outside", Typeflag: tar.TypeReg}, "unsafe release bundle path"},
		{"child symlink", tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, "symlink or hard link is forbidden"},
		{"child hardlink", tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "../outside"}, "symlink or hard link is forbidden"},
		{"fifo", tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}, "unsupported release bundle entry type"},
		{"mode", tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0o4777}, "invalid release bundle mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTempDir(t)
			archive := filepath.Join(root, "bundle.tar.gz")
			writeReleaseHeaderFixture(t, archive, tc.header)
			destination := filepath.Join(root, "unpacked")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			err := extractReleaseBundle(root, archive, destination)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extract error = %v, want %q", err, tc.want)
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected entry changed destination: %v, %v", entries, err)
			}
		})
	}
}

func TestExtractReleaseBundleRootEntriesCountTowardLimit(t *testing.T) {
	root := privateTempDir(t)
	archive := filepath.Join(root, "bundle.tar.gz")
	headers := make([]tar.Header, 10001)
	for i := range headers {
		headers[i] = tar.Header{Name: "./", Typeflag: tar.TypeDir}
	}
	writeReleaseHeaderFixture(t, archive, headers...)
	if err := extractReleaseBundle(root, archive, filepath.Join(root, "absent")); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("extract error = %v, want entry limit rejection", err)
	}
}

func writeReleaseHeaderFixture(t *testing.T, path string, headers ...tar.Header) {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, header := range headers {
		if header.Typeflag == tar.TypeReg {
			header.Name = strings.TrimSuffix(header.Name, "/")
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	// tar.Writer rejects regular names ending in '/' and zeroes directory sizes.
	// Restore the requested fields and checksum to exercise malformed input too.
	if len(headers) == 1 {
		block := raw.Bytes()[:512]
		clear(block[:100])
		copy(block[:100], headers[0].Name)
		copy(block[124:136], fmt.Sprintf("%011o\x00", headers[0].Size))
		copy(block[148:156], "        ")
		sum := 0
		for _, b := range block {
			sum += int(b)
		}
		copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseWorkflowBundleExtraction(t *testing.T) {
	version, err := exec.Command("tar", "--version").Output()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Skip("release workflow runs with GNU tar on Ubuntu")
	}
	workflow, err := os.ReadFile("../../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(workflow, &config); err != nil {
		t.Fatal(err)
	}
	var packaging string
	for _, step := range config.Jobs["publish-microvm"].Steps {
		if step.Name == "Assemble versioned platform bootstrap bundle" {
			var ok bool
			packaging, _, ok = strings.Cut(step.Run, "sha=\"")
			if !ok {
				t.Fatal("cannot locate end of bundle assembly")
			}
		}
	}
	if packaging == "" {
		t.Fatal("release workflow omitted bundle assembly")
	}
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			root := privateTempDir(t)
			assets := filepath.Join(root, "dist", "linux-amd64")
			if err := os.MkdirAll(filepath.Join(assets, "nested", "empty"), 0o700); err != nil {
				t.Fatal(err)
			}
			files := []string{"microvm-release-linux-amd64.json", "install-microvm-release.sh", ".hidden", "space name", "line\nbreak", "--option", "nested/.hidden"}
			for _, name := range files {
				if err := os.WriteFile(filepath.Join(assets, name), []byte(name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			script := packaging
			if legacy {
				// The published v0.0.47 convention must remain readable without repacking.
				script = "set -euo pipefail\nbundle=\"mecatl-microvm-${VERSION}-${PLATFORM}.tar.gz\"\ntar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner --exclude=\"${bundle}\" -C \"dist/${PLATFORM}\" -cf - . | gzip -n > \"dist/${PLATFORM}/${bundle}\"\n"
			}
			archive := filepath.Join(assets, "mecatl-microvm-v0.0.0-linux-amd64.tar.gz")
			var previous []byte
			for range 2 {
				cmd := exec.Command("bash", "-c", script)
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root, "VERSION=v0.0.0", "PLATFORM=linux-amd64", "LC_ALL=C"}
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("package: %v\n%s", err, output)
				}
				data, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				if previous != nil && !bytes.Equal(previous, data) {
					t.Fatal("bundle is not reproducible on rerun")
				}
				previous = data
			}
			zr, err := gzip.NewReader(bytes.NewReader(previous))
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			tr := tar.NewReader(zr)
			seen := make(map[string]bool)
			for {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if seen[header.Name] {
					t.Fatalf("duplicate archive entry %q", header.Name)
				}
				seen[header.Name] = true
				if header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 0 {
					t.Fatalf("unnormalized archive metadata: %+v", header)
				}
			}
			if (seen["."] || seen["./"]) != legacy {
				t.Fatalf("unexpected synthetic root presence: legacy=%t, entries=%v", legacy, seen)
			}
			wantEntries := len(files) + 2
			if legacy {
				wantEntries++
			}
			if len(seen) != wantEntries {
				t.Fatalf("archive entries = %v, want %d", seen, wantEntries)
			}
			destination := filepath.Join(root, "unpacked")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := extractReleaseBundle(root, archive, destination); err != nil {
				t.Fatalf("extract workflow bundle: %v", err)
			}
			for _, name := range files {
				data, err := os.ReadFile(filepath.Join(destination, name))
				if err != nil || string(data) != name {
					t.Fatalf("extracted %q = %q, %v", name, data, err)
				}
			}
			if info, err := os.Stat(filepath.Join(destination, "nested", "empty")); err != nil || !info.IsDir() {
				t.Fatalf("empty directory not preserved: %v", err)
			}
		})
	}
}
