package format_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContentSource(t *testing.T) {
	for _, path := range []string{"AGENTS.md", "skills/e\u0301/参考.md", "space dir/file ", "a\\b"} {
		v := format.ContentSource{Description: " adopted and rewritten \n", WorkspaceFile: &format.WorkspaceFileSource{Path: path}}
		before := v.Clone()
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		got, err := codec.Decode[format.ContentSource](wire, limits())
		if err != nil || !got.Equal(v) {
			t.Fatalf("roundtrip: %v", err)
		}
		v.WorkspaceFile.Path = "changed"
		clear(wire)
		if !got.Equal(before) {
			t.Fatal("source storage shared or normalized")
		}
	}
	if (format.ContentSource{Description: "d"}).Equal(format.ContentSource{Description: "d", WorkspaceFile: &format.WorkspaceFileSource{}}) {
		t.Fatal("file pointer presence lost")
	}
	for _, v := range []format.ContentSource{{}, {Description: string([]byte{0xff})}, {Description: "d", WorkspaceFile: &format.WorkspaceFileSource{}}} {
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("source encode: %v", err)
		}
		got, err := codec.Decode[format.ContentSource](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ContentSource{}) {
			t.Fatalf("source decode: %v", err)
		}
	}
	for _, path := range []string{"", ".", "..", "../a", "a/../b", "a/./b", "/a", "a//b", "a/", string([]byte{0xff})} {
		v := format.WorkspaceFileSource{Path: path}
		if v.Validate() == nil {
			t.Fatalf("invalid path validated: %q", path)
		}
		if _, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("path %q: %v", path, err)
		}
		if _, err := codec.Decode[format.WorkspaceFileSource](v.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("path decode %q: %v", path, err)
		}
	}
}
