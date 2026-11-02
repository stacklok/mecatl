package cborgen

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

const generatorCommand = "./internal/cborgen/cmd/cborgen"

// GenerateRepository derives targets solely from source-local go:generate lines.
// Check compares both inventory and bytes without writing. Generation does not
// scan for orphan artifacts; removing obsolete outputs is an explicit action.
func GenerateRepository(ctx context.Context, root string, check bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	var targets []LoadConfig
	expected := map[string]bool{}
	err = walkSource(root, func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "//go:generate ") || !strings.Contains(line, generatorCommand) {
				continue
			}
			target, err := directiveTarget(root, filepath.Dir(path), strings.Fields(strings.TrimPrefix(line, "//go:generate ")))
			if err != nil {
				return fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			output := filepath.Join(target.Dir, target.Output)
			for _, previous := range targets {
				if previous.Dir == target.Dir {
					return fmt.Errorf("require one codec output per package: %s", target.Dir)
				}
			}
			expected[output] = true
			targets = append(targets, target)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if check {
		if err := checkInventory(root, expected); err != nil {
			return err
		}
	}
	for _, target := range targets {
		if !check {
			if err := Generate(ctx, target); err != nil {
				return err
			}
			continue
		}
		loaded, err := Load(ctx, target)
		if err != nil {
			return err
		}
		want, err := render(loaded)
		if err != nil {
			return err
		}
		got, err := readOutput(loaded.Output)
		if err != nil {
			return err
		}
		if got == nil {
			return fmt.Errorf("missing codec output: %s", loaded.Output)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("stale codec output: %s", loaded.Output)
		}
	}
	return nil
}

// directiveTarget accepts the literal cborgen invocation used across modules:
// go -C <repository-relative-to-source> run ./internal/cborgen/cmd/cborgen
// -dir <source-relative-to-repository> -types A,B -out generated.go.
// This is not a general go:generate interpreter: quoting, variables and aliases
// are intentionally unsupported, so discovery and direct invocation agree.
func directiveTarget(root, source string, words []string) (LoadConfig, error) {
	var target LoadConfig
	if strings.ContainsAny(strings.Join(words, ""), "\"$\\") {
		return target, fmt.Errorf("codec directives require literal unquoted arguments")
	}
	if len(words) < 5 || words[0] != "go" || words[1] != "-C" || words[3] != "run" || words[4] != generatorCommand || filepath.Clean(filepath.Join(source, words[2])) != root {
		return target, fmt.Errorf("expected go -C <repository> run %s -dir <declaring-package> -types A,B -out generated.go", generatorCommand)
	}
	flags := flag.NewFlagSet("cborgen directive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&target.Dir, "dir", "", "")
	flags.StringVar(&target.Output, "out", "", "")
	roots := flags.String("types", "", "")
	if err := flags.Parse(words[5:]); err != nil {
		return target, err
	}
	if flags.NArg() != 0 || !filepath.IsLocal(target.Dir) || filepath.Join(root, target.Dir) != source || *roots == "" || !validOutputName(target.Output) {
		return target, fmt.Errorf("require declaring-package -dir, root -types and package-local non-test Go -out filename")
	}
	target.Dir = source
	target.Types = strings.Split(*roots, ",")
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
		prefix := make([]byte, len(GeneratedMarker)+1)
		n, readErr := io.ReadFull(file, prefix)
		closeErr := file.Close()
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if (string(prefix[:n]) == GeneratedMarker+"\n" || string(prefix[:n]) == GeneratedMarker) && !expected[path] {
			return fmt.Errorf("extra codec output (removed or unlisted target): %s", path)
		}
		return nil
	})
}
