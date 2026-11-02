package cborgen

import (
	"fmt"
	"strconv"
	"strings"
)

// mappingTags parses Go struct-tag syntax without silently accepting duplicate
// codec metadata. Unrelated tags are allowed but do not participate in mapping.
func mappingTags(raw string) (map[string]string, error) {
	tags := map[string]string{}
	for raw != "" {
		raw = strings.TrimLeft(raw, " ")
		if raw == "" {
			break
		}
		colon := strings.IndexByte(raw, ':')
		if colon <= 0 {
			return nil, fmt.Errorf("malformed struct tag")
		}
		key := raw[:colon]
		for _, c := range key {
			if c <= ' ' || c == '"' || c == 0x7f {
				return nil, fmt.Errorf("malformed struct tag key")
			}
		}
		raw = raw[colon+1:]
		if len(raw) == 0 || raw[0] != '"' {
			return nil, fmt.Errorf("malformed struct tag value")
		}
		end := 1
		for end < len(raw) && raw[end] != '"' {
			if raw[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(raw) {
			return nil, fmt.Errorf("unterminated struct tag value")
		}
		value, err := strconv.Unquote(raw[:end+1])
		if err != nil {
			return nil, fmt.Errorf("malformed struct tag value: %w", err)
		}
		raw = raw[end+1:]
		if len(raw) > 0 && raw[0] != ' ' {
			return nil, fmt.Errorf("struct tags require spaces")
		}
		if key == "cbor" || key == "cborlimit" {
			if _, ok := tags[key]; ok {
				return nil, fmt.Errorf("duplicate %s tag", key)
			}
			tags[key] = value
		}
	}
	return tags, nil
}
