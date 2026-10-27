// Package timezonegen owns the pinned timezone catalog's source and freshness checks.
// It uses only the standard library and is not linked into the client.
package timezonegen

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Catalog records names and provenance, not timezone rules.
type Catalog struct {
	GoVersion     string   `json:"go_version"`
	ArchiveSHA256 string   `json:"archive_sha256"`
	Names         []string `json:"names"`
}

// SourceVersion pins catalog provenance independently of the module's language
// floor and CI's rolling stable toolchain. Bump deliberately, then regenerate.
const SourceVersion = "go1.27.3"

// Command is the actionable regeneration command for the declared toolchain.
func Command(declared string) string {
	return "GOTOOLCHAIN=" + declared + " task generate:timezones"
}

// RequireVersion prevents an ambient newer toolchain from changing catalog provenance.
func RequireVersion(declared, active string) error {
	if active != declared {
		return fmt.Errorf("timezone source requires %s, running %s; install/select the source toolchain and run: %s", declared, active, Command(declared))
	}
	return nil
}

// Source reads the Go distribution archive and retains loadable slash-qualified IDs plus UTC.
// The caller enforces the source runtime version before generating or comparing it.
func Source(goRoot, sourceVersion string) (Catalog, error) {
	data, err := os.ReadFile(filepath.Join(goRoot, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		return Catalog{}, err
	}
	sum := sha256.Sum256(data)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Catalog{}, err
	}
	names := []string{"UTC"}
	for _, entry := range archive.File {
		if !strings.Contains(entry.Name, "/") || entry.FileInfo().IsDir() {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			return Catalog{}, err
		}
		rules, err := io.ReadAll(r)
		closeErr := r.Close()
		if err != nil {
			return Catalog{}, err
		}
		if closeErr != nil {
			return Catalog{}, closeErr
		}
		if _, err := time.LoadLocationFromTZData(entry.Name, rules); err == nil {
			names = append(names, entry.Name)
		}
	}
	slices.Sort(names)
	return Catalog{GoVersion: sourceVersion, ArchiveSHA256: hex.EncodeToString(sum[:]), Names: slices.Compact(names)}, nil
}

// CheckFreshness always checks the source pin, then compares archives only under
// that exact runtime. Other supported runtimes get a diagnostic instead;
// semantic checks still belong to the caller. source is lazy so those runtimes
// never read a different archive.
func CheckFreshness(c Catalog, declared, active string, source func() (Catalog, error)) (string, error) {
	stale := func() error { return fmt.Errorf("stale timezone catalog; regenerate with: %s", Command(declared)) }
	if c.GoVersion != declared {
		return "", stale()
	}
	if active != declared {
		return fmt.Sprintf("timezone source-archive freshness requires %s (running %s); run: %s", declared, active, Command(declared)), nil
	}
	want, err := source()
	if err != nil {
		return "", fmt.Errorf("timezone source: %w; run: %s", err, Command(declared))
	}
	if c.ArchiveSHA256 != want.ArchiveSHA256 || !slices.Equal(c.Names, want.Names) {
		return "", stale()
	}
	return "", nil
}
