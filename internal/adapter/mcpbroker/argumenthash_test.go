package mcpbroker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
)

// hashVectors is the shared argument hash vectors file,
// testdata/argumenthash/vectors.json: a byte-for-byte copy of ToolHive's
// canonical copy (pkg/vmcp/callbinding/testdata/argumenthash/vectors.json),
// which the vMCP tests against. argumentsSHA256 delegates to the vMCP's own
// hash; these tests pin its absent-is-{} rule and its refusals against them,
// and TestHashVectorsAreToolHives notices when ToolHive changes its copy. The expected canonical strings come from RFC 8785 and
// hand-checked cases; sha256 is computed over the expected string, not by
// this package.
type hashVectors struct {
	Vectors []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Canonical string          `json:"canonical"`
		SHA256    string          `json:"sha256"`
		// GatewayRefuses marks arguments the vMCP refuses before hashing; the
		// hash must agree anyway.
		GatewayRefuses bool `json:"gateway_refuses"`
	} `json:"vectors"`
	Refusals []hashRefusal `json:"refusals"`
}

// hashRefusal is arguments the hash must refuse, as text or, when they are
// not valid UTF-8, as base64.
type hashRefusal struct {
	Name   string  `json:"name"`
	Text   *string `json:"arguments_text"`
	Base64 *string `json:"arguments_base64"`
}

func (r hashRefusal) arguments(t *testing.T) json.RawMessage {
	t.Helper()
	switch {
	case r.Text != nil && r.Base64 == nil:
		return json.RawMessage(*r.Text)
	case r.Base64 != nil && r.Text == nil:
		raw, err := base64.StdEncoding.DecodeString(*r.Base64)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	t.Fatalf("refusal %q holds neither or both of arguments_text and arguments_base64", r.Name)
	return nil
}

const hashVectorsFile = "testdata/argumenthash/vectors.json"

func readHashVectors(t *testing.T) hashVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(hashVectorsFile))
	if err != nil {
		t.Fatal(err)
	}
	var file hashVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestHashVectorsAreToolHives checks the vectors file is ToolHive's
