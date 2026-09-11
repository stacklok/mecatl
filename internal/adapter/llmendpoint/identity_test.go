package llmendpoint

import (
	"reflect"
	"testing"
)

func TestNormalizeScopesSortsDeduplicatesAndOwnsInput(t *testing.T) {
	input := []string{"write", "read", "write", "admin"}
	wantInput := append([]string(nil), input...)

	got, err := NormalizeScopes(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"admin", "read", "write"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeScopes() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("NormalizeScopes mutated input: got %v, want %v", input, wantInput)
	}
	got[0] = "changed"
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("NormalizeScopes result aliases input: got %v, want %v", input, wantInput)
	}
}

func TestNormalizeScopesRejectsEmptyAndInvalid(t *testing.T) {
	for _, scopes := range [][]string{nil, {}, {"valid", "bad scope"}, {""}} {
		if _, err := NormalizeScopes(scopes); err == nil {
			t.Fatalf("NormalizeScopes(%q) succeeded", scopes)
		}
	}
}
