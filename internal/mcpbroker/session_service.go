package mcpbroker

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// SessionService is the transport-neutral broker-owned session contract.
// It does not expose owner assertions, creation correlation, execution tickets, or credentials.
// It is separate from Service and Attachment; those donor contracts remain broker-internal.
type SessionService interface {
	InspectConnectors(context.Context, SessionRef, CatalogueRef) (ConnectorInventory, error)
	OpenSession(context.Context, *session.BrokerSessionRef) (SessionSnapshot, error)
	InvokeTool(context.Context, session.BrokerSessionRef, session.BrokerCatalogueRef, Call) (InvocationOutcome, error)
	BeginAuthorization(context.Context, session.BrokerSessionRef, AuthorizationRef) (BrowserPrompt, error)
	ObserveAuthorization(context.Context, session.BrokerSessionRef, AuthorizationRef) (FlowStatus, error)
	CancelAuthorization(context.Context, session.BrokerSessionRef, AuthorizationRef) (CancelResult, error)
	ResumeTool(context.Context, session.BrokerSessionRef, AuthorizationRef, session.BrokerCatalogueRef) (InvocationOutcome, error)
	BeginEnrollment(context.Context, session.BrokerSessionRef) (BeginEnrollmentOutcome, error)
	ObserveEnrollment(context.Context, session.BrokerSessionRef, EnrollmentRef) (FlowStatus, error)
	CancelEnrollment(context.Context, session.BrokerSessionRef, EnrollmentRef) (CancelResult, error)
	DisconnectTools(context.Context, session.BrokerSessionRef, session.BrokerCatalogueRef) (DisconnectResult, error)
	DeleteSession(context.Context, session.BrokerSessionRef) (DeleteResult, error)
}

// AuthorizationCheck is nonexecuting. Ready is not dispatch or replay authority.
type AuthorizationCheck struct {
	Ready         bool
	Authorization AuthorizationRef
	ExpiresAt     time.Time
	Reason        FailureReason
}

func (a AuthorizationCheck) Valid() bool {
	if a.Ready {
		return a.Authorization == "" && a.ExpiresAt.IsZero() && a.Reason == FailureUnspecified
	}
	if a.Authorization != "" {
		return validBrokerRef(string(a.Authorization)) && !a.ExpiresAt.IsZero() && a.Reason == FailureUnspecified
	}
	return a.ExpiresAt.IsZero() && a.Reason.Valid()
}

// SessionRef and CatalogueRef are stable engine-owned public references.
type SessionRef = session.BrokerSessionRef
type CatalogueRef = session.BrokerCatalogueRef

// AuthorizationRef identifies a broker-owned authorization flow.
type AuthorizationRef string

// EnrollmentRef identifies a broker-owned enrollment flow.
type EnrollmentRef string

// SessionSnapshot is the current broker session and its catalogue.
type SessionSnapshot struct {
	Ref       session.BrokerSessionRef
	ExpiresAt time.Time
	Catalogue Catalogue
}

// Call carries the exact prepared invocation.
type Call struct {
	ID        session.ToolCallID
	Name      string
	Arguments []byte
}

// BrowserPrompt is an ephemeral HTTPS presentation, never durable state.
type BrowserPrompt struct {
	URL       string
	ExpiresAt time.Time
}

