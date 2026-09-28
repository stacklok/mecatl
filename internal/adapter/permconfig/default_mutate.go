package permconfig

import (
	"bytes"
	"errors"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

func mutateDefaults(data []byte, update DefaultUpdate) ([]byte, bool, error) {
	// An absent or empty settings document holds no unrelated settings to
	// preserve, so the canonical document is written directly. Seeding the
	// mutation with an empty "{}" flow mapping instead would keep its braces and
	// render "{models: ...}", which the settings schema rejects, leaving
	// `providers set-default` unable to run on a host with no settings file.
	if len(bytes.TrimSpace(data)) == 0 {
		out := []byte(defaultStaticText(update))
		if err := ValidateYAML(out); err != nil {
			return nil, false, errors.New("updated settings document is invalid")
		}
		return out, false, nil
	}
	if err := ValidateYAML(data); err != nil {
		return nil, false, errors.New("settings document is invalid")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, false, errors.New("settings document is invalid or ambiguous")
	}
	modelsNode, err := uniqueDefaultValue(doc.Mapping(), "models")
	if err != nil {
		return nil, false, err
	}
	if modelsNode == nil {
		entry := defaultStaticEntry(defaultStaticText(update))
		doc.Mapping().Values = append(doc.Mapping().Values, entry)
		if doc.Mapping().IsFlowStyle {
			doc.Mapping().SetIsFlowStyle(true)
		}
		out := []byte(doc.String())
		if err := ValidateYAML(out); err != nil {
			return nil, false, errors.New("updated settings document is invalid")
		}
		return out, false, nil
	}
	models, ok := modelsNode.(*ast.MappingNode)
	if !ok {
		return nil, false, errors.New("models must be a mapping")
	}
	providerNode, err := uniqueDefaultValue(models, "default_provider")
	if err != nil {
		return nil, false, err
	}
	modelNode, err := uniqueDefaultValue(models, "default")
	if err != nil {
		return nil, false, err
	}
	provider, providerOK := defaultString(providerNode)
	model, modelOK := defaultString(modelNode)
	if providerNode != nil && !providerOK || modelNode != nil && !modelOK {
		return nil, false, errors.New("models defaults must be string scalars")
	}
	if provider == update.Provider && model == update.Model {
		return data, true, nil
	}
	setDefaultScalar(models, "default_provider", update.Provider)
	setDefaultScalar(models, "default", update.Model)
	out := []byte(doc.String())
	if err := ValidateYAML(out); err != nil {
		return nil, false, errors.New("updated settings document is invalid")
	}
	return out, false, nil
}

func uniqueDefaultValue(mapping *ast.MappingNode, wanted string) (ast.Node, error) {
	var found ast.Node
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := defaultString(entry.Key)
		if !ok || seen[key] {
			return nil, errors.New("settings document has duplicate or non-string mapping key")
		}
		seen[key] = true
		if key == wanted {
			found = entry.Value
		}
	}
	return found, nil
}

func setDefaultScalar(mapping *ast.MappingNode, key, value string) {
	replacement := defaultNestedEntry(key, value)
	for _, entry := range mapping.Values {
		existing, _ := defaultString(entry.Key)
		if existing == key {
			_ = entry.Replace(replacement.Value)
			return
		}
	}
	mapping.Values = append(mapping.Values, replacement)
}

func defaultString(node ast.Node) (string, bool) {
	value, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func defaultNestedEntry(key, value string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte("models:\n  " + key + ": " + defaultQuote(value) + "\n"))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static nested settings YAML")
	}
	models, ok := doc.Mapping().Values[0].Value.(*ast.MappingNode)
	if !ok || len(models.Values) != 1 {
		panic("invalid static nested settings mapping")
	}
	return models.Values[0]
}

func defaultStaticEntry(text string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(text))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static settings YAML")
	}
	return doc.Mapping().Values[0]
}

// defaultStaticText renders the canonical `models` defaults block for an
// addition to a document that has none.
func defaultStaticText(update DefaultUpdate) string {
	return "models:\n  default_provider: " + defaultQuote(update.Provider) + "\n  default: " + defaultQuote(update.Model) + "\n"
}

func defaultQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
