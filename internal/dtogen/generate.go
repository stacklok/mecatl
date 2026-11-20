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
	owners := ownerCache{}
	for _, target := range targets {
		path, err := filepath.Abs(filepath.Join(target.Dir, target.Output))
		if err != nil {
			return nil, err
		}
		if _, ok := overlay[path]; ok {
			continue
		}
		loaded, err := load(ctx, target, owners)
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
	if err := typecheckArtifacts(ctx, artifacts, overlay); err != nil {
		return nil, err
	}
	return artifacts, nil
}

// typecheckArtifacts loads every artifact package once per module, in artifact order.
func typecheckArtifacts(
	ctx context.Context,
	artifacts []artifact,
	overlay map[string][]byte,
) error {
	modules := map[string][]string{}
	var order []string
	for _, artifact := range artifacts {
		dir := filepath.Dir(artifact.path)
		module := moduleRoot(dir)
		if _, ok := modules[module]; !ok {
			order = append(order, module)
		}
		modules[module] = append(modules[module], dir)
	}
	loaded := map[string]*packages.Package{}
	for _, module := range order {
		mode := packages.NeedName | packages.NeedFiles | packages.NeedTypes |
			packages.NeedImports | packages.NeedDeps
		cfg := &packages.Config{
			Context: ctx,
			Dir:     module,
			Env:     loadEnvironment(),
			Mode:    mode,
			Overlay: overlay,
		}
		pkgs, err := packages.Load(cfg, modules[module]...)
		if err != nil {
			return fmt.Errorf("typecheck generated outputs in %s: %w", module, err)
		}
		for _, pkg := range pkgs {
			if len(pkg.GoFiles) != 0 {
				loaded[filepath.Dir(pkg.GoFiles[0])] = pkg
			}
		}
	}
	for _, artifact := range artifacts {
		pkg := loaded[filepath.Dir(artifact.path)]
		if pkg == nil {
			return fmt.Errorf("typecheck generated output %s: package not loaded", artifact.path)
		}
		if err := loadDiagnostics([]*packages.Package{pkg}); err != nil {
			return fmt.Errorf("typecheck generated output %s: %w", artifact.path, err)
		}
	}
	return nil
}

// moduleRoot returns the nearest directory containing go.mod, or dir itself.
func moduleRoot(dir string) string {
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			return p
		}
		if filepath.Dir(p) == p {
			return dir
		}
	}
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
	if marker := strings.SplitN(string(data), "\n", 2)[0]; marker != GeneratedMarker {
		return nil, fmt.Errorf("refusing unmarked output: %s", path)
	}
	return data, nil
}
