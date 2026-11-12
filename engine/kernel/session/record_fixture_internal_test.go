package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// format clones a persistence DTO for test mutation/export fixtures only.
func (r *materializedRecord) format() format.Record {
	if r == nil {
		return format.Record{}
	}
	return r.value.Clone()
}

// identifiedInput pairs a canonical payload with an allocated fixture identity.
type identifiedInput struct {
	ID UserInputID
	UserInput
}

func makeInputRecord(v identifiedInput, limits codec.Limits) (*materializedRecord, error) {
	return newUserInputRecord(v.UserInput, v.ID, limits)
}

// identifiedRequest pairs a canonical payload with an allocated fixture identity.
type identifiedRequest struct {
	ID ModelRequestID
	ModelRequest
}

func makeRequestRecord(v identifiedRequest, limits codec.Limits) (*materializedRecord, error) {
	return newModelRequestRecord(v.ModelRequest, v.ID, limits)
}
