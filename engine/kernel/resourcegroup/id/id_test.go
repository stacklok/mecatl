package id_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

func TestGroupID(t *testing.T) {
	v := groupid.NewGroupID()
	parsed, err := groupid.ParseGroupID(v.String())
	if err != nil || parsed != v || v.ResourceID().Kind() != "GRP" {
		t.Fatalf("group identity round trip: %v, %v", parsed, err)
	}
	if !map[groupid.GroupID]bool{v: true}[parsed] {
		t.Fatal("equal group IDs must be interchangeable map keys")
	}
	var zero groupid.GroupID
	if zero.String() != "" || zero.ResourceID() != (resourceid.ID{}) {
		t.Fatal("zero identity must remain unset")
	}
	for _, text := range []string{
		"", "SES" + strings.Repeat("1", 22), "FOO" + strings.Repeat("1", 22),
		"GRP" + strings.Repeat("0", 22), "GRP" + strings.Repeat("1", 21),
		"GRP" + strings.Repeat("1", 23), " " + v.String(), v.String() + "\n",
		strings.ToLower(v.String()),
	} {
		got, err := groupid.ParseGroupID(text)
		if err == nil || got != zero {
			t.Errorf("ParseGroupID(%q) = %v, %v; want zero and error", text, got, err)
		}
	}
}

func TestGroupIDCBOR(t *testing.T) {
	limits := codec.Limits{MaxBytes: 100, MaxStringBytes: 25, MaxByteStringBytes: 1, MaxArrayElements: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 1, MaxCollectionElements: 1}
	v := groupid.NewGroupID()
	encoded, err := codec.Encode(v, limits)
	if err != nil || !bytes.Equal(encoded, v.ResourceID().AppendCBOR(nil)) {
		t.Fatalf("transparent resource text: %x, %v", encoded, err)
	}
	decoded, err := codec.Decode[groupid.GroupID](encoded, limits)
	if err != nil || decoded != v || !v.Equal(v.Clone()) {
		t.Fatalf("codec/value round trip: %v, %v", decoded, err)
	}
	clear(encoded)
	if decoded != v {
		t.Fatal("decoded identity borrows input bytes")
	}
	for _, text := range []string{"SES" + strings.Repeat("1", 22), "FOO" + strings.Repeat("1", 22)} {
		other, err := resourceid.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[groupid.GroupID](other.AppendCBOR(nil), limits)
		if !errors.Is(err, codec.ErrInvalidData) || got != (groupid.GroupID{}) {
			t.Fatalf("wrong kind accepted: %v, %v", got, err)
		}
	}
	var zero groupid.GroupID
	if _, err := codec.Encode(zero, limits); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("zero encoded: %v", err)
	}
	limits.MaxStringBytes--
	if _, err := codec.Encode(v, limits); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("text limit ignored: %v", err)
	}
}
