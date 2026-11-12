package mcpbroker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are duplicated with keysetfile.go's twin in
// internal/identitybroker/adapter/as1server; see keysetfile.go for why.
// https://github.com/stacklok/mecatl/issues/2538 deletes both.

const testKeySet = `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"secret-shaped-key-material"}]}`

func TestKeySetHasKeys(t *testing.T) {
	for name, test := range map[string]struct {
		raw  string
		want bool
	}{
		"one key":        {raw: testKeySet, want: true},
		"empty":          {raw: ""},
		"empty JWK Set":  {raw: `{"keys":[]}`},
		"no keys member": {raw: `{}`},
		"null keys":      {raw: `{"keys":null}`},
		"not a JWK Set":  {raw: `[]`},
		"keys not array": {raw: `{"keys":{}}`},
		"malformed JSON": {raw: `{"keys":`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hasKeys([]byte(test.raw)); got != test.want {
				t.Fatalf("hasKeys(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

func TestWriteKeySetIsPrivateAndRemovable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	file, remove, err := writeKeySet("keyset-test-", []byte(testKeySet))
	if err != nil {
		t.Fatalf("writeKeySet: %v", err)
	}
	dir := filepath.Dir(file)
	if filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), "keyset-test-") {
		t.Fatalf("file %q is not in a prefixed directory under TMPDIR %q", file, tmp)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, []byte(testKeySet)) {
		t.Fatalf("file contents = %q, %v; want the key set", got, err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, file: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
	}
	remove()
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Fatalf("TMPDIR after remove = %v, %v; want empty", left, err)
	}
}

func TestWriteKeySetFailureOmitsKeyMaterial(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notDir)
	file, remove, err := writeKeySet("keyset-test-", []byte(testKeySet))
	if err == nil {
		remove()
		t.Fatalf("writeKeySet under a non-directory TMPDIR = %q, want an error", file)
	}
	if strings.Contains(err.Error(), "secret-shaped-key-material") {
		t.Fatalf("error %q contains key material", err)
	}
}
