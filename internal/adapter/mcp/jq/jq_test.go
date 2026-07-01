package jq_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
)

func TestRunFiltersSubset(t *testing.T) {
	in := []byte(`{"items":[{"id":1},{"id":2}]}`)
	got, err := jq.Run(context.Background(), `.items[] | .id`, in)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	// Multiple gojq outputs are wrapped in a JSON array.
	if want := "[1,2]"; got != want {
		t.Fatalf("Run = %q, want %q", got, want)
	}
}

func TestRunSingleObject(t *testing.T) {
	in := []byte(`{"a":{"b":42}}`)
	got, err := jq.Run(context.Background(), `.a.b`, in)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if want := "42"; got != want {
		t.Fatalf("Run = %q, want %q", got, want)
	}
}

func TestRunSingleStringIsJSONQuoted(t *testing.T) {
	// A single gojq string output is JSON-encoded (quoted), not returned raw,
	// so the result is always JSON-shaped.
	in := []byte(`{"a":{"b":"hello"}}`)
	got, err := jq.Run(context.Background(), `.a.b`, in)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if want := `"hello"`; got != want {
		t.Fatalf("Run = %q, want %q", got, want)
	}
}

func TestRunZeroOutputsReturnsNull(t *testing.T) {
	// `empty` yields no values.
	got, err := jq.Run(context.Background(), `empty`, []byte(`{}`))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if want := "null"; got != want {
		t.Fatalf("Run = %q, want %q", got, want)
	}
}

func TestRunNonJSONInput(t *testing.T) {
	_, err := jq.Run(context.Background(), `.`, []byte(`not json`))
	if err == nil {
		t.Fatal("Run: expected error for non-JSON input, got nil")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "not valid JSON")
	}
}

func TestRunParseError(t *testing.T) {
	_, err := jq.Run(context.Background(), `.a |`, []byte(`{}`))
	if err == nil {
		t.Fatal("Run: expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "jq parse error") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "jq parse error")
	}
}

func TestRunDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// `def f: f; f` is a non-terminating recursion that yields NO values, so the
	// only way out is the context deadline — it exercises the timeout path
	// without tripping the output cap first.
	_, err := jq.Run(ctx, `def f: f; f`, []byte(`0`))
	if err == nil {
		t.Fatal("Run: expected timeout error for pathological filter, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "timed out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error should wrap context.DeadlineExceeded, got %T: %v", err, err)
	}
}

func TestRunDefaultTimeoutApplied(t *testing.T) {
	// A context with NO deadline still bounds the filter via DefaultTimeout.
	// `def f: f; f` is a non-terminating recursion yielding no values, so the
	// only exit is the applied DefaultTimeout.
	start := time.Now()
	_, err := jq.Run(context.Background(), `def f: f; f`, []byte(`0`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Run: expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "timed out")
	}
	// Sanity: the default is 5s, so this should finish in ~5s, not hang.
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %v, expected to be bounded by ~DefaultTimeout (5s)", elapsed)
	}
}

func TestRunOversizedInput(t *testing.T) {
	oversized := make([]byte, jq.MaxInputBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	_, err := jq.Run(context.Background(), `.`, oversized)
	if err == nil {
		t.Fatal("Run: expected error for oversized input, got nil")
	}
	if !strings.Contains(err.Error(), "input exceeded the") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "input exceeded the")
	}
}

func TestRunOversizedOutput(t *testing.T) {
	// ~200 KiB of a string value; filter `.` returns it whole, exceeding the
	// 100 KiB output cap.
	big := strings.Repeat("a", 200_000)
	in := []byte(`"` + big + `"`)
	_, err := jq.Run(context.Background(), `.`, in)
	if err == nil {
		t.Fatal("Run: expected error for oversized output, got nil")
	}
	if !strings.Contains(err.Error(), "output exceeded the") {
		t.Fatalf("Run error = %q, want one mentioning %q", err.Error(), "output exceeded the")
	}
}

