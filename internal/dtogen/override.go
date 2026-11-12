package dtogen

import (
	"fmt"
	"go/types"
)

// validateOverrides requires concrete, directly declared value-receiver methods.
// Promoted methods are not implementations of a whole-type ownership contract.
func validateOverrides(named *types.Named, overrides capabilities) error {
	for _, name := range []string{methodClone, methodEqual} {
		if !overrides.method(name) {
			continue
		}
		var method *types.Func
		for i := 0; i < named.NumMethods(); i++ {
			if candidate := named.Method(i); candidate.Name() == name {
				method = candidate
				break
			}
		}
		contract := "Clone() T"
		if name == methodEqual {
			contract = "Equal(T) bool"
		}
		invalid := func() error {
			return fmt.Errorf("%s: dtogen override %s requires a handwritten method with signature func (T) %s", named, name, contract)
		}
		if method == nil {
			return invalid()
		}
		sig := method.Type().(*types.Signature)
		if !types.Identical(sig.Recv().Type(), named) || sig.Variadic() || sig.Results().Len() != 1 {
			return invalid()
		}
		if name == methodClone {
			if sig.Params().Len() != 0 || !types.Identical(sig.Results().At(0).Type(), named) {
				return invalid()
			}
		} else if sig.Params().Len() != 1 || !types.Identical(sig.Params().At(0).Type(), named) || !types.Identical(sig.Results().At(0).Type(), types.Typ[types.Bool]) {
			return invalid()
		}
	}
	return nil
}
