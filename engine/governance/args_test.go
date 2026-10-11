package governance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// toolArgsSeeds are argument encodings covering canonical objects, JSON key
// escapes, repeated keys, case and Unicode case-folding variants, nesting and
// non-object payloads. They seed the decode-agreement fuzzer and the tables.
var toolArgsSeeds = []string{
	``,
	`{}`,
	`null`,
	`[]`,
	`"text"`,
	`{bad`,
	`{"command":"ls"} {"command":"pwd"}`,
	`{"command":"go test ./..."}`,
	`{"command":"go test ./...","timeout_ms":5000,"temp_scope":"managed"}`,
	`{"command":"ls"}`,
	`{"command":"ls","cmd":"pwd"}`,
	`{"cmd":"pwd"}`,
	`{"command":"","cmd":"pwd"}`,
	`{"command":7,"cmd":"pwd"}`,
	`{"command":null}`,
	`{"command":"ls","command":"pwd"}`,
	`{"command":"ls","\u0063ommand":"pwd"}`,
	`{"Command":"pwd"}`,
	`{"command":"ls","COMMAND":"pwd"}`,
	`{"COMMAND":"pwd","command":"ls"}`,
	`{"command":"ls","Temp_Scope":"system"}`,
	`{"command":"ls","Timeout_MS":1}`,
	`{"path":"a"}`,
	`{"path":"a","Path":"b"}`,
	`{"file_path":"a"}`,
	`{"FILE_PATH":"a"}`,
	`{"pattern":"*.go","path":"src"}`,
	`{"url":"https://example.com","URL":"https://example.org"}`,
	`{"query":"q","Query":"r"}`,
	`{"source":"a","destination":"b"}`,
	`{"source":"a","ſource":"b","destination":"c"}`,
	`{"ſource":"b","destination":"c"}`,
	`{"key":"a","Key":"b"}`,
	`{"server":"s","tool":"t","Tool":"u"}`,
	`{"edits":[{"old_string":"a","old_string":"b"}]}`,
	`{"meta":{"x":1,"x":2}}`,
	`{"meta":{"x":1,"X":2}}`,
	`{"filePath":"a","FilePath":"b"}`,
	`{"label":"x","Label":"y"}`,
	`{"a\\\"b":1,"a\\\"b":2}`,
	`{"s":"{\"command\":1}","command":"ls"}`,
	`{"bad\xffkey":1}`,
}

// TestCheckToolArgsClassification pins which argument objects are ambiguous.
func TestCheckToolArgsClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args      string
		ambiguous bool
	}{
		{``, false},
		{`{}`, false},
		{`null`, false},
		{`[]`, false},
		{`{bad`, false},
		{`{"command":"ls"}`, false},
		{`{"\u0063ommand":"ls"}`, false},
		{`{"command":"ls","cmd":"pwd"}`, false},
		{`{"filePath":"a"}`, false},
		{`{"meta":{"x":1,"X":2}}`, false},
		{`{"command":"ls","command":"pwd"}`, true},
		{`{"command":"ls","\u0063ommand":"pwd"}`, true},
		{`{"Command":"pwd"}`, true},
		{`{"command":"ls","COMMAND":"pwd"}`, true},
		{`{"command":"ls","Temp_Scope":"system"}`, true},
		{`{"path":"a","Path":"b"}`, true},
		{`{"FILE_PATH":"a"}`, true},
		{`{"url":"https://example.com","URL":"https://example.org"}`, true},
		{`{"source":"a","ſource":"b","destination":"c"}`, true},
		{`{"ſource":"b","destination":"c"}`, true},
		{`{"key":"a","Key":"b"}`, true},
		{`{"server":"s","tool":"t","Tool":"u"}`, true},
		{`{"edits":[{"old_string":"a","old_string":"b"}]}`, true},
		{`{"meta":{"x":1,"x":2}}`, true},
		{`{"filePath":"a","FilePath":"b"}`, true},
		{`{"label":"x","Label":"y"}`, true},
	} {
		err := CheckToolArgs(json.RawMessage(tc.args))
		if got := errors.Is(err, ErrAmbiguousToolArgs); got != tc.ambiguous {
			t.Errorf("CheckToolArgs(%s) = %v, want ambiguous=%v", tc.args, err, tc.ambiguous)
		}
	}
}

