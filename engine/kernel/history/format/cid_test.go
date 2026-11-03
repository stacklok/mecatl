package format_test

import (
	"encoding/base32"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// emptyDigestCID carries SHA-256 of empty bytes. Parsing an identifier does not
// require its digest to identify a valid commit.
const emptyDigestCID = "bafyreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"

func TestCID(t *testing.T) {
	v, err := contentid.Parse(emptyDigestCID)
	if err != nil {
		t.Fatal(err)
	}
	if v == (contentid.CID{}) || v.String() != emptyDigestCID {
		t.Fatalf("parsed CID = %q; want %q and nonzero value", v, emptyDigestCID)
	}
	parsed, err := contentid.Parse(v.String())
	if err != nil || parsed != v {
		t.Fatalf("round trip = %v, %v; want %v", parsed, err, v)
	}
	if got := map[contentid.CID]bool{v: true}; !got[parsed] {
		t.Fatal("equal CIDs must be interchangeable map keys")
	}
	if got := fmt.Sprint(v); got != emptyDigestCID {
		t.Fatalf("debug text = %q; want %q", got, emptyDigestCID)
	}
	var zero contentid.CID
	if zero.String() != "" {
		t.Fatalf("zero CID = %q; want empty text", zero)
	}
}

func TestParseCIDRejectsInvalidText(t *testing.T) {
	for name, text := range map[string]string{
		"empty":               "",
		"short":               emptyDigestCID[:58],
		"long":                emptyDigestCID + "a",
		"leading space":       " " + emptyDigestCID,
		"trailing newline":    emptyDigestCID + "\n",
		"embedded newline":    emptyDigestCID[:30] + "\n" + emptyDigestCID[31:],
		"embedded carriage":   emptyDigestCID[:30] + "\r" + emptyDigestCID[31:],
		"uppercase multibase": "B" + emptyDigestCID[1:],
		"uppercase base32":    "b" + strings.ToUpper(emptyDigestCID[1:]),
		"base58 multibase":    "z" + emptyDigestCID[1:],
		"no multibase":        emptyDigestCID[1:],
		"padding":             emptyDigestCID + "======",
		"invalid alphabet":    emptyDigestCID[:58] + "0",
		"nonzero pad bits":    emptyDigestCID[:58] + "v",
	} {
		t.Run(name, func(t *testing.T) {
			assertInvalidCID(t, text)
		})
	}
}

func TestParseCIDProfile(t *testing.T) {
	// The binary CID consists of four single-byte varints followed by a digest.
	binary := append([]byte{1, 0x71, 0x12, 32}, make([]byte, 32)...)
	text := cidText(binary)
	zeroDigest, err := contentid.Parse(text)
	if err != nil || zeroDigest == (contentid.CID{}) || zeroDigest.String() != text {
		t.Fatalf("all-zero digest = %v, %v; want valid, set CID", zeroDigest, err)
	}
	other, err := contentid.Parse(emptyDigestCID)
	if err != nil || other == zeroDigest {
		t.Fatalf("different digests must differ: %v, %v", other, err)
	}
	for name, data := range map[string][]byte{
		"CIDv0":             append([]byte{0x12, 32}, make([]byte, 32)...),
		"CIDv2":             append([]byte{2, 0x71, 0x12, 32}, make([]byte, 32)...),
		"raw codec":         append([]byte{1, 0x55, 0x12, 32}, make([]byte, 32)...),
		"different hash":    append([]byte{1, 0x71, 0x13, 32}, make([]byte, 32)...),
		"wrong digest size": append([]byte{1, 0x71, 0x12, 31}, make([]byte, 32)...),
		"truncated digest":  binary[:35],
		"extended digest":   append(append([]byte{}, binary...), 0),
		"overlong version":  append([]byte{0x81, 0, 0x71, 0x12, 32}, make([]byte, 32)...),
		"overlong codec":    append([]byte{1, 0xf1, 0, 0x12, 32}, make([]byte, 32)...),
		"overlong hash":     append([]byte{1, 0x71, 0x92, 0, 32}, make([]byte, 32)...),
		"overlong size":     append([]byte{1, 0x71, 0x12, 0xa0, 0}, make([]byte, 32)...),
	} {
		t.Run(name, func(t *testing.T) {
			assertInvalidCID(t, cidText(data))
		})
	}
}

func FuzzParseCID(f *testing.F) {
	for _, text := range []string{"", emptyDigestCID, emptyDigestCID[:58] + "v", strings.Repeat("a", 59)} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		v, err := contentid.Parse(text)
		if err != nil {
			if v != (contentid.CID{}) {
				t.Fatal("failed parse returned a set CID")
			}
			return
		}
		if v == (contentid.CID{}) || v.String() != text {
			t.Fatalf("successful parse changed text %q to %q", text, v)
		}
		parsed, err := contentid.Parse(v.String())
		if err != nil || parsed != v {
			t.Fatalf("round trip = %v, %v; want %v", parsed, err, v)
		}
	})
}

func assertInvalidCID(t *testing.T, text string) {
	t.Helper()
	v, err := contentid.Parse(text)
	if err == nil || v != (contentid.CID{}) {
		t.Fatalf("ParseCID(%q) = %v, %v; want zero CID and error", text, v, err)
	}
}

func cidText(binary []byte) string {
	return "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(binary))
}
