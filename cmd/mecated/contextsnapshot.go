package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/stacklok/mecatl/internal/adapter/tokenizer"
)

// scanMCPSnapshot accepts only an offline MCP tools/list result. It inventories
// measurements, never connects to the server or exposes tool bodies.
func scanMCPSnapshot(path string, remaining *int, counter *tokenizer.Counter) ([]contextOccurrence, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("context scan: cannot open regular non-symlink MCP snapshot")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > contextMaxInput || info.Size() > int64(*remaining) {
		return nil, errors.New("context scan: MCP snapshot must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(min(contextMaxInput, *remaining)+1)))
	if err != nil || len(data) > contextMaxInput || len(data) > *remaining {
		return nil, errors.New("context scan: MCP snapshot read budget exceeded")
	}
	*remaining -= len(data)
	if err := contextUniqueJSON(data); err != nil {
		return nil, errors.New("context scan: malformed MCP snapshot")
	}
	var snapshot struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snapshot); err != nil || len(snapshot.Tools) > contextMaxEntries {
		return nil, errors.New("context scan: malformed or oversized MCP snapshot")
	}
	used, rawUsed := map[string]bool{}, map[string]bool{}
	out := make([]contextOccurrence, 0, len(snapshot.Tools))
	for i, tool := range snapshot.Tools {
		if tool.Name == "" || rawUsed[tool.Name] || len(tool.InputSchema) == 0 || !jsonObject(tool.InputSchema) {
			return nil, errors.New("context scan: malformed or duplicate MCP tool")
		}
		rawUsed[tool.Name] = true
		name := tool.Name
		if !contextLabel.MatchString(name) {
			name = "tool-" + contextThreeDigits(i)
		}
		for used[name] {
			name += "x"
		}
		used[name] = true
		docBytes, schemaBytes := len(tool.Description), len(tool.InputSchema)
		out = append(out, contextOccurrence{
			Source:                "mcp-snapshot",
			Name:                  name,
			Bytes:                 contextPtr(docBytes + schemaBytes),
			DocBytes:              contextPtr(docBytes),
			SchemaBytes:           contextPtr(schemaBytes),
			EstimatedDocTokens:    contextPtr(counter.Count(tool.Description)),
			EstimatedSchemaTokens: contextPtr(counter.Count(string(tool.InputSchema))),
			EstimatedTokens:       contextPtr(counter.Count(tool.Description + string(tool.InputSchema))),
			Status:                "snapshot candidate; runtime admission unknown",
		})
	}
	return out, nil
}

func contextThreeDigits(n int) string {
	return fmt.Sprintf("%03d", n)
}

func jsonObject(data []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(data, &v) == nil && v != nil
}
