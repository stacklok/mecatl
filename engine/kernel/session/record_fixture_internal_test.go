package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// format clones a persistence DTO for test mutation/export fixtures only.
func (r *materializedRecord) format() format.Record {
	if r == nil {
		return format.Record{}
	}
	return r.value.Clone()
}

// identifiedInput supplies explicit ordinals only at private construction/replay
// seams. Public authoring tests allocate them through SessionCommit instead.
type identifiedInput struct {
	ID    UserInputID
	Parts []ContentPart
}

func (v identifiedInput) authoring() UserInput              { return UserInput{RunID: v.ID.runID, Parts: v.Parts} }
func (v identifiedInput) toFormat() sessionformat.UserInput { return v.authoring().toFormat(v.ID) }
func (v identifiedInput) Clone() identifiedInput            { v.Parts = v.authoring().Clone().Parts; return v }
func makeInputRecord(v identifiedInput, limits codec.Limits) (*materializedRecord, error) {
	return newUserInputRecord(v.authoring(), v.ID, limits)
}

// identifiedRequest keeps a requested fixture ordinal separate from authoring.
// Typed construction checks nested scope; admission checks membership and order.
type identifiedRequest struct {
	ID      ModelRequestID
	Source  SourceHead
	Recipe  ConstructionRecipe
	RetryOf *ModelRequestID
}

func (v identifiedRequest) authoring() ModelRequest {
	return ModelRequest{RunID: v.ID.runID, Source: v.Source, Recipe: v.Recipe, RetryOf: v.RetryOf}
}
func (v identifiedRequest) toFormat() sessionformat.ModelRequest { return v.authoring().toFormat(v.ID) }
func makeRequestRecord(v identifiedRequest, limits codec.Limits) (*materializedRecord, error) {
	return newModelRequestRecord(v.authoring(), v.ID, limits)
}
