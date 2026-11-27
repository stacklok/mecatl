package mcpbroker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
)

// hashVectors is the shared argument hash vectors file,
// testdata/argumenthash/vectors.json: a byte-for-byte copy of ToolHive's
// canonical copy (pkg/vmcp/callbinding/testdata/argumenthash/vectors.json),
// which the vMCP tests against, so the MCP client and the vMCP compute the
// same hash and refuse the same arguments. TestHashVectorsAreToolHives keeps
// the two equal. The expected canonical strings come from RFC 8785 and
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
			if _, canonical, err := canonicalArguments(v.Arguments); err != nil || canonical != v.Canonical {
				t.Errorf("canonical = %s, %v; want %s", canonical, err, v.Canonical)
			}
		})
	}
}

// TestCanonicalizeNumbersAppendixB checks the number formatting of
// jsontext's Canonicalize, which canonicalArguments relies on, against every
// sample in RFC 8785 Appendix B, including those argumentsSHA256 refuses
// because they are integers beyond 2^53.
func TestCanonicalizeNumbersAppendixB(t *testing.T) {
	for _, c := range []struct{ ieee, want string }{
		{"0000000000000000", "0"},
		{"8000000000000000", "0"},
		{"0000000000000001", "5e-324"},
		{"8000000000000001", "-5e-324"},
		{"7fefffffffffffff", "1.7976931348623157e+308"},
		{"ffefffffffffffff", "-1.7976931348623157e+308"},
		{"4340000000000000", "9007199254740992"},
		{"c340000000000000", "-9007199254740992"},
		{"4430000000000000", "295147905179352830000"},
		{"44b52d02c7e14af5", "9.999999999999997e+22"},
		{"44b52d02c7e14af6", "1e+23"},
		{"44b52d02c7e14af7", "1.0000000000000001e+23"},
		{"444b1ae4d6e2ef4e", "999999999999999700000"},
		{"444b1ae4d6e2ef4f", "999999999999999900000"},
		{"444b1ae4d6e2ef50", "1e+21"},
		{"3eb0c6f7a0b5ed8c", "9.999999999999997e-7"},
		{"3eb0c6f7a0b5ed8d", "0.000001"},
		{"41b3de4355555553", "333333333.3333332"},
		{"41b3de4355555554", "333333333.33333325"},
		{"41b3de4355555555", "333333333.3333333"},
		{"41b3de4355555556", "333333333.3333334"},
		{"41b3de4355555557", "333333333.33333343"},
		{"becbf647612f3696", "-0.0000033333333333333333"},
		{"43143ff3c1cb0959", "1424953923781206.2"},
	} {
		bits, err := strconv.ParseUint(c.ieee, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		v := jsontext.Value("[" + strconv.FormatFloat(math.Float64frombits(bits), 'g', -1, 64) + "]")
		if err := v.Canonicalize(); err != nil || string(v) != "["+c.want+"]" {
			t.Errorf("Canonicalize(%s) = %s, %v; want [%s]", c.ieee, v, err, c.want)
		}
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

// TestArgumentsSHA256AgreesWithTheVMCP computes every shared vector and
// refusal with Mecatl's hash and with the vMCP's own,
// callbinding.ArgumentsSHA256 from the ToolHive module this build uses: the
// two produce the same hash, and refuse the same arguments.
func TestArgumentsSHA256AgreesWithTheVMCP(t *testing.T) {
	file := readHashVectors(t)
	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			_, ours, err := argumentsSHA256(v.Arguments)
			if err != nil {
				t.Fatalf("Mecatl's hash refused %s: %v", v.Arguments, err)
			}
			theirs, err := callbinding.ArgumentsSHA256(v.Arguments)
			if err != nil || theirs != ours {
				t.Fatalf("the vMCP's hash = %s, %v; Mecatl's %s", theirs, err, ours)
			}
		})
	}
	for _, r := range file.Refusals {
		t.Run(r.Name, func(t *testing.T) {
			args := r.arguments(t)
			if _, _, err := argumentsSHA256(args); err == nil {
				t.Fatal("Mecatl's hash accepted arguments the vectors refuse")
			}
			if sum, err := callbinding.ArgumentsSHA256(args); err == nil {
				t.Fatalf("the vMCP's hash accepted them: %s", sum)
			}
		})
	}
}
