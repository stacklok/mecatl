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

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
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
		return fmt.Errorf("%q: settings validation failed", path)
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
	mapping := patchDoc.Content[0]
	if len(mapping.Content) != 2 || mapping.Content[0].Kind != yaml.ScalarNode || mapping.Content[0].Value != "learning" || mapping.Content[1].Kind != yaml.MappingNode {
		return nil, errors.New("learning patch must contain exactly one top-level learning mapping")
	}
	replaceMappingValue(doc.Content[0], "learning", mapping.Content[1])
	proposed, err := yaml.Marshal(doc)
	if err != nil {
		return nil, errors.New("cannot marshal proposed settings")
	}
	if len(proposed) > maxSettingsConfigBytes {
		return nil, errors.New("proposed settings exceed the size limit")
	}
	if err := permconfig.ValidateYAML(proposed); err != nil {
		return nil, errors.New("proposed settings schema validation failed")
	}
	return proposed, nil
}

func parseSettingsDocument(data []byte, allowEmpty bool) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		if !allowEmpty {
			return nil, errors.New("empty YAML document")
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("malformed YAML document")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple YAML documents are not supported")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("settings must be a top-level mapping")
	}
	if err := validateYAMLTree(doc.Content[0]); err != nil {
		return nil, err
	}
	return &doc, nil
}

func validateYAMLTree(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Alias != nil || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			id := key.Tag + "\x00" + key.Value
			if _, ok := seen[id]; ok {
				return errors.New("duplicate YAML mapping key")
			}
			seen[id] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child); err != nil {
			return err
		}
	}
	return nil
}

func replaceMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
