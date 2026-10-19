package server

import (
	"fmt"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func executionFromProto(value *mecatlv1.ExecutionSelection) (ExecutionSelection, error) {
	if value == nil {
		return ExecutionSelection{}, nil
	}
	if len(value.ProtoReflect().GetUnknown()) != 0 {
		return ExecutionSelection{}, ErrInvalidArgument
	}
	if value.None != nil && value.Template == nil && len(value.None.ProtoReflect().GetUnknown()) == 0 {
		return ExecutionSelection{Kind: PlacementSelectorNoFS}, nil
	}
	if value.Template != nil && value.None == nil && len(value.Template.ProtoReflect().GetUnknown()) == 0 {
		e := ExecutionSelection{Kind: PlacementSelectorTemplate, ID: value.Template.GetId(), Revision: value.Template.GetRevision()}
		if _, _, err := e.placement(); err == nil {
			return e, nil
		}
	}
	return ExecutionSelection{}, fmt.Errorf("%w: execution must select exactly one valid variant", ErrInvalidArgument)
}
