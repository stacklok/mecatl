// Package timezonegen generates a snapshot of timezone names and their provenance.
// It uses only the standard library and is not linked into the client.
package timezonegen

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// Source reads the Go distribution archive and retains loadable slash-qualified IDs plus UTC.
// sourceVersion records the toolchain that supplied the archive; it is not a build requirement.
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
