package mcpbrokergrpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
)

const (
	// Continuity receipts retain enough state to replay an exact Stage or Recover
	// result. This is intentionally separate from Execute's configurable per-handle
	// receipt budget: continuity custody is process-wide authority.
	maxConcurrentContinuityReceipts = 8
	maxGlobalContinuityReceipts     = 32
	stageReceiptReservationBytes    = 256
	recoverReceiptReservationBytes  = 1 << 20
	maxContinuityReceiptBytes       = 4 * recoverReceiptReservationBytes
	maxGlobalContinuityReceiptBytes = 16 * recoverReceiptReservationBytes
)

type continuityReceiptKey struct {
	workload  [32]byte
	operation string
	request   string
}

type continuityReceipt struct {
	digest      [sha256.Size]byte
	workload    [32]byte
	expiresAt   time.Time
	done        chan struct{}
	bytes       int
	reservation int
	reserved    bool
	settled     bool
	stage       *brokerv1.StageCredentialCustodyResponse
	recover     *brokerv1.RecoverCredentialAttachmentResponse
	err         error
}

type continuityReceiptUsage struct {
	bytes int
	slots int
}

// reserveContinuityReceipt reserves a process-local retry receipt before an
// optional custody capability is reached. The caller must publish exactly once.
func (s *Server) reserveContinuityReceipt(key continuityReceiptKey, digest [sha256.Size]byte, expiresAt time.Time, reservation int) (*continuityReceipt, bool, error) {
	if reservation <= 0 || reservation > maxContinuityReceiptBytes {
		return nil, false, continuityUnavailable()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.closed {
		return nil, false, continuityUnavailable()
	}
	if receipt := s.continuityReceipts[key]; receipt != nil {
		if !now.Before(receipt.expiresAt) {
			if !receipt.settled {
				s.settleExpiredContinuityReceiptLocked(key, receipt)
			}
			delete(s.continuityReceipts, key)
			s.releaseContinuityReservationLocked(receipt)
		} else if receipt.digest != digest {
			return nil, false, continuityUnavailable()
		} else {
			return receipt, false, nil
		}
	}
	usage := s.continuityReceiptUsage[key.workload]
	if s.continuityReceiptSlots >= maxGlobalContinuityReceipts || s.continuityReceiptBytes+reservation > maxGlobalContinuityReceiptBytes ||
		usage.slots >= maxConcurrentContinuityReceipts || usage.bytes+reservation > maxContinuityReceiptBytes {
		return nil, false, continuityUnavailable()
	}
	receipt := &continuityReceipt{digest: digest, workload: key.workload, expiresAt: expiresAt, done: make(chan struct{}), bytes: reservation, reservation: reservation, reserved: true}
	s.continuityReceipts[key] = receipt
	s.continuityReceiptUsage[key.workload] = continuityReceiptUsage{bytes: usage.bytes + reservation, slots: usage.slots + 1}
	s.continuityReceiptSlots++
	s.continuityReceiptBytes += reservation
	return receipt, true, nil
}

func (s *Server) awaitContinuityReceipt(ctx context.Context, receipt *continuityReceipt) error {
	select {
	case <-receipt.done:
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-s.stop:
		return continuityUnavailable()
	}
}

func (s *Server) finishContinuityReceipt(key continuityReceiptKey, receipt *continuityReceipt, stage *brokerv1.StageCredentialCustodyResponse, recover *brokerv1.RecoverCredentialAttachmentResponse, err error, expiresAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt.settled || s.continuityReceipts[key] != receipt {
		if s.continuityReceipts[key] == receipt && !time.Now().Before(receipt.expiresAt) {
			delete(s.continuityReceipts, key)
			s.releaseContinuityReservationLocked(receipt)
		}
		return false
	}
	if expiresAt.Before(receipt.expiresAt) {
		receipt.expiresAt = expiresAt
	}
	stage = cloneStageReceipt(stage)
	recover = cloneRecoverReceipt(recover)
	retained := 0
	if stage != nil {
		retained = proto.Size(stage)
	}
	if recover != nil {
		retained = proto.Size(recover)
	}
	accepted := retained <= receipt.reservation
	if !accepted {
		stage, recover, err = nil, nil, continuityUnavailable()
		retained = 0
	}
	s.resizeContinuityReceiptLocked(receipt, retained)
	receipt.stage = stage
	receipt.recover = recover
	receipt.err = err
	receipt.settled = true
	close(receipt.done)
	return accepted
}

func (s *Server) sweepContinuityReceiptsLocked(now time.Time) {
	for key, receipt := range s.continuityReceipts {
		if !now.Before(receipt.expiresAt) {
			if receipt.settled {
				delete(s.continuityReceipts, key)
				s.releaseContinuityReservationLocked(receipt)
			} else {
				s.settleExpiredContinuityReceiptLocked(key, receipt)
			}
		}
	}
}

func (s *Server) resizeContinuityReceiptLocked(receipt *continuityReceipt, bytes int) {
	if receipt.bytes == bytes {
		return
	}
	usage := s.continuityReceiptUsage[receipt.workload]
	usage.bytes += bytes - receipt.bytes
	s.continuityReceiptUsage[receipt.workload] = usage
	s.continuityReceiptBytes += bytes - receipt.bytes
	receipt.bytes = bytes
}

func (s *Server) releaseContinuityBytesLocked(receipt *continuityReceipt) {
	s.resizeContinuityReceiptLocked(receipt, 0)
}

func (s *Server) releaseContinuityReservationLocked(receipt *continuityReceipt) {
	s.releaseContinuityBytesLocked(receipt)
	if !receipt.reserved {
		return
	}
	receipt.reserved = false
	usage := s.continuityReceiptUsage[receipt.workload]
	usage.slots--
	if usage.slots == 0 && usage.bytes == 0 {
		delete(s.continuityReceiptUsage, receipt.workload)
	} else {
		s.continuityReceiptUsage[receipt.workload] = usage
	}
	s.continuityReceiptSlots--
}

func (s *Server) discardContinuityReceipt(key continuityReceiptKey, receipt *continuityReceipt, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt.settled || s.continuityReceipts[key] != receipt {
		return
	}
	delete(s.continuityReceipts, key)
	s.releaseContinuityReservationLocked(receipt)
	receipt.err = err
	receipt.settled = true
	close(receipt.done)
}

func (s *Server) settleExpiredContinuityReceiptLocked(_ continuityReceiptKey, receipt *continuityReceipt) {
	if receipt.settled {
		return
	}
	s.releaseContinuityBytesLocked(receipt)
	receipt.err = continuityUnavailable()
	receipt.settled = true
	close(receipt.done)
}

func cloneStageReceipt(in *brokerv1.StageCredentialCustodyResponse) *brokerv1.StageCredentialCustodyResponse {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*brokerv1.StageCredentialCustodyResponse)
}

func cloneRecoverReceipt(in *brokerv1.RecoverCredentialAttachmentResponse) *brokerv1.RecoverCredentialAttachmentResponse {
	if in == nil {
		return nil
	}
	return proto.Clone(in).(*brokerv1.RecoverCredentialAttachmentResponse)
}

func continuityDigest(parts ...[]byte) [sha256.Size]byte {
	hash := sha256.New()
	for _, part := range parts {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(part)
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum
}

func continuityTimePart(value time.Time) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(value.UnixNano()))
	return out[:]
}

func continuityUint32Part(value uint32) []byte {
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], value)
	return out[:]
}

func continuityGuardParts(guard [3][32]byte, sessionID, incarnation string, providers []string) [][]byte {
	parts := make([][]byte, 0, 5+len(providers))
	parts = append(parts, []byte(sessionID), []byte(incarnation), guard[0][:], guard[1][:], guard[2][:])
	for _, provider := range providers {
		parts = append(parts, []byte(provider))
	}
	return parts
}
