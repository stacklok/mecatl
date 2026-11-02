package cborgen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Generate validates and renders a complete artifact before replacing it atomically.
// It refuses symlink/nonregular and unmarked output files, and never executes owner
// code. Concurrent generation or editing of the same artifact is unsupported.
func Generate(ctx context.Context, config LoadConfig) error {
	loaded, err := Load(ctx, config)
	if err != nil {
		return err
	}
	data, err := render(loaded)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(loaded.Output), ".cborgen-*.go")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = file.Chmod(0o644); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), loaded.Output)
}

// render validates the entire schema and formats complete source before any write.
// Freshness uses the same path as generation without creating temporary files.
func render(loaded *Loaded) ([]byte, error) {
	s, err := buildSchema(loaded)
	if err != nil {
		return nil, err
	}
	return emit(s)
}

// readOutput refuses nonregular or handwritten output. Nil means missing output.
func readOutput(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("output must be a regular file, not a symlink: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.SplitN(string(data), "\n", 2)[0] != GeneratedMarker {
		return nil, fmt.Errorf("refusing unmarked output: %s", path)
	}
	return data, nil
}
