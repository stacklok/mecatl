package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	yaml "go.yaml.in/yaml/v3"
)

const maxSettingsBytes = 256 * 1024

type proposedFlags struct {
	file                       string
	mode                       string
	sensitivity                string
	activation                 string
	cooldown                   time.Duration
	window                     time.Duration
	maxReflections             int
	maxTokens                  int
	maxReflectionsPerPrincipal int
	maxTokensPerPrincipal      int
	allowMissing               bool
}

type proposedLearning struct {
	Mode        string            `yaml:"mode"`
	Sensitivity string            `yaml:"sensitivity"`
	Skills      proposedSkills    `yaml:"skills"`
	Automatic   proposedAutomatic `yaml:"automatic"`
}

type proposedSkills struct {
	Activation string `yaml:"activation"`
}

type proposedAutomatic struct {
	Cooldown                   string `yaml:"cooldown"`
	Window                     string `yaml:"window"`
	MaxReflections             int    `yaml:"max_reflections"`
	MaxTokens                  int    `yaml:"max_tokens"`
	MaxReflectionsPerPrincipal int    `yaml:"max_reflections_per_principal"`
	MaxTokensPerPrincipal      int    `yaml:"max_tokens_per_principal"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Println("invalid settings: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("valid")
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: existing --file PATH | proposed --file PATH [closed learning flags]")
	}
	switch args[0] {
	case "existing":
		fs := flag.NewFlagSet("existing", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("file", "", "settings file")
		if err := fs.Parse(args[1:]); err != nil || *path == "" || fs.NArg() != 0 {
			return errors.New("invalid existing-mode flags")
		}
		data, err := readSettings(*path, false)
		if err != nil {
			return err
		}
		if _, err := parseSingleDocument(data); err != nil {
			return err
		}
		if err := permconfig.ValidateYAML(data); err != nil {
			return errors.New("configuration parse failed")
		}
		return nil
	case "proposed":
		cfg, err := parseProposedFlags(args[1:])
		if err != nil {
			return err
		}
		return validateProposed(cfg)
	default:
		return errors.New("usage: existing --file PATH | proposed --file PATH [closed learning flags]")
	}
}

func parseProposedFlags(args []string) (proposedFlags, error) {
	var cfg proposedFlags
	fs := flag.NewFlagSet("proposed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.file, "file", "", "settings file")
	fs.StringVar(&cfg.mode, "mode", "", "off|review|auto")
	fs.StringVar(&cfg.sensitivity, "sensitivity", "", "conservative|balanced|eager")
	fs.StringVar(&cfg.activation, "activation", "", "validated|evaluated")
	fs.DurationVar(&cfg.cooldown, "cooldown", -1, "nonnegative duration")
	fs.DurationVar(&cfg.window, "window", 0, "1m..24h")
	fs.IntVar(&cfg.maxReflections, "max-reflections", -1, "0..1000000000")
	fs.IntVar(&cfg.maxTokens, "max-tokens", -1, "0..1000000000")
	fs.IntVar(&cfg.maxReflectionsPerPrincipal, "max-reflections-per-principal", -1, "0..1000000000")
	fs.IntVar(&cfg.maxTokensPerPrincipal, "max-tokens-per-principal", -1, "0..1000000000")
	fs.BoolVar(&cfg.allowMissing, "allow-missing", false, "validate insertion into a missing target")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return proposedFlags{}, errors.New("invalid proposed-mode flags")
	}
	if cfg.file == "" || !oneOf(cfg.mode, "off", "review", "auto") ||
		!oneOf(cfg.sensitivity, "conservative", "balanced", "eager") ||
		!oneOf(cfg.activation, "validated", "evaluated") {
		return proposedFlags{}, errors.New("invalid proposed learning value")
	}
	if cfg.cooldown < 0 || cfg.window < time.Minute || cfg.window > 24*time.Hour ||
		!validMaximum(cfg.maxReflections) || !validMaximum(cfg.maxTokens) ||
		!validMaximum(cfg.maxReflectionsPerPrincipal) || !validMaximum(cfg.maxTokensPerPrincipal) {
		return proposedFlags{}, errors.New("proposed learning value out of range")
	}
	return cfg, nil
}

func validateProposed(cfg proposedFlags) error {
	data, err := readSettings(cfg.file, cfg.allowMissing)
	if err != nil {
		return err
	}
	doc, err := parseSingleDocument(data)
	if err != nil {
		return err
	}
	learning, err := learningNode(cfg)
	if err != nil {
		return errors.New("proposed learning node could not be generated")
	}
	if err := replaceTopLevelLearning(doc, learning); err != nil {
		return err
	}
	proposed, err := yaml.Marshal(doc)
	if err != nil {
		return errors.New("proposed configuration could not be marshaled")
	}
	if len(proposed) > maxSettingsBytes {
		return errors.New("proposed configuration exceeds the 262144-byte limit")
	}
	if err := permconfig.ValidateYAML(proposed); err != nil {
		return errors.New("proposed configuration validation failed")
	}
	return nil
}

func readSettings(path string, allowMissing bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errors.New("file is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return nil, errors.New("file could not be read")
	}
	if len(data) > maxSettingsBytes {
		return nil, errors.New("file exceeds the 262144-byte limit")
	}
	return data, nil
}

func parseSingleDocument(data []byte) (*yaml.Node, error) {
	if len(data) == 0 {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("configuration parse failed")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple YAML documents are not supported")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration must be a top-level mapping")
	}
	return &doc, nil
}

func learningNode(cfg proposedFlags) (*yaml.Node, error) {
	data, err := yaml.Marshal(proposedLearning{
		Mode: cfg.mode, Sensitivity: cfg.sensitivity,
		Skills: proposedSkills{Activation: cfg.activation},
		Automatic: proposedAutomatic{
			Cooldown: cfg.cooldown.String(), Window: cfg.window.String(),
			MaxReflections: cfg.maxReflections, MaxTokens: cfg.maxTokens,
			MaxReflectionsPerPrincipal: cfg.maxReflectionsPerPrincipal,
			MaxTokensPerPrincipal:      cfg.maxTokensPerPrincipal,
		},
	})
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) != 1 {
		return nil, errors.New("invalid generated node")
	}
	return doc.Content[0], nil
}

func replaceTopLevelLearning(doc, learning *yaml.Node) error {
	mapping := doc.Content[0]
	found := -1
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "learning" {
			continue
		}
		if found >= 0 {
			return errors.New("duplicate top-level learning key")
		}
		found = i
	}
	if found >= 0 {
		mapping.Content[found+1] = learning
		return nil
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "learning"}, learning)
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validMaximum(value int) bool { return value >= 0 && value <= 1_000_000_000 }
