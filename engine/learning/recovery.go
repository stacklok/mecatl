package learning

import (
	"bytes"
	"encoding/json"
)

// ReflectionInput returns the verified source bindings behind a display projection.
// Serialized or modified display DTOs cannot establish evidence authority.
func (p Projection) ReflectionInput() (Input, error) {
	if p.verifiedInput == nil {
		return Input{}, ErrInvalidEvidence
	}
	canonical, err := ProjectInput(*p.verifiedInput)
	if err != nil {
		return Input{}, err
	}
	got, err := json.Marshal(p)
	if err != nil {
		return Input{}, err
	}
	want, err := json.Marshal(canonical)
	if err != nil || !bytes.Equal(got, want) {
		return Input{}, ErrInvalidEvidence
	}
	return *p.verifiedInput, nil
}
