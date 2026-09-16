package permconfig

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// AddDirectMCPServer preserves the legacy environment-key profile shape.
func AddDirectMCPServer(data []byte, name, endpoint, issuer, root, keyEnv string) ([]byte, error) {
	return addDirectMCPServer(data, name, endpoint, issuer, root, "key_env: "+quoteYAML(keyEnv))
}

// AddDirectMCPServerWithKey writes a root-pinned native custody profile.
func AddDirectMCPServerWithKey(data []byte, name, endpoint, issuer, root, mode, keyPath string) ([]byte, error) {
	key := "key:\n                mode: " + quoteYAML(mode)
	if mode == "file" {
		key += "\n                file: {path: " + quoteYAML(keyPath) + "}"
	}
	return addDirectMCPServer(data, name, endpoint, issuer, root, key)
}

func quoteYAML(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func addDirectMCPServer(data []byte, name, endpoint, issuer, root, keyDeclaration string) ([]byte, error) {
	wasEmpty := len(bytes.TrimSpace(data)) == 0
	if wasEmpty {
		data = []byte("{}\n")
	}
	if err := ValidateYAML(data); err != nil {
		return nil, errors.New("settings document is invalid")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, errors.New("settings document is invalid or ambiguous")
	}
	if wasEmpty {
		// The placeholder above parses as a flow-style "{}" mapping. Every entry
		// this function appends is block-style; serializing block-style values
		// inside a flow-style parent produces malformed YAML.
		doc.Mapping().IsFlowStyle = false
	}
	mcpNode, err := uniqueDirectMCPValue(doc.Mapping(), "mcp")
	if err != nil {
		return nil, err
	}
	if mcpNode == nil {
		doc.Mapping().Values = append(doc.Mapping().Values, directMCPEntry(directMCPDocument(name, endpoint, issuer, root, keyDeclaration)))
		return validateDirectMCPDocument(doc)
	}
	mcpMap, ok := mcpNode.(*ast.MappingNode)
	if !ok {
		return nil, errors.New("mcp must be a mapping")
	}
	serversNode, err := uniqueDirectMCPValue(mcpMap, "servers")
	if err != nil {
		return nil, err
	}
	if serversNode == nil {
		entry := directMCPEntry("servers:\n" + directMCPServerEntry(name, endpoint, issuer, root, keyDeclaration))
		mcpMap.Values = append(mcpMap.Values, entry)
		return validateDirectMCPDocument(doc)
	}
	servers, ok := serversNode.(*ast.SequenceNode)
	if !ok {
		return nil, errors.New("mcp.servers must be a sequence")
	}
	for _, server := range servers.Values {
		mapping, ok := server.(*ast.MappingNode)
		if !ok {
			return nil, errors.New("mcp.servers entries must be mappings")
		}
		existing, err := uniqueDirectMCPValue(mapping, "name")
		if err != nil {
			return nil, err
		}
		value, ok := existing.(*ast.StringNode)
		if !ok || strings.EqualFold(value.Value, name) {
			return nil, fmt.Errorf("mcp server %q already exists or has an invalid name", name)
		}
	}
	servers.Values = append(servers.Values, directMCPServerNode(name, endpoint, issuer, root, keyDeclaration))
	return validateDirectMCPDocument(doc)
}

// RemoveDirectMCPServer removes one server entry while retaining the rest of the
// parsed document. It intentionally leaves an empty mcp.servers list valid.
func RemoveDirectMCPServer(data []byte, name string) ([]byte, error) {
	if err := ValidateYAML(data); err != nil {
		return nil, errors.New("settings document is invalid")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, errors.New("settings document is invalid or ambiguous")
	}
	mcpNode, err := uniqueDirectMCPValue(doc.Mapping(), "mcp")
	if err != nil || mcpNode == nil {
		return nil, errors.New("mcp server is not configured")
	}
	mcpMap, ok := mcpNode.(*ast.MappingNode)
	if !ok {
		return nil, errors.New("mcp must be a mapping")
	}
	serversNode, err := uniqueDirectMCPValue(mcpMap, "servers")
	if err != nil || serversNode == nil {
		return nil, errors.New("mcp server is not configured")
	}
	servers, ok := serversNode.(*ast.SequenceNode)
	if !ok {
		return nil, errors.New("mcp.servers must be a sequence")
	}
	for i, server := range servers.Values {
		mapping, ok := server.(*ast.MappingNode)
		if !ok {
			return nil, errors.New("mcp.servers entries must be mappings")
		}
		existing, err := uniqueDirectMCPValue(mapping, "name")
		if err != nil {
			return nil, err
		}
		value, ok := existing.(*ast.StringNode)
		if ok && strings.EqualFold(value.Value, name) {
			if len(servers.Values) == 1 {
				if len(mcpMap.Values) == 1 {
					mcpMap.Values = nil
					mcpMap.Values = append(mcpMap.Values, directMCPEntry("mode: global\n"))
				} else {
					for j, entry := range mcpMap.Values {
						if entry.Value == serversNode {
							mcpMap.Values = append(mcpMap.Values[:j], mcpMap.Values[j+1:]...)
							break
						}
					}
				}
			} else {
				servers.Values = append(servers.Values[:i], servers.Values[i+1:]...)
			}
			return validateDirectMCPDocument(doc)
		}
	}
	return nil, fmt.Errorf("mcp server %q is not configured", name)
}

func uniqueDirectMCPValue(mapping *ast.MappingNode, wanted string) (ast.Node, error) {
	var found ast.Node
	for _, entry := range mapping.Values {
		key, ok := entry.Key.(*ast.StringNode)
		if !ok {
			return nil, errors.New("settings document has a non-string mapping key")
		}
		if key.Value == wanted {
			if found != nil {
				return nil, fmt.Errorf("settings document has duplicate %q keys", wanted)
			}
			found = entry.Value
		}
	}
	return found, nil
}

func validateDirectMCPDocument(doc *yamldiag.Document) ([]byte, error) {
	out := []byte(doc.String())
	if err := ValidateYAML(out); err != nil {
		return nil, fmt.Errorf("updated settings document is invalid: %w", err)
	}
	return out, nil
}

func directMCPEntry(text string) *ast.MappingValueNode {
	doc, err := yamldiag.ParseSettingsDocument([]byte(text))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static MCP settings YAML")
	}
	return doc.Mapping().Values[0]
}

func directMCPServerNode(name, endpoint, issuer, root, keyDeclaration string) ast.Node {
	doc, err := yamldiag.ParseSettingsDocument([]byte("servers:\n" + directMCPServerEntry(name, endpoint, issuer, root, keyDeclaration)))
	if err != nil || len(doc.Mapping().Values) != 1 {
		panic("invalid static MCP server YAML")
	}
	return doc.Mapping().Values[0].Value.(*ast.SequenceNode).Values[0]
}

func directMCPDocument(name, endpoint, issuer, root, keyDeclaration string) string {
	return "mcp:\n  servers:\n" + directMCPServerEntry(name, endpoint, issuer, root, keyDeclaration)
}

func directMCPServerEntry(name, endpoint, issuer, root, keyDeclaration string) string {
	q := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	return "    - name: " + q(name) + "\n      url: " + q(endpoint) + "\n      auth:\n        mode: oauth\n        oauth:\n          profile: " + q(strings.ToLower(name)) + "\n          principal: 'local-user'\n          issuer: " + q(issuer) + "\n          client: {mode: dcr, dcr: {}}\n          credentials:\n            mode: local\n            local:\n              root: " + q(root) + "\n              " + keyDeclaration + "\n          network: {additional_origins: [], private_origins: [], max_redirects: 0}\n"
}
