package mcpbrokergrpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func descriptors(in []tool.Tool) ([]*brokerv1.ToolDescriptor, map[string]tool.Tool, error) {
	out := make([]*brokerv1.ToolDescriptor, 0, len(in))
	tools := make(map[string]tool.Tool, len(in))
	for _, t := range in {
		if t == nil {
			return nil, nil, errors.New("nil tool")
		}
		spec := t.Spec()
		if spec.Name == "" || !utf8.ValidString(spec.Name) || !utf8.ValidString(spec.Description) || !validJSONObject(spec.Schema) {
			return nil, nil, errors.New("invalid tool descriptor")
		}
		if _, ok := tools[spec.Name]; ok {
			return nil, nil, errors.New("duplicate tool descriptor")
		}
		_, serial := t.(tool.DispatchSerial)
		_, auth := t.(tool.AuthorizationRequester)
		out = append(out, &brokerv1.ToolDescriptor{Name: spec.Name, Description: spec.Description, Schema: append([]byte(nil), spec.Schema...), ReadOnly: t.ReadOnly(), DispatchSerial: serial, AuthorizationCapable: auth})
		tools[spec.Name] = t
	}
	return out, tools, nil
}
func validJSONObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
func newHandle() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }

func reasonStatus(code codes.Code, message string, reason brokerv1.BrokerErrorReason, method string) error {
	st := status.New(code, message)
	withDetail, err := st.WithDetails(&brokerv1.BrokerErrorDetail{Reason: reason, DispatchMethod: method})
	if err != nil {
		return status.Error(codes.Internal, "encode broker error reason")
	}
	return withDetail.Err()
}

func continuityUnavailable() error {
	return reasonStatus(codes.FailedPrecondition, "broker continuity is not available", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE, "")
}

