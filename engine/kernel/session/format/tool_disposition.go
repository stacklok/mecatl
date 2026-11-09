package format

import "errors"

// ToolDispositionKind names the closed derived disposition branches; it is not
// a redundant persisted tag. Inspection derives the kind from branch presence.
type ToolDispositionKind string

const (
	// ToolDispositionReturned identifies an observed terminal tool return.
	ToolDispositionReturned ToolDispositionKind = "returned"
	// ToolDispositionNotInvoked identifies a known non-invocation.
	ToolDispositionNotInvoked ToolDispositionKind = "not-invoked"
	// ToolDispositionIndeterminate identifies an unobserved terminal outcome.
	ToolDispositionIndeterminate ToolDispositionKind = "indeterminate"
)

// ToolReturnStatus is the closed tool-result meaning, not execution mechanics or
// rollback semantics. Empty and unknown values are invalid.
type ToolReturnStatus string

const (
	// ToolReturnSuccess reports tool-level success without asserting rollback behavior.
	ToolReturnSuccess ToolReturnStatus = "success"
	// ToolReturnError reports tool-level error without asserting absent side effects.
	ToolReturnError ToolReturnStatus = "error"
)

// ToolReturned retains observed terminal output. Zero is invalid. Assignment
// shares output storage; Clone detaches it.
//
//dtogen:validate validateLocal
type ToolReturned struct {
	// Status is required tool-level success/error, at most 32 UTF-8 bytes.
	Status ToolReturnStatus `cbor:"s" cborlimit:"bytes=32"`
	// Output is required capture, including explicit complete empty output.
	Output ToolOutput `cbor:"o"`
}

// Validate checks status and captured output, not aggregate commitment.
func (v ToolReturned) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	return v.Output.Validate()
}

// validateLocal checks the immediate closed status.
func (v ToolReturned) validateLocal() error {
	switch v.Status {
	case ToolReturnSuccess, ToolReturnError:
		return nil
	}
	return errors.New("unknown tool return status")
}

// ToolNotInvokedReason is the closed known non-invocation vocabulary.
// Empty and unknown values are invalid.
type ToolNotInvokedReason string

const (
	// ToolNotInvokedPolicyRejected reports policy rejection.
	ToolNotInvokedPolicyRejected ToolNotInvokedReason = "policy-rejected"
	// ToolNotInvokedPreparationFailed reports failed preparation.
	ToolNotInvokedPreparationFailed ToolNotInvokedReason = "preparation-failed"
	// ToolNotInvokedCancelled reports cancellation before invocation.
	ToolNotInvokedCancelled ToolNotInvokedReason = "cancelled"
	// ToolNotInvokedOther reports another known non-invocation reason.
	ToolNotInvokedOther ToolNotInvokedReason = "other"
)

// ToolNotInvoked retains a known non-invocation, before or after commitment.
// Zero is invalid; producer detail belongs in ToolDisposition.Evidence.
//
//dtogen:validate validateLocal
type ToolNotInvoked struct {
	// Reason is required closed-vocabulary non-invocation meaning, at most 32 bytes.
	Reason ToolNotInvokedReason `cbor:"r" cborlimit:"bytes=32"`
}

// Validate checks the closed non-invocation reason only.
func (v ToolNotInvoked) Validate() error { return v.validateLocal() }

// validateLocal checks the immediate reason.
func (v ToolNotInvoked) validateLocal() error {
	switch v.Reason {
	case ToolNotInvokedPolicyRejected, ToolNotInvokedPreparationFailed, ToolNotInvokedCancelled, ToolNotInvokedOther:
		return nil
	}
	return errors.New("unknown tool non-invocation reason")
}

// ToolIndeterminateReason is the closed unobserved-terminal-outcome vocabulary.
// Empty and unknown values are invalid.
type ToolIndeterminateReason string

const (
	// ToolIndeterminateTransport reports transport uncertainty.
	ToolIndeterminateTransport ToolIndeterminateReason = "transport"
	// ToolIndeterminateTimeout reports timeout uncertainty.
	ToolIndeterminateTimeout ToolIndeterminateReason = "timeout"
	// ToolIndeterminateExecutorLost reports loss of the executor.
	ToolIndeterminateExecutorLost ToolIndeterminateReason = "executor-lost"
	// ToolIndeterminateCancelled reports cancellation with uncertain outcome.
	ToolIndeterminateCancelled ToolIndeterminateReason = "cancelled"
	// ToolIndeterminateOther reports another uncertainty reason.
	ToolIndeterminateOther ToolIndeterminateReason = "other"
)

