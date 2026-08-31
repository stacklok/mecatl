package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

const maxSettingsConfigBytes = 256 * 1024

func runConfigValidate(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("mecated config validate", flag.ContinueOnError)
	fs.SetOutput(out)
	var file, learningPatch string
	fs.StringVar(&file, "file", "", "path to the complete settings.yaml (default: $XDG_CONFIG_HOME/mecatl/settings.yaml)")
	fs.StringVar(&learningPatch, "learning-patch", "", "path to a learning-only YAML patch to validate in memory")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("config validate: unexpected arguments; usage: mecated config validate [--file PATH] [--learning-patch PATCH]")
	}

	path := file
	if path == "" {
		cfgDir := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
		if cfgDir == "" {
			return fmt.Errorf("cannot resolve the user config directory (set $XDG_CONFIG_HOME or $HOME); pass --file PATH")
		}
		path = filepath.Join(cfgDir, permconfig.UserSettingsRelPath)
	}

	base, missing, err := readConfigFile(path, learningPatch != "", false)
	if err != nil {
		return err
	}
	var patch []byte
	if learningPatch != "" {
		patch, _, err = readConfigFile(learningPatch, false, true)
		if err != nil {
			return err
		}
	}
	if _, err := validateSettingsInMemory(base, patch); err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	if missing {
		_, err = fmt.Fprintln(out, "valid (new file)")
	} else {
		_, err = fmt.Fprintln(out, "valid")
	}
	return err
}

func readConfigFile(path string, allowMissing, learningPatch bool) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			if learningPatch {
				return nil, false, fmt.Errorf("learning patch %q does not exist", path)
			}
			return nil, false, fmt.Errorf("%q does not exist; create it with 'mecated config init' or pass --learning-patch to preflight a new file", path)
		}
		return nil, false, fmt.Errorf("cannot read %q", path)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%q is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSettingsConfigBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("cannot read %q", path)
	}
	if len(data) > maxSettingsConfigBytes {
		return nil, false, fmt.Errorf("%q exceeds the %d-byte settings limit", path, maxSettingsConfigBytes)
	}
	return data, false, nil
}

// validateSettingsInMemory validates a complete settings document. When patch is
// non-nil, it must contain only a top-level learning mapping, which replaces or
// inserts the base document's learning node in memory before full validation.
func validateSettingsInMemory(base, patch []byte) ([]byte, error) {
	doc, err := parseSettingsDocument(base, true)
	if err != nil {
		return nil, err
	}
	if patch == nil {
		if err := permconfig.ValidateYAML(base); err != nil {
			return nil, errors.New("settings schema validation failed")
		}
		return append([]byte(nil), base...), nil
	}

	patchDoc, err := parseSettingsDocument(patch, false)
	if err != nil {
		return nil, errors.New("learning patch is not a single safe YAML document")
	}
	patchMapping := patchDoc.Mapping()
	if len(patchMapping.Values) != 1 || patchMapping.Values[0].Key.String() != "learning" {
		return nil, errors.New("learning patch must contain exactly one top-level learning mapping")
	}
	learning := patchMapping.Values[0]
	if _, ok := learning.Value.(*ast.MappingNode); !ok {
		return nil, errors.New("learning patch must contain exactly one top-level learning mapping")
	}
	if len(bytes.TrimSpace(base)) == 0 {
		doc = patchDoc
	} else {
		replaceMappingValue(doc.Mapping(), learning)
	}
	proposed := []byte(doc.String())
	if len(proposed) > maxSettingsConfigBytes {
		return nil, errors.New("proposed settings exceed the size limit")
	}
	if err := permconfig.ValidateYAML(proposed); err != nil {
		return nil, errors.New("proposed settings schema validation failed")
	}
	return proposed, nil
}

func parseSettingsDocument(data []byte, allowEmpty bool) (*yamldiag.Document, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		if !allowEmpty {
			return nil, errors.New("empty YAML document")
		}
		data = []byte("{}\n")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		var documentError *yamldiag.DocumentError
		if errors.As(err, &documentError) && documentError.Location.HasLocation {
			return nil, fmt.Errorf("malformed YAML document at line %d, column %d", documentError.Location.Line, documentError.Location.Column)
		}
		return nil, errors.New("malformed YAML document")
	}
	if err := validateYAMLTree(doc.Mapping()); err != nil {
		return nil, err
	}
	return doc, nil
}

func validateYAMLTree(node ast.Node) error {
	mapping, ok := node.(*ast.MappingNode)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{}, len(mapping.Values))
	for _, entry := range mapping.Values {
		key := entry.Key.String()
		if _, ok := seen[key]; ok {
			return errors.New("duplicate YAML mapping key")
		}
		seen[key] = struct{}{}
		if err := validateYAMLTree(entry.Value); err != nil {
			return err
		}
	}
	return nil
}

func replaceMappingValue(mapping *ast.MappingNode, replacement *ast.MappingValueNode) {
	for _, entry := range mapping.Values {
		if entry.Key.String() == replacement.Key.String() {
			_ = entry.Replace(replacement.Value)
			return
		}
	}
	mapping.Values = append(mapping.Values, replacement)
}
