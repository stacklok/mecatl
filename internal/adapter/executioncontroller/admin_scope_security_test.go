package executioncontroller

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func scopedManifestClient(t *testing.T, uri string, admin bool, scope []string) securityClientManifest {
	t.Helper()
	return securityClientManifest{URI: uri, Administrator: admin, AdministratorFor: scope}
}

func TestAdministratorScopeManifestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"absent", `{"uri":"spiffe://example/admin","administrator":true}`, true},
		{"empty", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":[]}`, true},
		{"scoped", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/creator"]}`, true},
		{"not admin", `{"uri":"spiffe://example/admin","administratorFor":["spiffe://example/creator"]}`, false},
		{"duplicate", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/creator","spiffe://example/creator"]}`, false},
		{"wildcard", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/*"]}`, false},
		{"escaped wildcard", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/%2A"]}`, false},
		{"case", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://EXAMPLE/creator"]}`, false},
		{"query", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/creator?q=1"]}`, false},
		{"fragment", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://example/creator#x"]}`, false},
		{"userinfo", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["spiffe://user@example/creator"]}`, false},
		{"malformed", `{"uri":"spiffe://example/admin","administrator":true,"administratorFor":["creator"]}`, false},
		{"unknown", `{"uri":"spiffe://example/admin","administrator":true,"adminFor":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entry securityClientManifest
			err := executionenv.DecodeStrict([]byte(tc.raw), &entry)
			if err == nil {
				_, err = clientPolicies([]securityClientManifest{entry})
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	for _, count := range []int{256, 257} {
		scope := make([]string, count)
		for i := range scope {
			scope[i] = fmt.Sprintf("spiffe://example/creator-%d", i)
		}
		entry := scopedManifestClient(t, "spiffe://example/admin", true, scope)
		if _, err := clientPolicies([]securityClientManifest{entry}); (err == nil) != (count == 256) {
			t.Fatalf("scope count=%d err=%v", count, err)
		}
	}
}

func TestAdministratorScopeDigestIsNormalizedAndAuthorityBound(t *testing.T) {
	a, b := "spiffe://example/a", "spiffe://example/b"
	mf := securityManifest{Clients: []securityClientManifest{scopedManifestClient(t, "spiffe://example/admin", true, []string{b, a})}}
	before, _ := json.Marshal(mf)
	digest := func(m securityManifest) string {
		t.Helper()
		d, err := authorityDigest(m, time.Minute, time.Second, nil, nil, "server")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	original := digest(mf)
	after, _ := json.Marshal(mf)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("digest mutated source manifest")
	}
	mf.Clients[0] = scopedManifestClient(t, "spiffe://example/admin", true, []string{a, b})
	if digest(mf) != original {
		t.Fatal("scope ordering changed authority digest")
	}
	mf.Clients[0] = scopedManifestClient(t, "spiffe://example/admin", true, []string{a})
	if digest(mf) == original {
		t.Fatal("scope removal did not change authority digest")
	}
	mf.Clients[0] = scopedManifestClient(t, "spiffe://example/admin", true, nil)
	empty := digest(mf)
	mf.Clients[0] = scopedManifestClient(t, "spiffe://example/admin", true, []string{})
	if digest(mf) != empty {
		t.Fatal("empty and absent scope differ")
	}
}
