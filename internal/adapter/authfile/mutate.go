package authfile

import (
	"bytes"
	"errors"

	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/providerid"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

func validateAuthDocument(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return errors.New("auth document is invalid or ambiguous")
	}
	providersNode, err := uniqueAuthValue(doc.Mapping(), "providers", true)
	if err != nil {
		return err
	}
	if providersNode == nil {
		return errors.New("auth document is missing providers mapping")
	}
	providers, ok := providersNode.(*ast.MappingNode)
	if !ok {
		return errors.New("auth providers must be a mapping")
	}
	seen := map[string]bool{}
	for _, entry := range providers.Values {
		name, ok := authString(entry.Key)
		if !ok || !providerid.Valid(name) || seen[name] {
			return errors.New("auth providers contain an invalid or duplicate id")
		}
		seen[name] = true
		mapping, ok := entry.Value.(*ast.MappingNode)
		if !ok {
			return errors.New("auth provider entry must be a mapping")
		}
		if err := validateProviderMapping(mapping); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gocyclo // Schema validation and the one targeted mutation stay together to preserve the AST safely.
func mutateAuth(data []byte, update APIKeyUpdate) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		if update.APIKey == nil {
			return data, true, nil
		}
		out := []byte("providers:\n  " + update.Provider + ":\n    api_key: " + quoteYAML(*update.APIKey) + "\n")
		if err := validateUpdatedAuth(out); err != nil {
			return nil, false, err
		}
		return out, false, nil
	}
	if err := validateAuthDocument(data); err != nil {
		return nil, false, err
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, false, errors.New("auth document is invalid or ambiguous")
	}
	providersNode, err := uniqueAuthValue(doc.Mapping(), "providers", true)
	if err != nil {
		return nil, false, err
	}
	providers, ok := providersNode.(*ast.MappingNode)
	if !ok {
		return nil, false, errors.New("auth providers must be a mapping")
	}
	var target *ast.MappingValueNode
	seen := map[string]bool{}
	for _, entry := range providers.Values {
		name, ok := authString(entry.Key)
		if !ok || !providerid.Valid(name) || seen[name] {
			return nil, false, errors.New("auth providers contain an invalid or duplicate id")
		}
		seen[name] = true
		mapping, ok := entry.Value.(*ast.MappingNode)
		if !ok {
			return nil, false, errors.New("auth provider entry must be a mapping")
		}
		if err := validateProviderMapping(mapping); err != nil {
			return nil, false, err
		}
		if name == update.Provider {
			target = entry
		}
	}
	if target == nil {
		if update.APIKey == nil {
			return data, true, nil
		}
		providers.SetIsFlowStyle(false)
		target = staticAuthEntry(update.Provider + ":\n  api_key: " + quoteYAML(*update.APIKey) + "\n")
		target.AddColumn(2)
		providers.Values = append(providers.Values, target)
		out := []byte(doc.String())
		if err := validateUpdatedAuth(out); err != nil {
			return nil, false, err
		}
		return out, false, nil
	}
	mapping := target.Value.(*ast.MappingNode)
	for _, entry := range mapping.Values {
		key, _ := authString(entry.Key)
		if key == "oauth" {
			return nil, false, errors.New("target provider uses OAuth and cannot be mutated as an API-key record")
		}
	}
	var keyEntry *ast.MappingValueNode
	for _, entry := range mapping.Values {
		key, _ := authString(entry.Key)
		if key == "api_key" {
			keyEntry = entry
		}
	}
	if update.APIKey == nil {
		// Remove the empty credential record as well: leaving a custom ID behind
		// makes strict loading fail after its provider definition is removed.
		for i, entry := range providers.Values {
			if entry == target {
				providers.Values = append(providers.Values[:i], providers.Values[i+1:]...)
				break
			}
		}
		if len(providers.Values) == 0 {
			for i, entry := range doc.Mapping().Values {
				key, _ := authString(entry.Key)
				if key == "providers" {
					doc.Mapping().Values = append(doc.Mapping().Values[:i], doc.Mapping().Values[i+1:]...)
					break
				}
			}
		}
	} else if keyEntry != nil {
		current, ok := authString(keyEntry.Value)
		if !ok {
			return nil, false, errors.New("auth api_key must be a string")
		}
		if current == *update.APIKey {
			return data, true, nil
		}
		providers.SetIsFlowStyle(false)
		mapping.SetIsFlowStyle(false)
		replacement := staticAuthEntry("api_key: " + quoteYAML(*update.APIKey) + "\n")
		if err := keyEntry.Replace(replacement.Value); err != nil {
			return nil, false, errors.New("replace auth api_key")
		}
	} else {
		providers.SetIsFlowStyle(false)
		mapping.SetIsFlowStyle(false)
		mapping.Values = append(mapping.Values, staticAuthEntry("api_key: "+quoteYAML(*update.APIKey)+"\n"))
	}
	if len(doc.Mapping().Values) == 0 {
		return []byte{}, false, nil
	}
	out := []byte(doc.String())
	if err := validateUpdatedAuth(out); err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func validateUpdatedAuth(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil || doc.Mapping() == nil {
		return errors.New("updated auth document is invalid")
	}
	return nil
}

func validateProviderMapping(mapping *ast.MappingNode) error {
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := authString(entry.Key)
		if !ok || seen[key] || (key != "api_key" && key != "oauth") {
			return errors.New("auth provider entry has duplicate or unknown schema")
		}
		seen[key] = true
		if key == "api_key" {
			if _, ok := authString(entry.Value); !ok {
				return errors.New("auth api_key must be a string")
			}
			continue
		}
		oauth, ok := entry.Value.(*ast.MappingNode)
		if !ok {
			return errors.New("auth oauth must be a mapping")
		}
		oauthSeen := map[string]bool{}
		for _, field := range oauth.Values {
			name, ok := authString(field.Key)
			if !ok || oauthSeen[name] || (name != "access_token" && name != "account_id" && name != "expires_at") {
				return errors.New("auth oauth has duplicate or unknown schema")
			}
			oauthSeen[name] = true
			if _, ok := authString(field.Value); !ok {
				return errors.New("auth oauth values must be strings")
			}
		}
	}
	return nil
}

func uniqueAuthValue(mapping *ast.MappingNode, wanted string, rejectUnknown bool) (ast.Node, error) {
	var found ast.Node
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := authString(entry.Key)
		if !ok || seen[key] || (rejectUnknown && key != wanted) {
			return nil, errors.New("auth root has duplicate or unknown schema")
		}
		seen[key] = true
		if key == wanted {
			found = entry.Value
		}
	}
	return found, nil
}

func authString(node ast.Node) (string, bool) {
	if tagged, ok := node.(*ast.TagNode); ok && tagged.Start != nil && tagged.Start.Value == "!!str" {
		if parserToken := tagged.Value.GetToken(); parserToken != nil {
			return parserToken.Value, true
		}
	}
	value, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func staticAuthEntry(text string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(text))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static auth YAML")
	}
	return doc.Mapping().Values[0]
}

func quoteYAML(value string) string {
	var b bytes.Buffer
	b.WriteByte('\'')
	for _, r := range value {
		if r == '\'' {
			b.WriteString("''")
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