// TestDecodeToolArgsRequiresExactFieldSpelling pins that a key naming a tagged
// destination field only under case folding is refused, while untagged fields
// and unrelated keys keep decoding.
func TestDecodeToolArgsRequiresExactFieldSpelling(t *testing.T) {
	t.Parallel()
	type embedded struct {
		Offset int `json:"offset"`
	}
	type dst struct {
		embedded
		Limit    int `json:"limit"`
		Untagged string
		Skipped  string `json:"-"`
	}
	for _, tc := range []struct {
		args string
		ok   bool
	}{
		{`{"limit":1,"offset":2}`, true},
		{`{"untagged":"x"}`, true},
		{`{"other":1}`, true},
		{`{"Limit":1}`, false},
		{`{"OFFSET":2}`, false},
	} {
		var d dst
		err := DecodeToolArgs(json.RawMessage(tc.args), &d)
		if (err == nil) != tc.ok {
			t.Errorf("DecodeToolArgs(%s) = %v, want ok=%v", tc.args, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrAmbiguousToolArgs) {
			t.Errorf("DecodeToolArgs(%s) error %v does not wrap ErrAmbiguousToolArgs", tc.args, err)
		}
	}
}

// TestEvaluatorDeniesAmbiguousArgs pins the evaluator's own check: an ambiguous
// argument object is denied before any rule, plan mode or learned allow, and is
// never learnable.
func TestEvaluatorDeniesAmbiguousArgs(t *testing.T) {
	t.Parallel()
	e := NewEvaluator([]Rule{{Scope: ScopeCLI, Effect: Allow}})
	for _, tc := range []struct{ tool, args string }{
		{"Shell", `{"command":"ls","COMMAND":"pwd"}`},
		{"Read", `{"path":"a","path":"b"}`},
		{"Copy", `{"source":"a","destination":"b","Destination":"c"}`},
		{"mcp__srv__fetch", `{"url":"https://example.com","Url":"https://example.org"}`},
	} {
		d := e.EvaluateWith(tc.tool, json.RawMessage(tc.args), false, []Rule{{Scope: ScopeUser, Tool: tc.tool, Effect: Allow}})
		if d.Effect != Deny || !strings.Contains(d.Reason, "ambiguous tool arguments") {
			t.Errorf("%s %s: decision %+v, want an ambiguous-arguments deny", tc.tool, tc.args, d)
		}
		if _, ok := e.LearnableRule(tc.tool, json.RawMessage(tc.args)); ok {
			t.Errorf("%s %s: ambiguous arguments produced a learnable rule", tc.tool, tc.args)
		}
	}
}

// shellExecArgs mirrors the Shell tool's argument struct: the fields its
// execution decodes.
type shellExecArgs struct {
	Command    string `json:"command"`
	TimeoutMS  int    `json:"timeout_ms"`
	Background bool   `json:"background"`
	TempScope  string `json:"temp_scope"`
}

// pathExecArgs mirrors the single-path built-in tools' argument struct.
type pathExecArgs struct {
	Path string `json:"path"`
}

// copyExecArgs mirrors the Copy/Move tools' argument struct.
type copyExecArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// assertPolicyMatchesExecution checks, for one argument payload, that whenever
// the execution decode accepts the payload, the policy extraction reads the
// same value; and that an ambiguous payload is refused by both.
func assertPolicyMatchesExecution(t *testing.T, args string) {
	t.Helper()
	raw := json.RawMessage(args)
	ambiguous := CheckToolArgs(raw) != nil

	// The key scanner sees every top-level key encoding/json sees, once.
	var object map[string]json.RawMessage
	if !ambiguous && json.Unmarshal(raw, &object) == nil && object != nil {
		keys, err := scanToolArgs(raw)
		if err != nil || len(keys) != len(object) {
			t.Errorf("%q: scanned keys %q (err=%v), encoding/json keys %d", args, keys, err, len(object))
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				t.Errorf("%q: scanned key %q that encoding/json does not decode", args, key)
			}
		}
	}

	var shell shellExecArgs
	if err := DecodeToolArgs(raw, &shell); err == nil {
		if ambiguous {
			t.Errorf("%q: Shell decode accepted an ambiguous object", args)
		}
		cmd, ok := ShellCommandFromArgs(raw)
		if strings.TrimSpace(shell.Command) != "" && (!ok || cmd != shell.Command) {
			t.Errorf("%q: policy command %q (ok=%v), executed command %q", args, cmd, ok, shell.Command)
		}
	} else if cmd, ok := ShellCommandFromArgs(raw); ambiguous && ok {
		t.Errorf("%q: policy read command %q from an ambiguous object", args, cmd)
	}

	var path pathExecArgs
	if err := DecodeToolArgs(raw, &path); err == nil && path.Path != "" {
		if got := nonShellPattern("Read", raw); got != path.Path {
			t.Errorf("%q: policy path %q, executed path %q", args, got, path.Path)
		}
	} else if ambiguous && nonShellPattern("Read", raw) != "" {
		t.Errorf("%q: policy read a path from an ambiguous object", args)
	}

	var operands copyExecArgs
	if err := DecodeToolArgs(raw, &operands); err == nil && operands.Source != "" && operands.Destination != "" {
		got, ok := copyMovePatterns(raw)
		if !ok || len(got) != 2 || got[0] != operands.Source || got[1] != operands.Destination {
			t.Errorf("%q: policy operands %q (ok=%v), executed %q -> %q", args, got, ok, operands.Source, operands.Destination)
		}
	} else if _, ok := copyMovePatterns(raw); ambiguous && ok {
		t.Errorf("%q: policy read operands from an ambiguous object", args)
	}
}

// TestPolicyExtractionMatchesExecutionDecode runs the decode-agreement property
// over the seed encodings.
func TestPolicyExtractionMatchesExecutionDecode(t *testing.T) {
	t.Parallel()
	for _, args := range toolArgsSeeds {
		assertPolicyMatchesExecution(t, args)
	}
}

// FuzzPolicyExtractionMatchesExecutionDecode asserts the decode-agreement
// property for arbitrary argument payloads.
func FuzzPolicyExtractionMatchesExecutionDecode(f *testing.F) {
	for _, seed := range toolArgsSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, args string) {
		assertPolicyMatchesExecution(t, args)
	})
}
