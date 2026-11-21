package as1server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are duplicated with keysetfile.go's twin in
// internal/adapter/mcpbroker; see keysetfile.go for why. Keep both tables the
// same.

func TestCheckKeySetFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	unreadable := write("unreadable.json", `{"keys":[{"kty":"EC"}]}`)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		path   string
		want   string
		wantIs error
	}{
		// ToolHive checks the key itself; this check needs one key of any type.
		"a valid non-EC key": {path: write("ed25519.json", `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"secret-shaped-key-material"}]}`)},
		"an empty file":      {path: write("empty.json", ""), want: "is not a JSON JWK Set"},
		"no keys member":     {path: write("object.json", `{}`), want: "holds no key"},
		"null keys":          {path: write("null.json", `{"keys":null}`), want: "holds no key"},
		"keys not an array":  {path: write("keys-object.json", `{"keys":{"kty":"secret-shaped"}}`), want: "is not a JSON JWK Set"},
		"an empty key set":   {path: write("no-keys.json", `{"keys":[]}`), want: "holds no key"},
		"not a JWK Set":      {path: write("array.json", `["secret-shaped"]`), want: "is not a JSON JWK Set"},
		"a relative path":    {path: "jwks.json", want: "is not an absolute path"},
		"a missing file":     {path: filepath.Join(dir, "absent.json"), want: "no such file", wantIs: fs.ErrNotExist},
		"an unreadable file": {path: unreadable, want: "permission denied", wantIs: fs.ErrPermission},
	} {
		t.Run(name, func(t *testing.T) {
			if test.wantIs == fs.ErrPermission && os.Geteuid() == 0 {
				t.Skip("root reads a file without permission")
			}
			err := checkKeySetFile(test.path)
			if test.want == "" {
				if err != nil {
					t.Fatalf("checkKeySetFile = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkKeySetFile = %v, want an error containing %q", err, test.want)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Errorf("checkKeySetFile = %v, want errors.Is %v", err, test.wantIs)
			}
			if strings.Contains(err.Error(), "secret-shaped") {
				t.Errorf("checkKeySetFile = %v, which contains the file's contents", err)
			}
		})
	}
}
