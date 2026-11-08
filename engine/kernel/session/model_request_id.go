package session

import "errors"

// ModelRequestID identifies one engine-visible invocation within a run. It is
// comparable and has no mutable storage. Zero is invalid. Allocation and inspection
// return scoped identities; [NewModelRequestID] reconstructs an identity value
// without allocating or establishing membership.
type ModelRequestID struct {
	// runID is the owning run, including history scope.
	runID RunID
	// requestOrdinal is the one-based request ordinal within RunID. Its distinct
	// field name prevents conversion from other occurrence identity kinds.
	requestOrdinal uint64
}

// RunID returns the enclosing run identity, or zero for zero.
func (v ModelRequestID) RunID() RunID { return v.runID }

// RunRelative returns this request's nonzero component within its enclosing
// run, or zero for a zero ModelRequestID. The component alone does not prove
// the request belongs to any run or was allocated there.
func (v ModelRequestID) RunRelative() uint64 { return v.requestOrdinal }

// NewModelRequestID reconstructs a structurally valid request identity from
// its enclosing run and nonzero local component. It does not allocate a
// request, establish run membership, or imply an allocation order.
func NewModelRequestID(runID RunID, component uint64) (ModelRequestID, error) {
	id := ModelRequestID{runID: runID, requestOrdinal: component}
	if err := id.Validate(); err != nil {
		return ModelRequestID{}, err
	}
	return id, nil
}

// Validate checks local identity validity, not accepted allocation or membership.
func (v ModelRequestID) Validate() error {
	if err := v.runID.Validate(); err != nil {
		return errors.New("invalid model request run ID")
	}
	if v.requestOrdinal == 0 {
		return errors.New("model request ordinal must be positive")
	}
	return nil
}

// Equal reports whether two run-scoped requests are identical.
func (v ModelRequestID) Equal(other ModelRequestID) bool { return v == other }

// RequestedCallID identifies one requested-call occurrence in a model response.
// It is comparable, contains no mutable storage, and has an invalid zero value.
// Obtain IDs by inspecting a response's requested calls.
type RequestedCallID struct {
	// requestID identifies the response that contains this call.
	requestID ModelRequestID
	// ordinal is the one-based call ordinal among requested calls in that response.
	ordinal uint64
}

// RequestID returns the enclosing request identity, or zero for zero.
func (v RequestedCallID) RequestID() ModelRequestID { return v.requestID }

// RequestRelative returns this call's nonzero component within its enclosing
// request, or zero for a zero RequestedCallID. The component alone does not
// prove the call belongs to any response or was accepted there.
func (v RequestedCallID) RequestRelative() uint64 { return v.ordinal }

// NewRequestedCallID reconstructs a structurally valid call identity from its
// enclosing request and nonzero local component. It does not allocate a call,
// establish response membership, or imply an allocation order.
func NewRequestedCallID(requestID ModelRequestID, component uint64) (RequestedCallID, error) {
	id := RequestedCallID{requestID: requestID, ordinal: component}
	if err := id.Validate(); err != nil {
		return RequestedCallID{}, err
	}
	return id, nil
}

// Validate checks local identity validity, not response membership or call order.
func (v RequestedCallID) Validate() error {
	if err := v.requestID.Validate(); err != nil {
		return errors.New("invalid requested call request ID")
	}
	if v.ordinal == 0 {
		return errors.New("requested call ordinal must be positive")
	}
	return nil
}

// Equal reports whether two requested-call occurrences are identical.
func (v RequestedCallID) Equal(other RequestedCallID) bool { return v == other }
