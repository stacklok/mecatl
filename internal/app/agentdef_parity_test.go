package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/agentdef"
	"github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestAgentDefParsersAgree pins that the harness's definition parser (the
// engine's agentfs) and the identity broker's (agentdef) read every file the
// same way: the same fields, the same adjustment notes, and the same
// rejections. The harness runs agentfs's result while the broker records
// agentdef's, so a difference would make the broker record a definition other
// than the one that runs. Remove this test when agentfs parses with agentdef.
func TestAgentDefParsersAgree(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("世", agentdef.MaxBodyBytes)
	cases := map[string]string{
		"full": "---\nname: code-reviewer\ndescription: Reviews a diff.\ntools: [Read, Grep, Glob]\n" +
			"disallowedTools: [Write]\nmodel: sonnet\nprovider:  openrouter\npermissionMode: plan\n" +
			"maxTurns: 9\nmaxToolCalls: 25\ncolor: blue\nmemory: PROJECT\n---\nYou review code.\n",
		"string lists":   "---\nname: x\ndescription: d\ntools: Read, Edit Grep\ndisallowedTools: \"Write, Edit\"\nskills: a, b\n---\nbody",
		"scalar tools":   "---\nname: n\ndescription: d\ntools: true\n---\n",
		"legacy bash":    "---\nname: legacy\ndescription: d\ntools: [Bash, Read]\ndisallowedTools: [Bash]\n---\n",
		"hooks":          "---\nname: h\ndescription: d\nhooks:\n  PreToolUse: \"echo pre\"\n  PostToolUse: \"  \"\n  \"\": \"x\"\n---\n",
		"mcp servers":    "---\nname: ops\ndescription: d\nmcpServers:\n  - github\n  - name: inline\n    url: https://example.test/mcp\n    headers:\n      Authorization: Bearer tok\n      Empty: \"\"\n  - name: bare\n  - url: https://nameless.test\n  - name: stdio\n    command: ./srv\n  - name: typed\n    type: stdio\n---\n",
		"mcp string":     "---\nname: m\ndescription: d\nmcpServers: \"a, b\"\n---\n",
		"bad memory":     "---\nname: m\ndescription: d\nmemory: local\n---\n",
		"long body":      "---\nname: l\ndescription: d\n---\n" + long,
		"long desc":      "---\nname: l\ndescription: " + strings.Repeat("d", agentdef.MaxDescriptionBytes+1) + "\n---\n",
		"crlf bom":       "\ufeff---\r\nname: c\r\ndescription: d\r\n---\r\nbody\r\n",
		"unknown key":    "---\nname: u\ndescription: d\nfuture: x\n---\n",
		"no frontmatter": "just a body",
		"no name":        "---\ndescription: d\n---\n",
		"blank name":     "---\nname: \"  \"\ndescription: d\n---\n",
		"no description": "---\nname: n\n---\n",
		"bad yaml":       "---\nname: [unterminated\n---\n",
		"bad header":     "---\nname: b\ndescription: d\nmcpServers:\n  - name: r\n    url: https://x\n    headers:\n      Authorization: [secret]\n---\n",
		"bad list":       "---\nname: b\ndescription: d\ntools:\n  a: b\n---\n",
	}
	for _, doc := range semanticMatrixAgentDocuments(t) {
		cases["semantic matrix: "+doc.name] = doc.document
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "def.md")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			discovered, skips, err := agentfs.DirSource{Dir: dir}.Agents(t.Context())
			if err != nil {
				t.Fatalf("agentfs: %v", err)
			}
			var harnessReason string
			var harnessNotes []string
			for _, s := range skips {
				if s.Fatal {
					harnessReason = s.Reason
				} else {
					harnessNotes = append(harnessNotes, s.Reason)
				}
			}

			def, notes, err := agentdef.Parse([]byte(content))
			brokerReason := ""
			if err != nil {
				brokerReason = err.Error()
			}
			if brokerReason != harnessReason {
				t.Fatalf("rejection differs: agentdef %q, agentfs %q", brokerReason, harnessReason)
			}
			if !reflect.DeepEqual(notes, harnessNotes) {
				t.Errorf("notes differ:\nagentdef %q\nagentfs  %q", notes, harnessNotes)
			}
			if harnessReason != "" {
				return
			}
			if len(discovered) != 1 {
				t.Fatalf("agentfs kept %d definitions, want 1", len(discovered))
			}
			if got, want := toolAgentDef(def), discovered[0].Def; !reflect.DeepEqual(got, want) {
				t.Errorf("definitions differ:\nagentdef %+v\nagentfs  %+v", got, want)
			}
		})
	}
}

// toolAgentDef converts an agentdef definition to the engine's type, with the
// explicit origin a directory source stamps.
func toolAgentDef(d agentdef.Definition) tool.AgentDef {
	var servers []tool.AgentMCPServer
	for _, s := range d.MCPServers {
		servers = append(servers, tool.AgentMCPServer{Name: s.Name, URL: s.URL, Headers: s.Headers})
	}
	return tool.AgentDef{
		Name:            d.Name,
		Description:     d.Description,
		Tools:           d.Tools,
		DisallowedTools: d.DisallowedTools,
		Model:           d.Model,
		Provider:        d.Provider,
		PermissionMode:  d.PermissionMode,
		MaxTurns:        d.MaxTurns,
		MaxToolCalls:    d.MaxToolCalls,
		Color:           d.Color,
		Skills:          d.Skills,
		MCPServers:      servers,
		Hooks:           d.Hooks,
		Memory:          d.Memory,
		Body:            d.Body,
		Origin:          tool.AgentOriginExplicit,
	}
}

type matrixDocument struct{ name, document string }

// semanticMatrixAgentDocuments returns the engine's YAML compatibility
// fixtures owned by the agentfs reader.
func semanticMatrixAgentDocuments(t *testing.T) []matrixDocument {
	t.Helper()
	data, err := os.ReadFile("../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatalf("read semantic matrix: %v", err)
	}
	var matrix struct {
		Cases []struct {
			Name     string            `yaml:"name"`
			Document string            `yaml:"document"`
			Readers  map[string]string `yaml:"readers"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatalf("parse semantic matrix: %v", err)
	}
	var docs []matrixDocument
	for _, c := range matrix.Cases {
		if _, ok := c.Readers["agentfs"]; ok {
			docs = append(docs, matrixDocument{c.Name, c.Document})
		}
	}
	if len(docs) == 0 {
		t.Fatal("semantic matrix declares no agentfs case")
	}
	return docs
}
