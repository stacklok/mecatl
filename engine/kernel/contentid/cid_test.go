package contentid_test

import (
	"bytes"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// Independent SHA-256 vectors with the fixed 01 71 12 20 CID header.
const emptyCID = "bafyreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
const abcCID = "bafyreif2pall7dybz7vecqka3zo24irdwabwdi4wc55jznaq75q7eaavvu"

func TestSumAndVerify(t *testing.T) {
	for _, tt := range []struct{ data, text, digest string }{
		{"", emptyCID, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", abcCID, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	} {
		t.Run(tt.data, func(t *testing.T) {
			data := []byte(tt.data)
			before := bytes.Clone(data)
			v := contentid.Sum(data)
			if v.String() != tt.text {
				t.Fatalf("Sum = %s; want %s", v, tt.text)
			}
			binary := v.Binary()
			if hex.EncodeToString(binary[:]) != "01711220"+tt.digest {
				t.Fatalf("binary = %x", binary)
			}
			parsed, err := contentid.Parse(tt.text)
			if err != nil || parsed != v || !map[contentid.CID]bool{v: true}[parsed] {
				t.Fatalf("parse = %v, %v", parsed, err)
			}
			if err := contentid.Verify(data, v); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, before) {
				t.Fatal("input mutated")
			}
			if err := contentid.Verify(append(bytes.Clone(data), 0), v); !errors.Is(err, contentid.ErrMismatch) {
				t.Fatalf("altered bytes: %v", err)
			}
			if err := contentid.Verify(data, contentid.CID{}); !errors.Is(err, contentid.ErrInvalidExpected) || errors.Is(err, contentid.ErrMismatch) {
				t.Fatalf("unset expected: %v", err)
			}
		})
	}
	if (contentid.CID{}).String() != "" || (contentid.CID{}).Binary() != ([36]byte{}) || contentid.Sum(nil) != contentid.Sum([]byte{}) {
		t.Fatal("unset or empty input behavior")
	}
}

func TestSumOpaqueExactBytes(t *testing.T) {
	// These include invalid CBOR, noncanonical encodings of the same integer,
	// and arbitrary invalid UTF-8. Identity does not interpret any schema.
	inputs := [][]byte{{0xff, 0, 0xfe}, {0}, {0x18, 0}, {0xa0}, {0xa0, 0}}
	seen := map[contentid.CID]bool{}
	for _, data := range inputs {
		v := contentid.Sum(data)
		if seen[v] || contentid.Verify(data, v) != nil {
			t.Fatal("bytes normalized or rejected")
		}
		seen[v] = true
		data[0] ^= 1
		if !errors.Is(contentid.Verify(data, v), contentid.ErrMismatch) {
			t.Fatal("CID retained mutable input")
		}
	}
}

func TestParseRejectsNoncanonicalText(t *testing.T) {
	for name, text := range map[string]string{
		"empty": "", "short": emptyCID[:58], "long": emptyCID + "a",
		"space": " " + emptyCID, "newline": emptyCID + "\n",
		"embedded newline":  emptyCID[:30] + "\n" + emptyCID[31:],
		"embedded carriage": emptyCID[:30] + "\r" + emptyCID[31:],
		"uppercase prefix":  "B" + emptyCID[1:], "uppercase": "b" + strings.ToUpper(emptyCID[1:]),
		"base58": "z" + emptyCID[1:], "missing prefix": emptyCID[1:],
		"padding": emptyCID + "======", "alphabet": emptyCID[:58] + "0",
		"nonzero padding bits": emptyCID[:58] + "v",
	} {
		t.Run(name, func(t *testing.T) {
			if v, err := contentid.Parse(text); err == nil || v != (contentid.CID{}) {
				t.Fatalf("Parse = %v, %v", v, err)
			}
		})
	}
}

func TestBinaryProfileAndOwnership(t *testing.T) {
	// An all-zero digest is valid, and distinct from the unset CID.
	binary := append([]byte{1, 0x71, 0x12, 32}, make([]byte, 32)...)
	v, err := contentid.ParseBinary(binary)
	if err != nil || v == (contentid.CID{}) {
		t.Fatal(v, err)
	}
	owned := v.Binary()
	binary[4] = 1
	if v.Binary() != owned {
		t.Fatal("ParseBinary retained input")
	}
	modified := v.Binary()
	modified[4] = 2
	if v.Binary() != owned {
		t.Fatal("Binary aliases CID storage")
	}
	if parsed, err := contentid.Parse(v.String()); err != nil || parsed != v {
		t.Fatal(parsed, err)
	}
	for name, data := range map[string][]byte{
		"unset": make([]byte, 36), "nil": nil,
		"CIDv0":       append([]byte{0x12, 32}, make([]byte, 32)...),
		"CIDv2":       append([]byte{2, 0x71, 0x12, 32}, make([]byte, 32)...),
		"raw codec":   append([]byte{1, 0x55, 0x12, 32}, make([]byte, 32)...),
		"other hash":  append([]byte{1, 0x71, 0x13, 32}, make([]byte, 32)...),
		"digest size": append([]byte{1, 0x71, 0x12, 31}, make([]byte, 32)...),
		"short":       binary[:35], "long": append(bytes.Clone(binary), 0),
		"multibase prefix": append([]byte{0}, binary...),
		"overlong version": append([]byte{0x81, 0, 0x71, 0x12, 32}, make([]byte, 32)...),
		"overlong codec":   append([]byte{1, 0xf1, 0, 0x12, 32}, make([]byte, 32)...),
		"overlong hash":    append([]byte{1, 0x71, 0x92, 0, 32}, make([]byte, 32)...),
		"overlong size":    append([]byte{1, 0x71, 0x12, 0xa0, 0}, make([]byte, 32)...),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := contentid.ParseBinary(data); err == nil || got != (contentid.CID{}) {
				t.Fatalf("ParseBinary = %v, %v", got, err)
			}
			text := "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data))
			if got, err := contentid.Parse(text); err == nil || got != (contentid.CID{}) {
				t.Fatalf("Parse = %v, %v", got, err)
			}
		})
	}
}

func TestConcurrentIdentity(t *testing.T) {
	data := []byte("abc")
	want := contentid.Sum(data)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 32 {
				got, err := contentid.Parse(abcCID)
				binary := want.Binary()
				fromBinary, binaryErr := contentid.ParseBinary(binary[:])
				if err != nil || binaryErr != nil || got != want || fromBinary != want || contentid.Sum(data) != want || contentid.Verify(data, want) != nil {
					t.Error("concurrent identity changed")
				}
			}
		})
	}
	wg.Wait()
}

func FuzzParse(f *testing.F) {
	for _, text := range []string{"", emptyCID, abcCID, emptyCID[:58] + "v"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		v, err := contentid.Parse(text)
		if err != nil {
			if v != (contentid.CID{}) {
				t.Fatal("failure returned set CID")
			}
			return
		}
		binary := v.Binary()
		parsed, err := contentid.ParseBinary(binary[:])
		if v == (contentid.CID{}) || v.String() != text || err != nil || parsed != v {
			t.Fatal("successful parse changed identity")
		}
	})
}