// Valid checks the presentation URL and expiry metadata, not whether it is still live.
func (p BrowserPrompt) Valid() bool {
	if len(p.URL) == 0 || len(p.URL) > 8*1024 || !utf8.ValidString(p.URL) || p.ExpiresAt.IsZero() {
		return false
	}
	u, err := url.ParseRequestURI(p.URL)
	return err == nil && u.IsAbs() && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// validBrokerRef checks the canonical unpadded base64url encoding of 32 bytes.
func validBrokerRef(ref string) bool {
	if len(ref) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(ref)
	return err == nil && len(decoded) == 32
}

// FailureReason is a safe, closed non-dispatch or flow failure reason.
type FailureReason uint8

const (
	// FailureUnspecified is not a valid failure.
	FailureUnspecified FailureReason = iota
	// FailureCatalogueChanged means the prepared descriptor no longer matches.
	FailureCatalogueChanged
	// FailureAuthorityWithdrawn means tool authority was revoked.
	FailureAuthorityWithdrawn
	// FailureCapacity means broker admission was exhausted.
	FailureCapacity
	// FailureInterrupted means process-local flow state was lost.
	FailureInterrupted
	// FailureAuthorizationFailed means the upstream grant failed.
	FailureAuthorizationFailed
	// FailureCallChanged means the prepared call no longer matches.
	FailureCallChanged
	// FailureExpired means the attempt expired.
	FailureExpired
)

// Valid reports whether the failure reason is defined.
func (r FailureReason) Valid() bool { return r >= FailureCatalogueChanged && r <= FailureExpired }

// InvocationKind selects one closed invocation outcome.
type InvocationKind string

const (
	// InvocationCompleted carries a tool result.
	InvocationCompleted InvocationKind = "completed"
	// InvocationAuthorizationRequired carries a browser-flow reference.
	InvocationAuthorizationRequired InvocationKind = "authorization_required"
	// InvocationNotDispatched carries a reason, never transport-replay authority in this PoC.
	InvocationNotDispatched InvocationKind = "not_dispatched"
	// InvocationOutcomeUnknown means dispatch cannot be ruled out.
	InvocationOutcomeUnknown InvocationKind = "outcome_unknown"
)

// InvocationOutcome carries exactly one completed, authorization, non-dispatch, or unknown arm.
type InvocationOutcome struct {
	Kind          InvocationKind
	Result        *session.ToolResult
	Authorization AuthorizationRef
	Reason        FailureReason
}

// Valid checks the selected invocation arm and its payload, not execution provenance.
func (o InvocationOutcome) Valid() bool {
	switch o.Kind {
	case InvocationCompleted:
		return o.Authorization == "" && o.Reason == FailureUnspecified && validBrokerResult(o.Result)
	case InvocationAuthorizationRequired:
		return o.Result == nil && validBrokerRef(string(o.Authorization)) && o.Reason == FailureUnspecified
	case InvocationNotDispatched:
		return o.Result == nil && o.Authorization == "" && o.Reason.Valid()
	case InvocationOutcomeUnknown:
		return o.Result == nil && o.Authorization == "" && o.Reason == FailureUnspecified
	default:
		return false
	}
}

func validBrokerResult(result *session.ToolResult) bool {
	if result == nil || len(result.CallID) == 0 || len(result.CallID) > 256 || !utf8.ValidString(string(result.CallID)) || !utf8.ValidString(result.Content) || session.ValidateToolResultParts(result.Parts) != nil {
		return false
	}
	for _, part := range result.Parts {
		if !validBrokerPart(part) {
			return false
		}
	}
	return true
}

func validBrokerPart(part session.Content) bool {
	if !utf8.ValidString(part.Text) || !utf8.ValidString(part.URL) || !utf8.ValidString(part.MIMEType) || !utf8.ValidString(part.Name) || !utf8.ValidString(part.Title) || !utf8.ValidString(part.Description) || !utf8.ValidString(part.LastModified) {
		return false
	}
	for _, audience := range part.Audience {
		if !utf8.ValidString(audience) {
			return false
		}
	}
	switch part.BlockKind {
	case session.BlockText, session.BlockStructuredContent:
		return part.Kind == "" && len(part.Data) == 0 && part.URL == ""
	case session.BlockImage, session.BlockAudio:
		kind := session.MediaImage
		if part.BlockKind == session.BlockAudio {
			kind = session.MediaAudio
		}
		if part.Kind != kind {
			return false
		}
		_, err := session.NewContent(kind, part.MIMEType, part.Data, part.URL)
		return err == nil
	case session.BlockEmbeddedResource:
		return (len(part.Data) == 0) != (part.Text == "")
	case session.BlockResourceLink:
		return part.URL != "" && len(part.Data) == 0 && part.Text == ""
	default:
		return false
	}
}

var errInvalidOutcome = errors.New("invalid broker outcome")

// NewInvocationOutcome validates the selected arm and copies its result payload.
func NewInvocationOutcome(kind InvocationKind, result *session.ToolResult, auth AuthorizationRef, reason FailureReason) (InvocationOutcome, error) {
	o := InvocationOutcome{Kind: kind, Result: result, Authorization: auth, Reason: reason}
	if !o.Valid() {
		return InvocationOutcome{}, errInvalidOutcome
	}
	if result != nil {
		copyResult := *result
		copyResult.Parts = make([]session.Content, len(result.Parts))
		for i, part := range result.Parts {
			part.Data = append([]byte(nil), part.Data...)
			part.Audience = append([]string(nil), part.Audience...)
			copyResult.Parts[i] = part
		}
		o.Result = &copyResult
	}
	return o, nil
}

// FlowKind selects one closed browser-flow status.
type FlowKind string

const (
	// FlowPending means the browser flow is still active.
	FlowPending FlowKind = "pending"
	// FlowCompleted carries a replacement catalogue.
	FlowCompleted FlowKind = "completed"
	// FlowCancelled means the flow was cancelled.
	FlowCancelled FlowKind = "cancelled"
	// FlowExpired means the flow timed out.
	FlowExpired FlowKind = "expired"
	// FlowFailed carries a safe failure reason.
	FlowFailed FlowKind = "failed"
)

// FlowStatus carries exactly one browser-flow state.
type FlowStatus struct {
	Kind      FlowKind
	Catalogue Catalogue
	Reason    FailureReason
}

// Valid checks the selected flow arm and its payload.
func (s FlowStatus) Valid() bool {
	switch s.Kind {
	case FlowPending, FlowCancelled, FlowExpired:
		return s.Catalogue == nil && s.Reason == FailureUnspecified
	case FlowCompleted:
		return s.Catalogue != nil && s.Catalogue.Valid() && s.Reason == FailureUnspecified
	case FlowFailed:
		return s.Catalogue == nil && s.Reason.Valid()
	default:
		return false
	}
}

// NewFlowStatus validates a browser-flow status.
func NewFlowStatus(kind FlowKind, catalogue Catalogue, reason FailureReason) (FlowStatus, error) {
	s := FlowStatus{Kind: kind, Catalogue: catalogue, Reason: reason}
	if !s.Valid() {
		return FlowStatus{}, errInvalidOutcome
	}
	return s, nil
}

// CancelResult is a closed cancellation outcome.
type CancelResult uint8

const (
	// CancelUnspecified is not a valid cancellation result.
	CancelUnspecified CancelResult = iota
	// Cancelled means the flow was cancelled.
	Cancelled
	// AlreadyResolved means no pending flow remains.
	AlreadyResolved
)

// Valid reports whether the cancellation outcome is defined.
func (r CancelResult) Valid() bool { return r >= Cancelled && r <= AlreadyResolved }

// DisconnectResult is a closed catalogue-authority withdrawal outcome.
type DisconnectResult uint8

const (
	// DisconnectUnspecified is not a valid disconnect result.
	DisconnectUnspecified DisconnectResult = iota
	// Disconnected means the catalogue's connection authority was withdrawn.
	Disconnected
	// AlreadyDisconnected means there was no tool authority to withdraw.
	AlreadyDisconnected
	// CatalogueChanged means the expected catalogue no longer identifies the current connection authority.
	CatalogueChanged
)

// Valid reports whether the disconnect outcome is defined.
func (r DisconnectResult) Valid() bool { return r >= Disconnected && r <= CatalogueChanged }

// DeleteResult is a closed session deletion outcome.
type DeleteResult uint8

const (
	// DeleteUnspecified is not a valid deletion result.
	DeleteUnspecified DeleteResult = iota
	// Deleted means the session was deleted.
	Deleted
	// AlreadyAbsent means the session was already absent.
	AlreadyAbsent
)

// Valid reports whether the deletion outcome is defined.
func (r DeleteResult) Valid() bool { return r >= Deleted && r <= AlreadyAbsent }

// EnrollmentKind selects one closed begin-enrollment outcome.
type EnrollmentKind string

const (
	// EnrollmentStartedKind carries a pending enrollment and browser prompt.
	EnrollmentStartedKind EnrollmentKind = "started"
	// EnrollmentAlreadyConnected means the session has a connection.
	EnrollmentAlreadyConnected EnrollmentKind = "already_connected"
	// EnrollmentCompletedKind carries a synchronously committed catalogue.
	EnrollmentCompletedKind EnrollmentKind = "completed"
)

// EnrollmentStarted carries the exact enrollment reference and prompt.
type EnrollmentStarted struct {
	Ref    EnrollmentRef
	Prompt BrowserPrompt
}

// BeginEnrollmentOutcome carries exactly one enrollment-start arm.
type BeginEnrollmentOutcome struct {
	Kind      EnrollmentKind
	Started   *EnrollmentStarted
	Catalogue Catalogue
}

// Valid checks the selected enrollment arm and its prompt.
func (o BeginEnrollmentOutcome) Valid() bool {
	switch o.Kind {
	case EnrollmentStartedKind:
		return o.Catalogue == nil && o.Started != nil && validBrokerRef(string(o.Started.Ref)) && o.Started.Prompt.Valid()
	case EnrollmentAlreadyConnected:
		return o.Started == nil && o.Catalogue == nil
	case EnrollmentCompletedKind:
		return o.Started == nil && o.Catalogue != nil && o.Catalogue.Valid()
	default:
		return false
	}
}

// NewBeginEnrollmentOutcome validates and copies the selected enrollment arm.
func NewBeginEnrollmentOutcome(kind EnrollmentKind, started *EnrollmentStarted, catalogue Catalogue) (BeginEnrollmentOutcome, error) {
	o := BeginEnrollmentOutcome{Kind: kind, Started: started, Catalogue: catalogue}
	if !o.Valid() {
		return BeginEnrollmentOutcome{}, errInvalidOutcome
	}
	if started != nil {
		copyStarted := *started
		o.Started = &copyStarted
	}
	return o, nil
}
