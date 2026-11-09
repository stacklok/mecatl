package value_test

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

var (
	_ codec.Marshaler   = value.Value{}
	_ codec.Marshaler   = value.Map(nil)
	_ codec.Unmarshaler = (*value.Value)(nil)
	_ codec.Unmarshaler = (*value.Map)(nil)
)

func ExampleMap_Admit() {
	// Ordinary mutable fields need no pre-freeze wrapper before DTO assignment.
	type Request struct{ Arguments value.Map }
	options := value.Map{"enabled": value.Bool(true), "extra": value.Null()}
	request := Request{Arguments: value.Map{
		"path":    value.String("README.md"),
		"limit":   value.Int64(50),
		"options": value.Object(options),
	}}
	policy := codec.Limits{
		MaxBytes: 1 << 20, MaxStringBytes: 1 << 16, MaxByteStringBytes: 1 << 16,
		MaxArrayElements: 1000, MaxMapPairs: 1000, MaxDepth: 256,
		MaxNodes: 10000, MaxCollectionElements: 5000,
	}
	accepted, err := request.Arguments.Admit(policy)
	if err != nil {
		panic(err)
	}
	request.Arguments["path"] = value.String("changed after admission")
	options["extra"] = value.Bool(false)
	pathValue, _ := accepted.Get("path")
	path, _ := pathValue.String()
	limitValue, _ := accepted.Get("limit")
	limit, _ := limitValue.Int64()
	optionsValue, _ := accepted.Get("options")
	object, _ := optionsValue.Object()
	extra, present := object.Get("extra")
	fmt.Println(path, limit, present && extra.Kind() == value.NullKind)
	// Output: README.md 50 true
}