func brokerStatus(err error) error {
	switch {
	case errors.Is(err, mcpbroker.ErrBrokerIncarnationLost):
		return reasonStatus(codes.FailedPrecondition, "broker incarnation mismatch", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST, "")
	case errors.Is(err, mcpbroker.ErrContinuityUnavailable):
		return continuityUnavailable()

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, mcpbroker.ErrAttachmentClosed):
		return reasonStatus(codes.FailedPrecondition, "attachment closed", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED, "")
	case errors.Is(err, mcpbroker.ErrStateUnavailable):
		return reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	case errors.Is(err, mcpbroker.ErrCapacity):
		return reasonStatus(codes.ResourceExhausted, "broker capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
	case errors.Is(err, mcpbroker.ErrAuthorizationNotFound):
		return reasonStatus(codes.NotFound, "authorization not found", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND, "")
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
func validAttachOutcome(v string) bool {
	return v == string(mcpbroker.AttachCreated) || v == string(mcpbroker.AttachReattached) || v == string(mcpbroker.AttachRecoveredProvisional)
}
func brokerReason(err error) (brokerv1.BrokerErrorReason, string, bool, error) {
	var found *brokerv1.BrokerErrorDetail
	for _, detail := range status.Convert(err).Details() {
		candidate, ok := detail.(*brokerv1.BrokerErrorDetail)
		if !ok {
			continue
		}
		if found != nil {
			return 0, "", false, errors.New("mcpbrokergrpc: multiple broker error reasons")
		}
		found = candidate
	}
	if found == nil {
		return 0, "", false, nil
	}
	switch found.GetReason() {
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND,
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE:
		if found.GetDispatchMethod() != "" {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed broker error reason")
		}
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		if found.GetDispatchMethod() != "" && found.GetDispatchMethod() != executeMethod {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed capacity reason")
		}
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED:
		if found.GetDispatchMethod() != executeMethod {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed dispatch proof")
		}
	default:
		return 0, "", false, errors.New("mcpbrokergrpc: unknown broker error reason")
	}
	return found.GetReason(), found.GetDispatchMethod(), true, nil
}

func dispatchNotStarted(err error) bool {
	reason, method, ok, protocolErr := brokerReason(err)
	return protocolErr == nil && ok && method == executeMethod &&
		(reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED ||
			reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED)
}

func isDefinitiveSessionLoss(err error) bool {
	reason, _, ok, protocolErr := brokerReason(err)
	return protocolErr == nil && ok && (reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE || reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST)
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	reason, _, ok, protocolErr := brokerReason(err)
	if protocolErr != nil {
		return protocolErr
	}
	if !ok {
		return err
	}
	switch reason {
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE:
		return mcpbroker.ErrStateUnavailable
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST:
		st := status.Convert(err)
		if st.Code() != codes.FailedPrecondition || st.Message() != "broker incarnation mismatch" {
			return errors.New("mcpbrokergrpc: malformed broker incarnation lost reason")
		}
		return errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED:
		return mcpbroker.ErrAttachmentClosed
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_AUTHORIZATION_NOT_FOUND:
		return mcpbroker.ErrAuthorizationNotFound
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE:
		st := status.Convert(err)
		if st.Code() != codes.FailedPrecondition || st.Message() != "broker continuity is not available" {
			return errors.New("mcpbrokergrpc: malformed continuity unavailable reason")
		}
		return mcpbroker.ErrContinuityUnavailable
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		return mcpbroker.ErrCapacity
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED:
		return err
	default:
		return errors.New("mcpbrokergrpc: unknown broker error reason")
	}
}

const (
	maxInvocationCallIDBytes = 256
	maxInvocationNameBytes   = 256
	maxInvocationItemIDBytes = 1024
	maxInvocationArgsBytes   = 256 << 10
)

func callFrom(name, id string, args []byte, item string) (session.ToolCall, error) {
	if !validInvocationText(name, maxInvocationNameBytes) || !validInvocationText(id, maxInvocationCallIDBytes) || (item != "" && !validInvocationText(item, maxInvocationItemIDBytes)) || len(args) == 0 || len(args) > maxInvocationArgsBytes || !json.Valid(args) {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(args, &object) != nil {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: name, Args: append([]byte(nil), args...), ItemID: item}, nil
}

func validInvocationText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func resultToWire(r session.ToolResult) (*brokerv1.ToolResult, error) {
	if r.CallID == "" || !utf8.ValidString(string(r.CallID)) || !utf8.ValidString(r.Content) || !validParts(r.Parts) {
		return nil, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]*brokerv1.ResultPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		parts = append(parts, &brokerv1.ResultPart{BlockKind: string(p.BlockKind), MediaKind: string(p.Kind), MimeType: p.MIMEType, Data: append([]byte(nil), p.Data...), Url: p.URL, Text: p.Text, Name: p.Name, Title: p.Title, Description: p.Description, Size: p.Size, Audience: append([]string(nil), p.Audience...), Priority: p.Priority, LastModified: p.LastModified})
	}
	return &brokerv1.ToolResult{CallId: string(r.CallID), Content: r.Content, IsError: r.IsError, Parts: parts}, nil
}
func resultFromWire(r *brokerv1.ToolResult) (session.ToolResult, error) {
	if r == nil || r.GetCallId() == "" || !utf8.ValidString(r.GetCallId()) || !utf8.ValidString(r.GetContent()) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]session.Content, 0, len(r.GetParts()))
	for _, p := range r.GetParts() {
		if p == nil {
			return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed result part")
		}
		q := session.Content{BlockKind: session.BlockKind(p.GetBlockKind()), Kind: session.MediaKind(p.GetMediaKind()), MIMEType: p.GetMimeType(), Data: append([]byte(nil), p.GetData()...), URL: p.GetUrl(), Text: p.GetText(), Name: p.GetName(), Title: p.GetTitle(), Description: p.GetDescription(), Size: p.GetSize(), Audience: append([]string(nil), p.GetAudience()...), Priority: p.GetPriority(), LastModified: p.GetLastModified()}
		parts = append(parts, q)
	}
	if !validParts(parts) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	return session.ToolResult{CallID: session.ToolCallID(r.GetCallId()), Content: r.GetContent(), IsError: r.GetIsError(), Parts: parts}, nil
}
func validParts(parts []session.Content) bool {
	for _, p := range parts {
		for _, s := range []string{string(p.BlockKind), string(p.Kind), p.MIMEType, p.URL, p.Text, p.Name, p.Title, p.Description, p.LastModified} {
			if !utf8.ValidString(s) {
				return false
			}
		}
		for _, a := range p.Audience {
			if !utf8.ValidString(a) {
				return false
			}
		}
	}
	return session.ValidateToolResultParts(parts) == nil
}
func authToWire(a session.ExternalAuthorization) *brokerv1.Authorization {
	return &brokerv1.Authorization{Id: a.ID, Binding: string(a.Binding), ExpiresAt: timestamppb.New(a.ExpiresAt)}
}
func authFromWire(a *brokerv1.Authorization) (session.ExternalAuthorization, error) {
	if a == nil || a.GetId() == "" || a.GetBinding() == "" || !utf8.ValidString(a.GetId()) || !utf8.ValidString(a.GetBinding()) || a.GetExpiresAt() == nil || !a.GetExpiresAt().IsValid() {
		return session.ExternalAuthorization{}, errors.New("mcpbrokergrpc: malformed authorization")
	}
	return session.ExternalAuthorization{ID: a.GetId(), Binding: session.AuthorizationBinding(a.GetBinding()), ExpiresAt: a.GetExpiresAt().AsTime()}, nil
}

