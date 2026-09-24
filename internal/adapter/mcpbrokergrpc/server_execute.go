package mcpbrokergrpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const executeMethod = "/mecatl.broker.v1.BrokerService/Execute"

func preDispatchError(err error) error {
	st := status.Convert(err)
	return reasonStatus(st.Code(), st.Message(), brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED, executeMethod)
}

// Execute dispatches one tool invocation and retains one immutable bounded receipt.
func (s *Server) Execute(ctx context.Context, req *brokerv1.ExecuteRequest) (*brokerv1.ExecuteResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecuteDeadline)
	defer cancel()
	if err := s.checkInstanceID(req.GetBrokerIncarnation(), false); err != nil {
		return nil, err
	}
	call, err := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if err != nil {
		return nil, preDispatchError(invalid(err.Error()))
	}
	if req.GetHandle() == "" {
		return nil, preDispatchError(invalid("handle is required"))
	}
	digest := invocationDigest(call)

	s.mu.Lock()
	a := s.handles[req.GetHandle()]
	if err := authorizeHandle(ctx, a); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if a == nil || s.closed || !time.Now().Before(a.expiresAt) || a.running != lifecycleNone || a.terminal != lifecycleNone {
		s.mu.Unlock()
		return nil, reasonStatus(codes.FailedPrecondition, "attachment handle unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	target := a.tools[call.Name]
	if target == nil {
		s.traceUnknownTool(ctx, a, call.Name)
		s.mu.Unlock()
		return nil, preDispatchError(invalid("unknown tool"))
	}
	receipt := a.receipts[call.ID]
	if receipt != nil {
		if receipt.digest != digest {
			s.mu.Unlock()
			return nil, preDispatchError(invalid("call_id was reused with different invocation content"))
		}
		if receipt.started {
			s.mu.Unlock()
			return waitExecuteReceipt(ctx, receipt)
		}
		receipt.started = true
	} else {
		if len(a.receipts) >= s.cfg.MaxReceipts || a.receiptBytes+receiptReservationBytes(call) > s.cfg.MaxReceiptBytes {
			s.mu.Unlock()
			return nil, reasonStatus(codes.ResourceExhausted, "broker receipt capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, executeMethod)
		}
		receipt = &executeReceipt{digest: digest, bytes: receiptReservationBytes(call), started: true, done: make(chan struct{})}
		a.receipts[call.ID] = receipt
		a.receiptBytes += receipt.bytes
	}
	if s.activeExecutes >= s.cfg.MaxActiveExecutes {
		s.finishExecuteLocked(a, receipt, call, nil, executeCapacityError(), false)
		s.mu.Unlock()
		return waitExecuteReceipt(ctx, receipt)
	}
	s.activeExecutes++
	a.active++
	signalHandle(a)
	s.executeWG.Add(1)
	s.mu.Unlock()

	go s.executeOwner(a, receipt, target, call)
	return waitExecuteReceipt(ctx, receipt)
}

func invocationReceiptBytes(call session.ToolCall) int {
	return len(call.Name) + len(call.ID) + len(call.ItemID) + len(call.Args)
}

// receiptReservationBytes guarantees that an executed invocation can retain a
// deterministic terminal result even when the tool's successful response is too large.
func receiptReservationBytes(call session.ToolCall) int {
	return invocationReceiptBytes(call) + maxTerminalReceiptBytes
}

func executeCapacityError() error {
	return reasonStatus(codes.ResourceExhausted, "broker Execute capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, executeMethod)
}

func receiptOversizeResponse(call session.ToolCall) *brokerv1.ExecuteResponse {
	return &brokerv1.ExecuteResponse{Result: &brokerv1.ToolResult{
		CallId:  string(call.ID),
		Content: "tool completed, but its result exceeded the broker retained-receipt limit; do not retry this call",
		IsError: true,
	}}
}

func invocationDigest(call session.ToolCall) [sha256.Size]byte {
	h := sha256.New()
	var size [8]byte
	for _, field := range [][]byte{[]byte(call.Name), []byte(call.ItemID), call.Args} {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(field)
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func (s *Server) executeOwner(a *serverHandle, receipt *executeReceipt, target tool.Tool, call session.ToolCall) {
	defer s.executeWG.Done()
	var response *brokerv1.ExecuteResponse
	var executeErr error
	func() {
		defer func() {
			if recover() != nil {
				executeErr = status.Error(codes.Internal, "tool execution panicked")
			}
		}()
		ctx, cancel := context.WithTimeout(s.executeCtx, s.cfg.ExecuteDeadline)
		defer cancel()
		env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: string(a.logicalID), Revision: s.instanceID}, nofs.New(), memledger.New(), nil)
		result, err := target.Execute(ctx, call, env)
		switch {
		case err != nil:
			executeErr = brokerStatus(err)
		case result.CallID != call.ID:
			executeErr = status.Error(codes.Internal, "tool result call_id mismatch")
		default:
			wire, err := resultToWire(result)
			if err != nil {
				executeErr = status.Error(codes.Internal, "malformed tool result")
				return
			}
			response = &brokerv1.ExecuteResponse{Result: wire}
		}
	}()

	s.mu.Lock()
	s.finishExecuteLocked(a, receipt, call, response, executeErr, true)
	s.mu.Unlock()
}

func (s *Server) finishExecuteLocked(a *serverHandle, receipt *executeReceipt, call session.ToolCall, response *brokerv1.ExecuteResponse, err error, dispatched bool) {
	bytes := invocationReceiptBytes(call)
	switch {
	case response != nil:
		bytes += proto.Size(response)
	case err != nil:
		bytes += proto.Size(status.Convert(err).Proto())
	}
	if a.receiptBytes-receipt.bytes+bytes > s.cfg.MaxReceiptBytes {
		response = receiptOversizeResponse(call)
		err = nil
		bytes = invocationReceiptBytes(call) + proto.Size(response)
	}
	a.receiptBytes += bytes - receipt.bytes
	receipt.bytes = bytes
	receipt.response = response
	receipt.err = err
	close(receipt.done)
	if dispatched {
		s.activeExecutes--
		a.active--
	}
	s.releaseClosedReceiptsLocked(a)
	signalHandle(a)
}

func waitExecuteReceipt(ctx context.Context, receipt *executeReceipt) (*brokerv1.ExecuteResponse, error) {
	select {
	case <-receipt.done:
		if receipt.response == nil {
			return nil, receipt.err
		}
		return proto.Clone(receipt.response).(*brokerv1.ExecuteResponse), receipt.err
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}
