package dtogen

import (
	"fmt"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/packages"
)

const valueImportPath = "github.com/stacklok/mecatl/engine/kernel/value"

// primitiveName is an exact owner allowlist, not a serialization interface opt-in.
func primitiveName(t types.Type) string {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != valueImportPath {
		return ""
	}
	if n.Obj().Name() == "Value" || n.Obj().Name() == "Map" {
		return n.Obj().Name()
	}
	return ""
}

// verifyPrimitive checks loaded source provenance and every directly owned method.
// It never loads executable code or follows the owner's private recursive graph.
//
//nolint:gocyclo // Keep the closed owner provenance and complete five-method contract together.
func verifyPrimitive(t types.Type, root *packages.Package) error {
	n := types.Unalias(t).(*types.Named)
	var owner *packages.Package
	visitDependencies(root, func(p *packages.Package) {
		if p.Types == n.Obj().Pkg() {
			owner = p
		}
	})
	if owner == nil || owner.Module == nil || owner.Module.Path != "github.com/stacklok/mecatl/engine" || n.TypeParams().Len() != 0 || n.TypeArgs().Len() != 0 {
		return fmt.Errorf("%s: primitive owner provenance mismatch", t)
	}
	dir := filepath.Join(owner.Module.Dir, "kernel", "value")
	if len(owner.CompiledGoFiles) == 0 {
		return fmt.Errorf("%s: primitive owner has no source", t)
	}
	for _, file := range owner.CompiledGoFiles {
		if filepath.Dir(file) != dir {
			return fmt.Errorf("%s: primitive owner source mismatch", t)
		}
	}
	codec := owner.Imports[codecImportPath]
	if codec == nil || codec.Types == nil {
		return fmt.Errorf("%s: primitive owner missing codec dependency", t)
	}
	budgetType := codec.Types.Scope().Lookup("Budget")
	readerType := codec.Types.Scope().Lookup("Reader")
	if budgetType == nil || readerType == nil {
		return fmt.Errorf("%s: primitive owner missing codec types", t)
	}
	budget := types.NewPointer(budgetType.Type())
	reader := types.NewPointer(readerType.Type())
	bytes := types.NewSlice(types.Typ[types.Byte])
	errType := types.Universe.Lookup("error").Type()
	for _, method := range []struct {
		name            string
		pointer         bool
		params, results []types.Type
	}{
		{"CheckCBOR", false, []types.Type{budget, types.Typ[types.Int], types.Typ[types.Int]}, []types.Type{errType}},
		{"AppendCBOR", false, []types.Type{bytes}, []types.Type{bytes}},
		{"ReadCBOR", true, []types.Type{reader, types.Typ[types.Int], types.Typ[types.Int]}, []types.Type{errType}},
		{"Clone", false, nil, []types.Type{n}},
		{"Equal", false, []types.Type{n}, []types.Type{types.Typ[types.Bool]}},
	} {
		var fn *types.Func
		for i := 0; i < n.NumMethods(); i++ {
			if n.Method(i).Name() == method.name {
				fn = n.Method(i)
				break
			}
		}
		if fn == nil {
			return fmt.Errorf("%s: primitive owner missing %s", t, method.name)
		}
		sig := fn.Type().(*types.Signature)
		var recv types.Type = n
		if method.pointer {
			recv = types.NewPointer(n)
		}
		valid := !sig.Variadic() && types.Identical(sig.Recv().Type(), recv) && sig.Params().Len() == len(method.params) && sig.Results().Len() == len(method.results)
		if valid {
			for i, p := range method.params {
				valid = valid && types.Identical(sig.Params().At(i).Type(), p)
			}
			for i, r := range method.results {
				valid = valid && types.Identical(sig.Results().At(i).Type(), r)
			}
		}
		if !valid {
			return fmt.Errorf("%s: primitive owner invalid %s signature", t, method.name)
		}
		pos := owner.Fset.Position(fn.Pos()).Filename
		if filepath.Dir(pos) != dir {
			return fmt.Errorf("%s: primitive method provenance mismatch", t)
		}
	}
	return nil
}

// containsPrimitive follows whole-value edges, including imported and excluded
// state, only to document the bounded owner's transitive programmer-error panic.
func containsPrimitive(t types.Type, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if primitiveName(t) != "" {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return containsPrimitive(u.Elem(), seen)
	case *types.Slice:
		return containsPrimitive(u.Elem(), seen)
	case *types.Array:
		return containsPrimitive(u.Elem(), seen)
	case *types.Map:
		return containsPrimitive(u.Elem(), seen) || containsPrimitive(u.Key(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if containsPrimitive(u.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}