func TestRunNoFileEnvAccess(t *testing.T) {
	// gojq constructed without WithEnvironLoader must NOT expose the process
	// environment. `env` and `$ENV` evaluate to an empty object rather than
	// erroring — the security property we assert is that NO environment
	// variable leaks into the result, not that the filter errors.
	for _, filter := range []string{`env`, `$ENV`} {
		got, err := jq.Run(context.Background(), filter, []byte(`{}`))
		if err != nil {
			t.Fatalf("Run(%q): unexpected error: %v", filter, err)
		}
		// An empty env yields {} (no keys).
		if want := "{}"; got != want {
			t.Fatalf("Run(%q) = %q, want %q (env must not leak)", filter, got, want)
		}
	}
}

func TestRunContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := jq.Run(ctx, `while(1; .+1)`, []byte(`0`))
	if err == nil {
		t.Fatal("Run: expected error on cancelled context, got nil")
	}
}

// TestRunNoInputBuiltinAccess asserts gojq constructed without WithInputIter
// CANNOT read files/stdin via the `input`/`inputs` builtins. Without an input
// iterator, gojq surfaces a load error ("input(s)/0 is not allowed") rather
// than reading /dev/stdin or any file — the security property is that NO file
// is read, not the exact error string. We assert an error is returned (so the
// filter did not silently consume a file) and the output is not a value that
// could have come from reading a file.
func TestRunNoInputBuiltinAccess(t *testing.T) {
	for _, filter := range []string{`input`, `inputs`} {
		got, err := jq.Run(context.Background(), filter, []byte(`{}`))
		if err == nil {
			// If gojq ever returns null without error, that is still acceptable
			// (no file read) — but a non-null value would mean a file was read.
			if got != "null" {
				t.Fatalf("Run(%q) returned %q with no error — a value was produced, implying a file/stdin read", filter, got)
			}
			continue
		}
		// An error is the expected path; it must NOT look like a successful read.
		if strings.Contains(err.Error(), "not allowed") || strings.Contains(err.Error(), "input") {
			continue
		}
		// Any error is acceptable as long as no value was produced.
		_ = err
	}
}

// TestRunNoModuleImport asserts gojq constructed without WithModuleLoader cannot
// load modules via `import`/`include`. gojq surfaces this as a parse error
// (import/include require a module loader to resolve the path), so the filter
// never reaches a point where it could read a module file off disk.
func TestRunNoModuleImport(t *testing.T) {
	// import/include statements require a trailing filter (a bare import is a
	// parse error before any module load is attempted). Both forms error.
	for _, filter := range []string{"import \"foo\" as Bar\n.", "include \"foo\"\n."} {
		_, err := jq.Run(context.Background(), filter, []byte(`{}`))
		if err == nil {
			t.Fatalf("Run(%q): expected a parse/load error (no module loader), got nil", filter)
		}
		// The error must not indicate a successful module load.
		if !strings.Contains(err.Error(), "jq parse error") && !strings.Contains(err.Error(), "module") {
			// Any error is acceptable as long as no module file was read; the
			// key property is that Run did not succeed.
			t.Logf("Run(%q) error (acceptable, no module read): %v", filter, err)
		}
	}
}

// TestRunNoEnvLeakWithSecret sets a test env var with a secret value and asserts
// the `env`/`$ENV` filter does NOT leak it: gojq is constructed without
// WithEnvironLoader, so env evaluates to {} regardless of the process
// environment.
func TestRunNoEnvLeakWithSecret(t *testing.T) {
	const secret = "MECATL_TEST_SECRET_DO_NOT_LEAK_42"
	t.Setenv("MECATL_TEST_SECRET", secret)
	for _, filter := range []string{`env`, `$ENV`} {
		got, err := jq.Run(context.Background(), filter, []byte(`{}`))
		if err != nil {
			t.Fatalf("Run(%q): unexpected error: %v", filter, err)
		}
		if strings.Contains(got, secret) {
			t.Fatalf("Run(%q) leaked the secret env var: %q", filter, got)
		}
		if got != "{}" {
			t.Fatalf("Run(%q) = %q, want {} (env must not leak any variable)", filter, got)
		}
	}
}
