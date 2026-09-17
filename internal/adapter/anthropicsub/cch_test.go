package anthropicsub

import (
	"bytes"
	"strings"
	"testing"
)

// Canonical XXH64 vectors. The attestation uses a non-zero seed, so the seeded
// vector is the one that guards the seed path; an implementation that silently
// ignored the seed would still pass the seedless case.
func TestXXH64MatchesCanonicalVectors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		seed  uint64
		want  uint64
	}{
		{name: "empty seedless", input: "", seed: 0, want: 0xef46db3751d8e999},
		{name: "empty seeded", input: "", seed: 2654435761, want: 0xac75fda2929b17ef},
		{name: "single byte", input: "a", seed: 0, want: 0xd24ec4f1a98c6e5b},
		{name: "three bytes", input: "abc", seed: 0, want: 0x44bc2cf5ad770999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := xxh64([]byte(tc.input), tc.seed); got != tc.want {
				t.Fatalf("xxh64(%q, %d) = %016x, want %016x", tc.input, tc.seed, got, tc.want)
			}
		})
	}
}

// The 32-byte block path is separate code from the tail path, so a long input
// must be covered; these cross the boundary in both directions.
func TestXXH64CoversBlockAndTailPaths(t *testing.T) {
	long := strings.Repeat("mecatl-subscription-attestation-", 9) // 288 bytes
	for _, length := range []int{31, 32, 33, 39, 40, 63, 64, 100, 288} {
		input := []byte(long[:length])
		if got := xxh64(input, cchSeed); got == 0 {
			t.Fatalf("xxh64 of %d bytes produced zero", length)
		}
		// Flipping one byte must change the digest at every length.
		mutated := bytes.Clone(input)
		if length > 0 {
			mutated[length-1] ^= 0xff
			if xxh64(input, cchSeed) == xxh64(mutated, cchSeed) {
				t.Fatalf("digest unchanged after mutating %d-byte input", length)
			}
		}
	}
}

// A body with no billing block is an API-key request and must be left byte-identical.
func TestPatchCchLeavesNonBillingBodiesUntouched(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"cch=00000 in user text"}]}`)
	original := bytes.Clone(body)
	if patchCch(body) {
		t.Fatal("a body with no billing block reported a patch")
	}
	if !bytes.Equal(body, original) {
		t.Fatal("a non-billing body was modified")
	}
}

// The placeholder is anchored to the first system block, so a copy of the
// literal in user content must not be rewritten instead.
func TestPatchCchRewritesOnlyTheAnchoredPlaceholder(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"` + billingHeaderPrefix +
		` cc_version=1.abc; cc_entrypoint=claude-desktop; cch=00000;"}],` +
		`"messages":[{"role":"user","content":"cch=00000"}]}`)
	if !patchCch(body) {
		t.Fatal("the anchored placeholder was not patched")
	}
	if count := bytes.Count(body, []byte(cchPlaceholder)); count != 1 {
		t.Fatalf("remaining placeholders = %d, want the user-content copy only", count)
	}
	// The surviving copy must be the one in user content, after the system block.
	systemEnd := bytes.Index(body, []byte(`"messages"`))
	if bytes.Index(body, []byte(cchPlaceholder)) < systemEnd {
		t.Fatal("the system block placeholder survived instead of the user copy")
	}
}

// A placeholder beyond the search window is not the client's field and must be
// refused rather than rewritten.
func TestPatchCchRefusesUnanchoredPlaceholder(t *testing.T) {
	filler := strings.Repeat("x", cchSearchWindow+40)
	body := []byte(`{"system":[{"type":"text","text":"` + billingHeaderPrefix + filler + ` cch=00000;"}]}`)
	if patchCch(body) {
		t.Fatal("a placeholder outside the search window was patched")
	}
}

// The written digest is the low 20 bits of the seeded digest, rendered as five
// lowercase hex characters.
func TestPatchCchWritesLowTwentyBits(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"` + billingHeaderPrefix +
		` cc_version=1.abc; cc_entrypoint=claude-desktop; cch=00000;"}]}`)
	want := xxh64(body, cchSeed) & 0xfffff
	if !patchCch(body) {
		t.Fatal("placeholder was not patched")
	}
	at := bytes.Index(body, []byte("cch=")) + 4
	digits := string(body[at : at+cchDigits])
	var got uint64
	for _, c := range digits {
		got <<= 4
		switch {
		case c >= '0' && c <= '9':
			got |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			got |= uint64(c-'a') + 10
		default:
			t.Fatalf("digit %q is not lowercase hex in %q", c, digits)
		}
	}
	if got != want {
		t.Fatalf("attestation = %05x, want %05x", got, want)
	}
}
