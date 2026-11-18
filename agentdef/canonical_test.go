package agentdef

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestCanonicalForm pins the exact bytes of the form: keys from the json tags
// in field order, empty fields left out, map keys sorted, no HTML escaping, no
// trailing newline. A change here changes every digest and needs a new
// FormVersion.
func TestCanonicalForm(t *testing.T) {
	d := Definition{
		Name:        "reviewer",
		Description: "Reviews <diffs> & tests",
		Tools:       []string{"Read", "Grep"},
		MaxTurns:    3,
		MCPServers: []MCPServer{
			{Name: "github"},
			{Name: "tracker", URL: "https://mcp.example.test", Headers: map[string]string{"X-B": "2", "X-A": "1"}},
		},
		Hooks: map[string]string{"Stop": "b", "PreToolUse": "a"},
		Body:  "Body.",
	}
	got, err := Canonical(d)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	want := `{"name":"reviewer","description":"Reviews <diffs> & tests","tools":["Read","Grep"],"maxTurns":3,` +
		`"mcpServers":[{"name":"github"},{"name":"tracker","url":"https://mcp.example.test","headers":{"X-A":"1","X-B":"2"}}],` +
		`"hooks":{"PreToolUse":"a","Stop":"b"},"body":"Body."}`
	if string(got) != want {
		t.Errorf("Canonical =\n%s\nwant\n%s", got, want)
	}
}

// TestCanonicalOmitsUnsetFields pins that a definition setting only the
// required fields encodes only them, so adding an optional field to
// Definition leaves its digest unchanged.
func TestCanonicalOmitsUnsetFields(t *testing.T) {
	got, err := Canonical(Definition{Name: "n", Description: "d"})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if want := `{"name":"n","description":"d"}`; string(got) != want {
		t.Errorf("Canonical = %s, want %s", got, want)
	}
}

// TestCanonicalOfParsedForms pins that two spellings Parse reads as the same
// definition have the same form.
func TestCanonicalOfParsedForms(t *testing.T) {
	a, _, err := Parse([]byte("---\nname: n\ndescription: d\ntools: Read, Grep\n---\n\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b, _, err := Parse([]byte("---\r\ndescription: d\r\nname: n\r\ntools: [Read, Grep]\r\n---\r\nbody"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	fa, err := Canonical(a)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	fb, err := Canonical(b)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if string(fa) != string(fb) {
		t.Errorf("forms differ:\n%s\n%s", fa, fb)
	}
}

// TestCanonicalRejectsInvalidUTF8 pins that every kind of text field is
// checked: JSON would encode an invalid byte as U+FFFD, so two definitions
// could otherwise share a form.
func TestCanonicalRejectsInvalidUTF8(t *testing.T) {
	const bad = "x\xffy"
	cases := map[string]Definition{
		"body":        {Name: "n", Description: "d", Body: bad},
		"tools":       {Name: "n", Description: "d", Tools: []string{bad}},
		"header":      {Name: "n", Description: "d", MCPServers: []MCPServer{{Name: "s", URL: "https://x", Headers: map[string]string{"K": bad}}}},
		"hook phase":  {Name: "n", Description: "d", Hooks: map[string]string{bad: "cmd"}},
		"description": {Name: "n", Description: bad},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Canonical(d)
			if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
				t.Fatalf("Canonical err = %v, want a UTF-8 error", err)
			}
			if strings.Contains(err.Error(), bad) {
				t.Errorf("error quotes the value: %v", err)
			}
		})
	}
}

// TestDigest pins the digest rule: lowercase hexadecimal SHA-256 of the
// canonical form, failing when the form does.
func TestDigest(t *testing.T) {
	got, err := Digest(Definition{Name: "n", Description: "d"})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	sum := sha256.Sum256([]byte(`{"name":"n","description":"d"}`))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("Digest = %s, want %s", got, want)
	}
	if DigestAlgorithm != "sha256" {
		t.Errorf("DigestAlgorithm = %q, want sha256", DigestAlgorithm)
	}
	if _, err := Digest(Definition{Name: "n", Description: "x\xffy"}); err == nil {
		t.Error("Digest of a definition with invalid UTF-8 succeeded")
	}
}
