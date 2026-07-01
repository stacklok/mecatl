// Package jq is a sandboxed wrapper around github.com/itchyny/gojq that
// evaluates a jq filter against a JSON input and returns the JSON-stringified
// result. It exists to narrow a remote tool's JSON result before it enters
// model context, so a large response does not blow the context budget or get
// truncated into an unparseable blob.
//
// Sandboxing: the [gojq] query is constructed via [gojq.Parse] and run with
// [gojq.Query.RunWithContext] WITHOUT [gojq.WithModuleLoader],
// [gojq.WithInputIter], or [gojq.WithEnvironLoader]. The query therefore
// cannot read files, the environment, or stdin. A context deadline bounds
// pathological compute (e.g. `while(1; .+1)`); a context without a deadline is
// capped at [DefaultTimeout].
package jq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
)

// MaxInputBytes is the cap on the size of the input passed to [Run]. It
// matches session.MaxToolResultBytes (20 MiB): if a remote tool's JSON result
// is larger than this, filtering it in memory is unsafe, and the caller should
// narrow the remote call rather than rely on jq.
const MaxInputBytes = 20 << 20

// MaxOutputBytes is the cap on the JSON-encoded size of the filtered result.
// The filtered subset a caller actually needs should be small; a jq filter
// that produces more than ~100 KiB is too broad and is rejected so it does not
// simply move the context-budget problem from the input to the output.
const MaxOutputBytes = 100_000

// DefaultTimeout is applied when the context passed to [Run] carries no
// deadline, so a pathological filter cannot run unbounded.
const DefaultTimeout = 5 * time.Second

// Run evaluates a jq filter against a JSON input and returns the
// JSON-stringified result.
//
// Output shape: a jq filter can yield multiple values (e.g. `.items[]` yields
// one value per element). [Run] collects all yielded values and returns:
//   - exactly one value: that value, JSON-encoded (so a string result is
//     returned as a quoted JSON string, e.g. `"hello"`),
//   - more than one value: a JSON array wrapping every value, JSON-encoded,
//   - zero values: the literal `null`.
//
// The returned string is always JSON-shaped: every yielded value is passed
// through [json.Marshal], so a single gojq string output is quoted rather than
// returned raw. Callers parsing the result with [json.Unmarshal] get a
// consistent shape.
//
// Bounds:
//   - input <= [MaxInputBytes] (loud error if exceeded — don't load huge input),
//   - output <= [MaxOutputBytes] (loud error if the filtered result is huge),
//   - ctx should carry a deadline; if none, [DefaultTimeout] is applied.
//
// All error messages are loud and self-describing (they are surfaced to a
// model/operator), wrapping the underlying cause where useful.
func Run(ctx context.Context, filter string, input []byte) (string, error) {
	if len(input) > MaxInputBytes {
		return "", fmt.Errorf("jq input exceeded the %d-byte cap (%d bytes); narrow the call rather than filtering in memory", MaxInputBytes, len(input))
	}

	// Bound pathological compute when the caller supplied no deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return "", fmt.Errorf("jq parse error: %w", err)
	}

	// gojq takes the input as an `any`, not raw bytes, so unmarshal first. A
	// non-JSON input fails loudly here rather than inside the iterator.
	var inputAny any
	if err := json.Unmarshal(input, &inputAny); err != nil {
		return "", fmt.Errorf("input is not valid JSON: %w", err)
	}

	iter := query.RunWithContext(ctx, inputAny)

	var outputs []any
	accumulated := 0
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			if errors.Is(err, context.DeadlineExceeded) {
				return "", fmt.Errorf("jq filter timed out (possible pathological input): %w", err)
			}
			return "", fmt.Errorf("jq filter failed: %w", err)
		}

		enc, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		accumulated += len(enc)
		if accumulated > MaxOutputBytes {
			return "", fmt.Errorf("jq output exceeded the %d-byte cap (filter too broad?)", MaxOutputBytes)
		}
		outputs = append(outputs, v)
	}

	switch len(outputs) {
	case 0:
		return "null", nil
	case 1:
		b, err := json.Marshal(outputs[0])
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		return string(b), nil
	default:
		b, err := json.Marshal(outputs)
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		return string(b), nil
	}
}
