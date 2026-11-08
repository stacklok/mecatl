package dtogen

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const generatorCommand = "./internal/dtogen/cmd/dtogen"

// GenerateRepository derives targets solely from source-local go:generate lines.
// Check compares both inventory and bytes without writing. Generation does not
// scan for orphan artifacts; removing obsolete outputs is an explicit action.
func GenerateRepository(ctx context.Context, root string, check bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targets, err := discoverTargets(root)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, target := range targets {
		expected[filepath.Join(target.Dir, target.Output)] = true
	}
	if check {
		if err := checkInventory(root, expected); err != nil {
			return err
		}
	}
	artifacts, err := renderTargets(ctx, targets)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if !check {
			if err := writeArtifact(artifact.path, artifact.data); err != nil {
				return err
			}
			continue
		}
		got, err := readOutput(artifact.path)
		if err != nil {
			return err
		}
		if got == nil {
			return fmt.Errorf("missing data output: %s", artifact.path)
		}
		if !bytes.Equal(got, artifact.data) {
			return fmt.Errorf("stale data output: %s", artifact.path)
		}
	}
	return nil
}

// discoverTargets is the shared source registry for repository inventory and
// single-owner bootstrap. Traversal order is stable; dependencies precede owners
// when Generate installs each selected package's closure.
func discoverTargets(root string) ([]LoadConfig, error) {
	var targets []LoadConfig
	err := walkSource(root, func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "//go:generate ") && strings.Contains(line, "./internal/cborgen/cmd/cborgen") {
				return fmt.Errorf("%s:%d: obsolete cborgen declaration; use dtogen with explicit -emit", path, i+1)
			}
			if !strings.HasPrefix(line, "//go:generate ") || !strings.Contains(line, generatorCommand) {
				continue
			}
			target, err := directiveTarget(root, filepath.Dir(path), strings.Fields(strings.TrimPrefix(line, "//go:generate ")))
			if err != nil {
				return fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			for _, previous := range targets {
				if previous.Dir == target.Dir {
					return fmt.Errorf("require one data output per package: %s", target.Dir)
				}
			}
			targets = append(targets, target)
		}
		return nil
	})
	return targets, err
}

// directiveTarget accepts the literal dtogen invocation used across modules:
// go -C <repository-relative-to-source> run ./internal/dtogen/cmd/dtogen
// -dir <source-relative-to-repository> -types A,B -emit cbor,clone,equal -out generated.go.
// This is not a general go:generate interpreter: quoting, variables and aliases
// are intentionally unsupported, so discovery and direct invocation agree.
func directiveTarget(root, source string, words []string) (LoadConfig, error) {
	var target LoadConfig
	if strings.ContainsAny(strings.Join(words, ""), "\"$\\") {
		return target, fmt.Errorf("data directives require literal unquoted arguments")
	}
	if len(words) < 5 || words[0] != "go" || words[1] != "-C" || words[3] != "run" || words[4] != generatorCommand || filepath.Clean(filepath.Join(source, words[2])) != root {
		return target, fmt.Errorf("expected go -C <repository> run %s -dir <declaring-package> -types A,B -out generated.go", generatorCommand)
	}
	flags := flag.NewFlagSet("dtogen directive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&target.Dir, "dir", "", "")
	flags.StringVar(&target.Output, "out", "", "")
	flags.StringVar(&target.Emit, "emit", "", "")
	roots := flags.String("types", "", "")
	if err := flags.Parse(words[5:]); err != nil {
		return target, err
	}
	if flags.NArg() != 0 || !filepath.IsLocal(target.Dir) || filepath.Join(root, target.Dir) != source || *roots == "" || !validOutputName(target.Output) {
		return target, fmt.Errorf("require declaring-package -dir, root -types and package-local non-test Go -out filename")
	}
	if _, err := parseEmit(target.Emit); err != nil {
		return target, err
	}
	target.Dir = source
	if *roots != "" {
		target.Types = strings.Split(*roots, ",")
	}
	return target, nil
}

// walkSource includes untracked source across modules, but not dependencies or
// scratch trees. Directory symlinks are not traversed.
func walkSource(root string, visit func(string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".scratch", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		return visit(path)
	})
}

// checkInventory catches artifacts of removed targets, including untracked files.
func checkInventory(root string, expected map[string]bool) error {
	return walkSource(root, func(path string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		prefix := make([]byte, len(legacyGeneratedMarker)+1)
		n, readErr := io.ReadFull(file, prefix)
		closeErr := file.Close()
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		marker := strings.SplitN(string(prefix[:n]), "\n", 2)[0]
		if (marker == GeneratedMarker || marker == legacyGeneratedMarker) && !expected[path] {
			return fmt.Errorf("extra data output (removed or unlisted target): %s", path)
		}
		return nil
	})
}