func workspaceRefToWire(ref mcpbroker.WorkspaceEnrollmentRef) *brokerv1.WorkspaceRef {
	return &brokerv1.WorkspaceRef{Id: string(ref.ID), RequiredServices: ref.RequiredServices, ExpiresAt: timestamppb.New(ref.ExpiresAt)}
}
func workspaceRefFromWire(ref *brokerv1.WorkspaceRef) (mcpbroker.WorkspaceEnrollmentRef, error) {
	if ref == nil || ref.GetExpiresAt() == nil || !ref.GetExpiresAt().IsValid() {
		return mcpbroker.WorkspaceEnrollmentRef{}, invalid("malformed workspace reference")
	}
	out := mcpbroker.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID(ref.GetId()), RequiredServices: ref.GetRequiredServices(), ExpiresAt: ref.GetExpiresAt().AsTime()}
	if !out.Valid() {
		return mcpbroker.WorkspaceEnrollmentRef{}, invalid("malformed workspace reference")
	}
	return out, nil
}

type workspaceResultResponse interface {
	GetRef() *brokerv1.WorkspaceRef
	GetStatus() string
	GetTools() []*brokerv1.ToolDescriptor
}

func workspaceResultFromWire(c *Client, handle, instanceID string, r workspaceResultResponse) (mcpbroker.WorkspaceEnrollmentResult, error) {
	if r == nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, errors.New("mcpbrokergrpc: malformed workspace result")
	}
	ref, err := workspaceRefFromWire(r.GetRef())
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, err
	}
	out := mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentStatus(r.GetStatus())}
	if out.Status == mcpbroker.WorkspaceEnrollmentConnected {
		attach := &brokerv1.AttachResponse{Handle: handle, BrokerIncarnation: instanceID, Tools: r.GetTools()}
		tools, toolsErr := remoteTools(c, attach)
		if toolsErr != nil {
			return mcpbroker.WorkspaceEnrollmentResult{}, toolsErr
		}
		out.Catalogue, err = mcpbroker.NewWorkspaceCatalogue(ref, tools)
		if err != nil {
			return mcpbroker.WorkspaceEnrollmentResult{}, err
		}
	}
	if !out.Valid() {
		return mcpbroker.WorkspaceEnrollmentResult{}, errors.New("mcpbrokergrpc: malformed workspace result")
	}
	return out, nil
}
func validAuthorizationStatus(s session.AuthorizationStatus) bool {
	switch s {
	case session.AuthorizationPending, session.AuthorizationGranted, session.AuthorizationDenied,
		session.AuthorizationCancelled, session.AuthorizationExpired, session.AuthorizationInterrupted,
		session.AuthorizationFailed, session.AuthorizationClosed:
		return true
	default:
		return false
	}
}
func validCancelOutcome(out mcpbroker.CancelOutcome) bool {
	return out == mcpbroker.CancelCancelled || out == mcpbroker.CancelAlreadyCancelled || out == mcpbroker.CancelAlreadyResolved
}
