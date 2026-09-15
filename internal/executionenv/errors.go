package executionenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrorCode is a stable private-protocol error classification.
type ErrorCode string

// Private-protocol error codes.
const (
	CodeInvalidArgument   ErrorCode = "invalid_argument"
	CodeUnauthenticated   ErrorCode = "unauthenticated"
	CodePermissionDenied  ErrorCode = "permission_denied"
	CodeNotFound          ErrorCode = "not_found"
	CodeAlreadyExists     ErrorCode = "already_exists"
	CodeConflict          ErrorCode = "conflict"
	CodeVersionMismatch   ErrorCode = "version_mismatch"
	CodeNotReady          ErrorCode = "not_ready"
	CodeFenceUnknown      ErrorCode = "fence_unknown"
	CodeResourceExhausted ErrorCode = "resource_exhausted"
	CodeInternal          ErrorCode = "internal"
)

// Error is a bounded structured private-protocol failure.
type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// ErrorResponse is the bounded wire envelope returned for every failed request.
type ErrorResponse struct {
	Error *Error `json:"error"`
}

// DecodeStrict decodes exactly one bounded JSON value without unknown or duplicate fields.
func DecodeStrict(data []byte, dst any) error {
	if len(data) > MaxJSONBody {
		return &Error{Code: CodeResourceExhausted, Message: "request body exceeds limit"}
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return &Error{Code: CodeInvalidArgument, Message: "invalid JSON: " + err.Error()}
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return &Error{Code: CodeInvalidArgument, Message: "invalid JSON: " + err.Error()}
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Code: CodeInvalidArgument, Message: "request must contain exactly one JSON value"}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	if err := walkJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
func walkJSON(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := t.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, ok := seen[k]; ok {
				return fmt.Errorf("duplicate field %q", k)
			}
			seen[k] = struct{}{}
			if err := walkJSON(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case '[':
		for d.More() {
			if err := walkJSON(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	default:
		return errors.New("unexpected delimiter")
	}
}
