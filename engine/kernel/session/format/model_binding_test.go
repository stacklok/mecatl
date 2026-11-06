package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestModelBinding(t *testing.T) {
	for _, settings := range []string{"{}", " { \"z\": 1.00, \"a\": 1e+2 }\n", "null", "false", "[1,2]", "\"opaque\""} {
		v := format.ModelBinding{Route: " route ", API: "dialect/v1", Model: "opaque alias", Settings: []byte(settings)}
		before := v.Clone()
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.ModelBinding](wire, limits())
		if err != nil || !got.Equal(v) {
			t.Fatalf("roundtrip: %v", err)
		}
		clear(v.Settings)
		clear(wire)
		if !got.Equal(before) || !bytes.Equal(got.Settings, []byte(settings)) {
			t.Fatal("binding borrowed or normalized settings")
		}
	}
}

func TestModelBindingMalformed(t *testing.T) {
	valid := format.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}
	for _, field := range []string{"route", "api", "model", "settings", "bad JSON", "bad UTF-8"} {
		v := valid.Clone()
		switch field {
		case "route":
			v.Route = ""
		case "api":
			v.API = ""
		case "model":
			v.Model = ""
		case "settings":
			v.Settings = nil
		case "bad JSON":
			v.Settings = []byte("{} {}")
		case "bad UTF-8":
			v.Model = string([]byte{0xff})
		}
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("%s encode: %v", field, err)
		}
		got, err := codec.Decode[format.ModelBinding](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ModelBinding{}) {
			t.Fatalf("%s decode: %v", field, err)
		}
	}
}
