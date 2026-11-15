package modelrequest

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// ConstructionResult pairs an immutable logical request with its privately owned
// reconstruction recipe. Copies share immutable backing. Zero has an unset Request
// and zero Recipe; it is not a partial usable construction or invocation commitment.
type ConstructionResult struct {
	request Request
	recipe  session.ConstructionRecipe
}

// Request returns the immutable handle without copying request payloads.
func (v ConstructionResult) Request() Request { return v.request }

// Recipe returns a detached mutable recipe. Mutating it cannot change this result.
// The caller supplies it to session authoring; this method chooses no RetryOf.
func (v ConstructionResult) Recipe() session.ConstructionRecipe { return v.recipe.Clone() }

// finalizeConstruction fixes the built-in chronological-v1 recipe around a bounded
// logical request. Both inputs must remain stable during this trusted internal
// operation. Encoders validate before allocating output; no caller storage escapes.
// Failure returns zero, never a usable partial request or recipe.
func finalizeConstruction(v format.RequestV1, parameters format.ParametersV1, limits codec.Limits) (ConstructionResult, error) {
	encodedParameters, err := codec.Encode(parameters, limits)
	if err != nil {
		return ConstructionResult{}, err
	}
	request, err := finalizeRequest(v, limits)
	if err != nil {
		return ConstructionResult{}, err
	}
	check := request.CID().Binary()
	return ConstructionResult{
		request: request,
		recipe:  session.ConstructionRecipe{Algorithm: "chronological", Version: 1, Parameters: encodedParameters, Check: check[:]},
	}, nil
}
