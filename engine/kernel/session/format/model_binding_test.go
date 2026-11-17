package format_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestModelBinding(t *testing.T) {
	for _, settings := range []format.ModelSettings{
		{}, {ReasoningEffort: new(" exact target/尾 ")}, {MaxOutputTokens: new(int64(1))},
		{ReasoningEffort: new("high"), MaxOutputTokens: new(int64(9223372036854775807))},
	} {
		v := format.ModelBinding{Route: " route ", API: "dialect/v1", Model: "opaque alias", Settings: settings}
		before := v.Clone()
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.ModelBinding](wire, limits())
		if err != nil || !got.Equal(v) || v.Validate() != nil {
			t.Fatalf("roundtrip: %v", err)
		}
		if v.Settings.ReasoningEffort != nil {
			*v.Settings.ReasoningEffort = "changed"
		}
		if v.Settings.MaxOutputTokens != nil {
			*v.Settings.MaxOutputTokens = 0
		}
		clear(wire)
		if !got.Equal(before) {
			t.Fatal("binding borrowed or normalized settings")
		}
	}
}

func TestModelBindingMalformed(t *testing.T) {
	valid := format.ModelBinding{Route: "r", API: "a", Model: "m"}
	for _, field := range []string{"route", "api", "model", "empty effort", "bad effort", "zero tokens", "negative tokens", "bad UTF-8"} {
		v := valid.Clone()
		switch field {
		case "route":
			v.Route = ""
		case "api":
			v.API = ""
		case "model":
			v.Model = ""
		case "empty effort":
			v.Settings.ReasoningEffort = new("")
		case "bad effort":
			v.Settings.ReasoningEffort = new(string([]byte{0xff}))
		case "zero tokens":
			v.Settings.MaxOutputTokens = new(int64(0))
		case "negative tokens":
			v.Settings.MaxOutputTokens = new(int64(-1))
		case "bad UTF-8":
			v.Model = string([]byte{0xff})
		}
		if v.Validate() == nil {
			t.Fatalf("%s validates", field)
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
