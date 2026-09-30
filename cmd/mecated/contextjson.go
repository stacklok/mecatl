package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Reject ambiguous evidence before unmarshalling: encoding/json otherwise keeps
// the last value of a repeated key, which can silently change selected rows.
func contextUniqueJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := contextJSONValue(dec, 0); err != nil {
		return errors.New("context: ambiguous or malformed JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("context: multiple JSON values")
	}
	return nil
}
func contextJSONValue(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("unexpected JSON delimiter")
	}
	seen := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
		}
		if err := contextJSONValue(dec, depth+1); err != nil {
			return err
		}
	}
	closing, err := dec.Token()
	if err != nil {
		return err
	}
	if delim == '{' && closing != json.Delim('}') || delim == '[' && closing != json.Delim(']') {
		return errors.New("unclosed JSON container")
	}
	return nil
}
