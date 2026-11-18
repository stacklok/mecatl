package agentdef

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// FormVersion names the canonical form Canonical produces. Any change to that
// form, such as a new encoding rule or a renamed key, changes digests of
// definitions that did not change, so it needs a new version. A consumer that
// records a digest should record this version with it.
const FormVersion = "agentdef-canonical/v1"

// DigestAlgorithm names the algorithm Digest uses.
const DigestAlgorithm = "sha256"

// Digest returns the digest of the definition d: the lowercase hexadecimal
// SHA-256 of Canonical(d). It is the digest of the accepted definition, so two
// readers of the same file get the same value; it fails when Canonical does.
func Digest(d Definition) (string, error) {
	form, err := Canonical(d)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(form)
	return hex.EncodeToString(sum[:]), nil
}

// Canonical returns the canonical bytes of d: the input of a digest of the
// accepted definition, so two definitions have the same digest exactly when
// they are the same definition. The form is the JSON encoding of d with:
//
//   - the keys its json tags name, in the order of its fields;
//   - map keys sorted;
//   - empty fields left out, so a field added to Definition changes only the
//     digests of definitions that set it;
//   - no HTML escaping and no trailing newline.
//
// It returns an error when a string in d is not valid UTF-8: JSON would encode
// each invalid byte as U+FFFD, so two different definitions could share a form.
func Canonical(d Definition) ([]byte, error) {
	if err := checkUTF8(d); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("encode definition: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

type textField struct {
	name   string
	values []string
}

func checkUTF8(d Definition) error {
	fields := []textField{
		{"name", []string{d.Name}},
		{"description", []string{d.Description}},
		{"tools", d.Tools},
		{"disallowedTools", d.DisallowedTools},
		{"model", []string{d.Model}},
		{"provider", []string{d.Provider}},
		{"permissionMode", []string{d.PermissionMode}},
		{"color", []string{d.Color}},
		{"skills", d.Skills},
		{"memory", []string{d.Memory}},
		{"body", []string{d.Body}},
	}
	for _, s := range d.MCPServers {
		fields = append(fields, textField{"mcpServers", append([]string{s.Name, s.URL}, mapStrings(s.Headers)...)})
	}
	fields = append(fields, textField{"hooks", mapStrings(d.Hooks)})
	for _, f := range fields {
		for _, v := range f.values {
			if !utf8.ValidString(v) {
				return fmt.Errorf("definition field %s is not valid UTF-8", f.name)
			}
		}
	}
	return nil
}

func mapStrings(m map[string]string) []string {
	out := make([]string, 0, 2*len(m))
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}