// ToolIndeterminate retains uncertainty, not a retry or started record. It cannot
// assert complete terminal output without an observed terminal outcome. Zero is
// invalid. Assignment shares output storage; Clone detaches it.
//
//dtogen:validate validateLocal
type ToolIndeterminate struct {
	// Reason is required closed-vocabulary uncertainty meaning, at most 32 bytes.
	Reason ToolIndeterminateReason `cbor:"r" cborlimit:"bytes=32"`
	// Output is required truncated or omitted capture, possibly empty.
	Output ToolOutput `cbor:"o"`
}

// Validate checks reason, incomplete capture and all output children.
func (v ToolIndeterminate) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	return v.Output.Validate()
}

// validateLocal checks reason and capture applicability, not child validity.
func (v ToolIndeterminate) validateLocal() error {
	switch v.Reason {
	case ToolIndeterminateTransport, ToolIndeterminateTimeout, ToolIndeterminateExecutorLost, ToolIndeterminateCancelled, ToolIndeterminateOther:
	default:
		return errors.New("unknown tool indeterminate reason")
	}
	if v.Output.Capture != ToolCaptureTruncated && v.Output.Capture != ToolCaptureOmitted {
		return errors.New("indeterminate tool output must be truncated or omitted")
	}
	return nil
}

// ToolDisposition retains exactly one outcome fact for a requested call, not an
// execution mechanism. Assignment shares nested storage; Clone detaches it.
// Zero is invalid; history scope is omitted. Reload grants no execution authority.
//
// Aggregate obligations (not implemented by this DTO): Returned requires accepted
// commitment and settled prerequisites; Indeterminate requires accepted commitment
// even if prerequisites are unsettled; NotInvoked may precede or follow commitment.
// At most one invocation and disposition fact per call, and no late facts after
// ending, must be enforced by the aggregate, not this validator.
//
//dtogen:include
//dtogen:validate validateLocal
type ToolDisposition struct {
	// RunID is the required positive run component within the enclosing history.
	RunID uint64 `cbor:"r" view:"-"`
	// RequestID is the required positive request component within RunID.
	RequestID uint64 `cbor:"q" view:"-"`
	// CallID is the required positive requested-call component within the response
	// to RequestID; positivity does not establish membership or authority.
	CallID uint64 `cbor:"c" view:"manual"`
	// Returned optionally retains observed terminal return, exclusive with other branches.
	Returned *ToolReturned `cbor:"t,omitempty"`
	// NotInvoked optionally retains known non-invocation, exclusive with other branches.
	NotInvoked *ToolNotInvoked `cbor:"n,omitempty"`
	// Indeterminate optionally retains uncertain outcome, exclusive with other branches.
	Indeterminate *ToolIndeterminate `cbor:"i,omitempty"`
	// Evidence optionally retains small producer diagnostic detail, not model content.
	Evidence *ToolEvidence `cbor:"e,omitempty"`
}

// Validate checks persisted identity, branch exclusivity and all children, not
// lifecycle, accepted commitment, prerequisites or aggregate cardinality.
func (v ToolDisposition) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Returned != nil {
		if err := v.Returned.Validate(); err != nil {
			return err
		}
	}
	if v.NotInvoked != nil {
		if err := v.NotInvoked.Validate(); err != nil {
			return err
		}
	}
	if v.Indeterminate != nil {
		if err := v.Indeterminate.Validate(); err != nil {
			return err
		}
	}
	if v.Evidence != nil {
		return v.Evidence.Validate()
	}
	return nil
}

// validateLocal checks immediate persisted IDs and branch presence only.
func (v ToolDisposition) validateLocal() error {
	if v.RunID == 0 || v.RequestID == 0 || v.CallID == 0 {
		return errors.New("tool disposition requires positive run, request and call components")
	}
	n := 0
	if v.Returned != nil {
		n++
	}
	if v.NotInvoked != nil {
		n++
	}
	if v.Indeterminate != nil {
		n++
	}
	if n != 1 {
		return errors.New("tool disposition requires exactly one branch")
	}
	return nil
}
