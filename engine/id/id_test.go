package id

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	v := New(History)
	if v.Kind() != History || len(v.String()) != 25 {
		t.Fatalf("unexpected ID: %q (%q)", v.String(), v.Kind())
	}
	parsed, err := Parse(v.String())
	if err != nil || parsed != v {
		t.Fatalf("round trip: got %v, %v; want %v", parsed, err, v)
	}
	if got := map[ID]bool{v: true}; !got[parsed] {
		t.Fatal("equal IDs must be interchangeable map keys")
	}
}

func TestParse(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("1", 22),
		strings.Repeat("z", 22),
		"123456789ABCDEFGHJKLMN",
	} {
		text := "HST" + body
		v, err := Parse(text)
		if err != nil || v.String() != text || v.Kind() != History {
			t.Fatalf("Parse(%q) = %v, %v", text, v, err)
		}
	}
	for _, text := range []string{
		"", "HST", "hst" + strings.Repeat("1", 22),
		"RUN" + strings.Repeat("1", 22),
		"HST" + strings.Repeat("1", 21),
		"HST" + strings.Repeat("1", 23),
		" HST" + strings.Repeat("1", 22),
		"HST" + strings.Repeat("1", 22) + "\n",
	} {
		if v, err := Parse(text); err == nil || v != (ID{}) {
			t.Errorf("Parse(%q) = %v, %v; want zero ID and error", text, v, err)
		}
	}
	for _, bad := range []string{"0", "O", "I", "l", "-", "_", " ", "\x00", "\xff", "é"} {
		text := "HST" + strings.Repeat("1", 21) + bad
		if v, err := Parse(text); err == nil || v != (ID{}) {
			t.Errorf("Parse(%q) = %v, %v; want zero ID and error", text, v, err)
		}
	}
}

func TestZero(t *testing.T) {
	var v ID
	if v.String() != "" || v.Kind() != "" {
		t.Fatalf("zero ID = %q (%q)", v.String(), v.Kind())
	}
	if _, err := Parse(v.String()); err == nil {
		t.Fatal("zero ID must not parse as a valid identity")
	}
}

func TestNewAlphabet(t *testing.T) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for i := range len(alphabet) {
		v, err := newID(History, bytes.NewReader(bytes.Repeat([]byte{byte(i)}, 22)))
		if err != nil {
			t.Fatal(err)
		}
		if want := "HST" + strings.Repeat(alphabet[i:i+1], 22); v.String() != want {
			t.Fatalf("sample %d: got %q; want %q", i, v.String(), want)
		}
		if parsed, err := Parse(v.String()); err != nil || parsed != v {
			t.Fatalf("alphabet sample %d: Parse(%q) = %v, %v", i, v.String(), parsed, err)
		}
	}
}

func TestNewRejectsOutOfRangeSamples(t *testing.T) {
	// rand.Int masks each byte to six bits; 58–63 must be rejected, not reduced modulo 58.
	samples := bytes.Repeat([]byte{58, 59, 60, 61, 62, 63, 0}, 22)
	v, err := newID(History, bytes.NewReader(samples))
	if err != nil || v.String() != "HST"+strings.Repeat("1", 22) {
		t.Fatalf("rejection sampling: got %v, %v", v, err)
	}
}

func TestNewFailure(t *testing.T) {
	for _, n := range []int{0, 10, 21} {
		v, err := newID(History, bytes.NewReader(make([]byte, n)))
		if !errors.Is(err, io.EOF) || v != (ID{}) {
			t.Fatalf("%d bytes: got %v, %v; want zero ID and EOF", n, v, err)
		}
	}
	for _, kind := range []Kind{"", "RUN", "hst", "HSTextra"} {
		t.Run(string(kind), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("New(%q) must panic", kind)
				}
			}()
			New(kind)
		})
		v, err := newID(kind, bytes.NewReader(nil))
		if err == nil || errors.Is(err, io.EOF) || v != (ID{}) {
			t.Fatalf("kind %q: got %v, %v; want rejection before random read", kind, v, err)
		}
	}
}
