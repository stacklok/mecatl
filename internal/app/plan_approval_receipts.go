package app

import (
	"sync"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

const maxPlanApprovalReceipts = 1024

// planApprovalReceipts owns one bounded, single-use receipt per session. Receipts
// are deliberately process-local: restart and shutdown discard them.
type planApprovalReceipts struct {
	mu       sync.Mutex
	receipts map[session.SessionID]agent.PlanApprovalReceipt
}

func newPlanApprovalReceipts() *planApprovalReceipts {
	return &planApprovalReceipts{receipts: make(map[session.SessionID]agent.PlanApprovalReceipt)}
}

func (s *planApprovalReceipts) RecordPlanApproval(receipt agent.PlanApprovalReceipt) {
	if s == nil || receipt.Ref == "" || receipt.SessionID == "" || receipt.Call == "" || (receipt.TargetMode != session.ModeDefault && receipt.TargetMode != session.ModeAccept) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.receipts[receipt.SessionID]; !exists && len(s.receipts) >= maxPlanApprovalReceipts {
		return
	}
	s.receipts[receipt.SessionID] = receipt
}

func (s *planApprovalReceipts) ConsumePlanApproval(id session.SessionID) (agent.PlanApprovalReceipt, bool) {
	if s == nil {
		return agent.PlanApprovalReceipt{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[id]
	delete(s.receipts, id)
	return receipt, ok
}

func (s *planApprovalReceipts) ClearPlanApprovals(id session.SessionID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.receipts, id)
}