// canonical copy from the ToolHive module this build uses, byte for byte.
// After a ToolHive bump that changes them, copy the file again.
func TestHashVectorsAreToolHives(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/stacklok/toolhive").Output()
	if err != nil {
		t.Fatalf("locate the ToolHive module: %v", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatal("the ToolHive module has no directory in the module cache")
	}
	canonical, err := os.ReadFile(filepath.Join(dir, "pkg", "vmcp", "callbinding", "testdata", "argumenthash", "vectors.json"))
	if err != nil {
		t.Fatalf("read ToolHive's canonical vectors: %v", err)
	}
	ours, err := os.ReadFile(filepath.FromSlash(hashVectorsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, canonical) {
		t.Fatalf("%s differs from ToolHive's canonical copy in %s; copy it again", hashVectorsFile, dir)
	}
}

func TestArgumentsSHA256Vectors(t *testing.T) {
	file := readHashVectors(t)
	if len(file.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range file.Vectors {
		if v.GatewayRefuses {
			continue // TestArgumentsSHA256RefusesWhatTheGatewayRefuses
		}
		t.Run(v.Name, func(t *testing.T) {
			sent, sum, err := argumentsSHA256(v.Arguments)
			if err != nil {
				t.Fatalf("argumentsSHA256(%s): %v", v.Arguments, err)
			}
			if sum != v.SHA256 {
				t.Errorf("sum = %s, want %s", sum, v.SHA256)
			}
			wantSent := string(v.Arguments)
			if len(v.Arguments) == 0 || string(v.Arguments) == "null" {
				wantSent = "{}"
			}
			if string(sent) != wantSent {
				t.Errorf("sent = %s, want %s", sent, wantSent)
			}
		})
	}
}

// TestArgumentsSHA256RefusesWhatTheGatewayRefuses: every shared vector the
// vMCP refuses with -32600 before hashing (gateway_refuses: member names
// equal under case folding) is refused here too, before anything is
// authorized, although its hash still agrees with the vMCP's.
func TestArgumentsSHA256RefusesWhatTheGatewayRefuses(t *testing.T) {
	file := readHashVectors(t)
	n := 0
	for _, v := range file.Vectors {
		if !v.GatewayRefuses {
			continue
		}
		n++
		t.Run(v.Name, func(t *testing.T) {
			if sent, sum, err := argumentsSHA256(v.Arguments); !errors.Is(err, errCaseFoldedMembers) || sent != nil || sum != "" {
				t.Fatalf("argumentsSHA256(%s) = %s, %q, %v; want errCaseFoldedMembers", v.Arguments, sent, sum, err)
			}
			if sum, err := callbinding.ArgumentsSHA256(v.Arguments); err != nil || sum != v.SHA256 {
				t.Errorf("the vMCP's hash = %q, %v; want %s", sum, err, v.SHA256)
			}
		})
	}
	if n == 0 {
		t.Fatal("no gateway_refuses vectors")
	}
}

// TestHasCaseFoldedMembersMatchesTheVMCP checks hasCaseFoldedMembers
// against the vMCP's own request parser, mcp.DecodeMessage, which refuses a
// tools/call whose arguments it reports, and only those.
func TestHasCaseFoldedMembersMatchesTheVMCP(t *testing.T) {
	for _, c := range []struct {
		args string
		want bool
	}{
		{`{"a":1,"b":2}`, false},
		{`{"a":1,"A":2}`, true},
		{`{"x":{"k":1,"K":2}}`, true},
		{`{"l":[{"a":1,"A":2}]}`, true},
		{`{"a":{"A":1}}`, false},
		{`{"l":[{"a":1},{"A":1}]}`, false},
		{`{"k":1,"K":2}`, true},   // KELVIN SIGN folds to k
		{`{"s":1,"ſ":2}`, true},   // LATIN SMALL LETTER LONG S folds to s
		{`{"ß":1,"ẞ":2}`, true},   // sharp s and capital sharp s
		{`{"ss":1,"ß":2}`, false}, // only full case folding maps ß to ss
		{`{"σ":1,"ς":2}`, true},   // sigma and final sigma
		{`{"ab":1,"aB":2,"c":[]}`, true},
		{`{"":1,"e":{},"f":[1,{"g":{"h":2,"H":3}}]}`, true},
	} {
		t.Run(c.args, func(t *testing.T) {
			if got := hasCaseFoldedMembers(json.RawMessage(c.args)); got != c.want {
				t.Errorf("hasCaseFoldedMembers = %v, want %v", got, c.want)
			}
			_, err := thvmcp.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t","arguments":` + c.args + `}}`))
			if refused := err != nil; refused != c.want {
				t.Errorf("the vMCP's parser refuses = %v (%v), want %v", refused, err, c.want)
			}
		})
	}
}

// TestArgumentsSHA256RefusesOutsideIJSON refuses every refusal case of the
// shared vectors: arguments that are not exactly one JSON object, or not
// I-JSON, so the vMCP refuses them too rather than computing another hash.
func TestArgumentsSHA256RefusesOutsideIJSON(t *testing.T) {
	file := readHashVectors(t)
	if len(file.Refusals) == 0 {
		t.Fatal("no refusal vectors")
	}
	for _, r := range file.Refusals {
		t.Run(r.Name, func(t *testing.T) {
			args := r.arguments(t)
			sent, sum, err := argumentsSHA256(args)
			if !errors.Is(err, errNotIJSON) {
				t.Fatalf("argumentsSHA256(%q) = %s, %q, %v; want errNotIJSON", args, sent, sum, err)
			}
			if sent != nil || sum != "" {
				t.Errorf("refusal returned sent %q, sum %q", sent, sum)
			}
		})
	}
}
