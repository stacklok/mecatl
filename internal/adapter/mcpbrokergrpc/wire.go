package mcpbrokergrpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func newHandle() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }

//nolint:unparam // method is set by Execute RPCs.
func reasonStatus(code codes.Code, message string, reason brokerv1.BrokerErrorReason, method string) error {
	st := status.New(code, message)
	withDetail, err := st.WithDetails(&brokerv1.BrokerErrorDetail{Reason: reason, DispatchMethod: method})
	if err != nil {
		return status.Error(codes.Internal, "encode broker error reason")
	}
	return withDetail.Err()
}

func brokerStatus(err error) error {
	switch {
	case errors.Is(err, mcpbroker.ErrBrokerIncarnationLost):
		return reasonStatus(codes.FailedPrecondition, "broker incarnation mismatch", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST, "")
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
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
func validAttachOutcome(v string) bool {
	return v == string(mcpbroker.AttachCreated) || v == string(mcpbroker.AttachReattached)
}

//nolint:unparam // dispatch method is read by Execute RPCs.
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
		brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED:
		if found.GetDispatchMethod() != "" {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed broker error reason")
		}
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		if found.GetDispatchMethod() != "" {
			return 0, "", false, errors.New("mcpbrokergrpc: malformed capacity reason")
		}
	default:
		return 0, "", false, errors.New("mcpbrokergrpc: unknown broker error reason")
	}
	return found.GetReason(), found.GetDispatchMethod(), true, nil
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
		if status.Code(err) != codes.FailedPrecondition {
			return errors.New("mcpbrokergrpc: malformed broker incarnation lost reason")
		}
		return errors.Join(mcpbroker.ErrStateUnavailable, mcpbroker.ErrBrokerIncarnationLost)
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_ATTACHMENT_CLOSED:
		return mcpbroker.ErrAttachmentClosed
	case brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED:
		return mcpbroker.ErrCapacity
	default:
		return errors.New("mcpbrokergrpc: unknown broker error reason")
	}
}
