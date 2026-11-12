package dtogen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Generate validates and renders a complete artifact before replacing it atomically.
// It refuses symlink/nonregular and unmarked output files, and never executes owner
// code. Concurrent generation or editing of the same artifact is unsupported.
func Generate(ctx context.Context, config LoadConfig) error {
	artifacts, err := renderTargets(ctx, []LoadConfig{config})
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := writeArtifact(artifact.path, artifact.data); err != nil {
			return err
		}
	}
	return nil
}

// artifact is fully rendered source, ordered after its prerequisites.
type artifact struct {
	path string
	data []byte
}

// renderTargets validates the complete selected closure before installation. All
// output bodies are type checked through load-only overlays; owner code never runs.
func renderTargets(ctx context.Context, targets []LoadConfig) ([]artifact, error) {
	var artifacts []artifact
	overlay := map[string][]byte{}
	for _, target := range targets {
		path, err := filepath.Abs(filepath.Join(target.Dir, target.Output))
		if err != nil {
			return nil, err
		}
		if _, ok := overlay[path]; ok {
			continue
		}
		loaded, err := Load(ctx, target)
		if err != nil {
			return nil, err
		}
		for _, owner := range append(loaded.Prerequisites, loaded) {
			if _, ok := overlay[owner.Output]; ok {
				continue
			}
			data, err := render(owner)
			if err != nil {
				return nil, err
			}
			overlay[owner.Output] = data
			artifacts = append(artifacts, artifact{owner.Output, data})
		}
	}
	for _, artifact := range artifacts {
		if _, err := loadOne(&packages.Config{
			Context: ctx, Dir: filepath.Dir(artifact.path), Env: loadEnvironment(),
			Mode:    packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
			Overlay: overlay,
		}); err != nil {
			return nil, fmt.Errorf("typecheck generated output %s: %w", artifact.path, err)
		}
	}
	return artifacts, nil
}

// writeArtifact atomically installs a completely rendered owner artifact.
func writeArtifact(output string, data []byte) error {
	prior, err := readOutput(output)
	if err != nil {
		return err
	}
	if bytes.Equal(prior, data) {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".dtogen-*.go")
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
	return os.Rename(file.Name(), output)
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
	if marker := strings.SplitN(string(data), "\n", 2)[0]; marker != GeneratedMarker && (marker != legacyGeneratedMarker || !legacyOutput(path)) {
		return nil, fmt.Errorf("refusing unmarked output: %s", path)
	}
	return data, nil
}
