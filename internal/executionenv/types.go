// Package executionenv defines the private, versioned host-side execution-provider protocol.
// It is intentionally not part of the public Harness API.
//
//nolint:revive // Private protocol types are exported only across internal adapter packages.
package executionenv

import "time"

// Private protocol bounds.
const (
	MaxMessageBytes  = 8 << 20
	MaxJSONBody      = 8 << 20
	MaxFileBytes     = 5 << 20
	MaxCommandBytes  = 1 << 20
	MaxPathBytes     = 4096
	MaxIdentityBytes = 1024
	MaxBindingBytes  = 253
	MaxListEntries   = 10_000
)

// Operation identifies one provider or workload-helper operation.
type Operation string

// Supported private-protocol operations.
const (
	OpAttach                 Operation = "attach"
	OpAcquireRun             Operation = "run.acquire"
	OpRenewRun               Operation = "run.renew"
	OpReleaseRun             Operation = "run.release"
	OpReferenceCommit        Operation = "reference.commit"
	OpReferenceAbort         Operation = "reference.abort"
	OpReferenceReserve       Operation = "reference.reserve"
	OpReferenceDeletePrepare Operation = "reference.delete.prepare"
	OpReferenceDeleteConfirm Operation = "reference.delete.confirm"
	OpReferenceDeleteCancel  Operation = "reference.delete.cancel"
	OpReferenceRelease       Operation = "reference.release"
	OpRetire                 Operation = "retire"
)

// Valid reports whether the operation belongs to the closed protocol vocabulary.
func (o Operation) Valid() bool {
	switch o {
	case OpAttach,
		OpAcquireRun,
		OpRenewRun,
		OpReleaseRun,
		OpReferenceCommit,
		OpReferenceAbort,
		OpReferenceReserve,
		OpReferenceDeletePrepare,
		OpReferenceDeleteConfirm,
		OpReferenceDeleteCancel,
		OpReferenceRelease,
		OpRetire:
		return true
	}
	return false
}

// EnvironmentRef is the provider-private durable environment identity.
type EnvironmentRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Owner is an authenticated principal attestation.
type Owner struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// RequestContext carries the exact authorization binding for an operation.
type RequestContext struct {
	Environment     EnvironmentRef `json:"environment"`
	Owner           Owner          `json:"owner"`
	BindingID       string         `json:"binding_id"`
	RunID           string         `json:"run_id"`
	ClaimID         string         `json:"claim_id"`
	Epoch           uint64         `json:"epoch"`
	GrantGeneration uint64         `json:"grant_generation"`
}

// EnsureEnvironmentResponse returns allocation identity and readiness.
type EnsureEnvironmentResponse struct {
	Environment          EnvironmentRef `json:"environment"`
	Epoch                uint64         `json:"epoch"`
	Ready                bool           `json:"ready"`
	GrantGeneration      uint64         `json:"grant_generation"`
	AdmittedCapabilities []string       `json:"admitted_capabilities"`
}

// AttachEnvironmentRequest requests exact reattachment to an existing environment.
type AttachEnvironmentRequest struct {
	Context RequestContext `json:"context"`
	Purpose string         `json:"purpose"`
}

// PurposeSession is the supported attachment purpose.
const PurposeSession = "session"

// AttachEnvironmentResponse returns exact attachment state.
type AttachEnvironmentResponse struct {
	Environment          EnvironmentRef `json:"environment"`
	Epoch                uint64         `json:"epoch"`
	Ready                bool           `json:"ready"`
	GrantGeneration      uint64         `json:"grant_generation"`
	AdmittedCapabilities []string       `json:"admitted_capabilities"`
}

const (
	MinRunTTL            = 30 * time.Second
	MaxRunTTL            = 5 * time.Minute
	DefaultRunTTL        = time.Minute
	DefaultRenewInterval = 20 * time.Second
)

type RunClaimRequest struct {
	Environment     EnvironmentRef
	Owner           Owner
	BindingID       string
	RunID           string
	ClaimID         string
	Epoch           uint64
	GrantGeneration uint64
	OperationID     string
	TTL             time.Duration
}

type RunClaim struct {
	Environment     EnvironmentRef
	BindingID       string
	RunID           string
	ClaimID         string
	Epoch           uint64
	GrantGeneration uint64
	ExpiresAt       time.Time
}

type ReferenceState string

const (
	ReferencePendingCreate ReferenceState = "PendingCreate"
	ReferencePublished     ReferenceState = "Published"
	ReferencePendingDelete ReferenceState = "PendingDelete"
)

type ReferenceRequest struct {
	Environment     EnvironmentRef
	Owner           Owner
	BindingID       string
	SourceBindingID string
	OperationID     string
}

type ReferenceIntent struct {
	Environment     EnvironmentRef
	Owner           Owner
	BindingID       string
	State           ReferenceState
	OperationID     string
	SourceBindingID string
	CreatedAt       time.Time
}

// ReferenceReleaseRequest releases one durable binding reference.
type ReferenceReleaseRequest struct {
	Context RequestContext `json:"context"`
}

// RetireEnvironmentRequest requests controlled retirement without PVC deletion.
type RetireEnvironmentRequest struct {
	Environment    EnvironmentRef `json:"environment"`
	Owner          Owner          `json:"owner"`
	ExpectedEpoch  uint64         `json:"expected_execution_epoch"`
	ExpectedPodUID string         `json:"expected_pod_uid"`
	ExpectedPVCUID string         `json:"expected_pvc_uid"`
	OperationID    string         `json:"operation_id"`
}

// EmptyResponse is the successful response for operations without a payload.
type EmptyResponse struct{}
