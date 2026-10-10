package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

//nolint:gocyclo // Strict JSON parsing keeps every variant boundary at one public entry point.
func executionFromJSON(raw json.RawMessage) (ExecutionSelection, error) {
	if len(raw) == 0 {
		return ExecutionSelection{}, nil
	}
	decoderChoices := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoderChoices.Token()
	if err != nil || token != json.Delim('{') {
		return ExecutionSelection{}, ErrInvalidArgument
	}
	choices := make(map[string]json.RawMessage)
	for decoderChoices.More() {
		key, err := decoderChoices.Token()
		if err != nil {
			return ExecutionSelection{}, ErrInvalidArgument
		}
		name, ok := key.(string)
		if !ok {
			return ExecutionSelection{}, ErrInvalidArgument
		}
		if _, duplicate := choices[name]; duplicate {
			return ExecutionSelection{}, ErrInvalidArgument
		}
		var value json.RawMessage
		if err := decoderChoices.Decode(&value); err != nil {
			return ExecutionSelection{}, ErrInvalidArgument
		}
		choices[name] = value
	}
	if _, err := decoderChoices.Token(); err != nil || len(choices) != 1 {
		return ExecutionSelection{}, fmt.Errorf("%w: execution requires exactly one variant", ErrInvalidArgument)
	}
	if err := decoderChoices.Decode(&struct{}{}); err != io.EOF {
		return ExecutionSelection{}, ErrInvalidArgument
	}
	for name, data := range choices {
		switch name {
		case "none":
			var empty map[string]json.RawMessage
			if err := json.Unmarshal(data, &empty); err == nil && empty != nil && len(empty) == 0 {
				return ExecutionSelection{Kind: PlacementSelectorNoFS}, nil
			}
		case "template":
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 2 {
				break
			}
			idRaw, hasID := fields["id"]
			revisionRaw, hasRevision := fields["revision"]
			if !hasID || !hasRevision {
				break
			}
			var id, revision string
			if err := json.Unmarshal(idRaw, &id); err != nil {
				break
			}
			if err := json.Unmarshal(revisionRaw, &revision); err != nil {
				break
			}
			e := ExecutionSelection{Kind: PlacementSelectorTemplate, ID: id, Revision: revision}
			if _, _, err := e.placement(); err == nil {
				return e, nil
			}
		}
	}
	return ExecutionSelection{}, fmt.Errorf("%w: invalid execution variant", ErrInvalidArgument)
}
