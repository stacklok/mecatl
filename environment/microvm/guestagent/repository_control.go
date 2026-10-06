package guestagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strconv"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

// RepositoryControlOperation is a generation-scoped guest management operation.
type RepositoryControlOperation string

const (
	// RepositoryRegister registers one authenticated logical guest root.
	RepositoryRegister RepositoryControlOperation = "register"
	// RepositoryUnregister revokes one authenticated logical guest root.
	RepositoryUnregister RepositoryControlOperation = "unregister"
	// RepositoryHealth proves possession of the repository boot authority.
	RepositoryHealth RepositoryControlOperation = "health"
)

// RepositoryHealthChallenge and RepositoryHealthStatus are wire values signed
// by the guest's generation boot authority.
type RepositoryHealthChallenge struct {
	Owner, RepositoryKey, VMID string
	Generation                 uint32
	Nonce                      [32]byte
	Status                     RepositoryHealthStatus
}

// RepositoryHealthStatus is the runtime status covered by a health response MAC.
type RepositoryHealthStatus struct {
	Live            bool
	Generation      uint32
	VMID            string
	PID             int
	ProcessIdentity string
	Endpoint        string
}

// RepositoryControlRequest carries no host path. Binding.AssignedRoot is the
// path already mounted and visible inside the guest.
type RepositoryControlRequest struct {
	Operation   RepositoryControlOperation `json:"operation"`
	Sequence    uint64                     `json:"sequence,omitempty"`
	Incarnation string                     `json:"incarnation,omitempty"`
	Binding     control.Binding            `json:"binding"`
	Capability  string                     `json:"capability,omitempty"`
	Health      *RepositoryHealthChallenge `json:"health,omitempty"`
}

// RepositoryControlResponse is the bounded control acknowledgement.
type RepositoryControlResponse struct {
	Sequence  uint64            `json:"sequence,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
	HealthMAC [sha256.Size]byte `json:"health_mac,omitempty"`
}

// ServeAuthenticated proves this guest owns the repository generation before
// reading any capability or protocol payload, then dispatches only the purpose
// covered by the host challenge.
func (s *RepositoryServer) ServeAuthenticated(ctx context.Context, stream io.ReadWriteCloser) error {
	if s == nil {
		return ErrUnauthenticatedRepositoryChannel
	}
	purpose, err := authenticateGuestRepositoryChannel(ctx, stream, s.authorityKey, s.owner, s.repositoryKey, s.vmID, s.generation)
	if err != nil {
		return err
	}
	switch purpose {
	case RepositoryChannelControl:
		return s.ServeControl(ctx, stream)
	case RepositoryChannelData:
		select {
		case s.dataSlots <- struct{}{}:
			defer func() { <-s.dataSlots }()
		default:
			return errors.New("repository logical connection capacity exhausted")
		}
		return s.ServeBound(ctx, stream)
	default:
		return ErrUnauthenticatedRepositoryChannel
	}
}

// ServeControl authenticates one register/unregister exchange. Register opens
// only the guest-visible path from the binding; unregister atomically revokes it.
func (s *RepositoryServer) ServeControl(ctx context.Context, stream io.ReadWriteCloser) error {
	if s == nil || stream == nil {
		return control.ErrUnauthenticatedCapability
	}
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	var request RepositoryControlRequest
	if err := codec.Read(stream, &request); err != nil {
		return err
	}
	var err error
	response := RepositoryControlResponse{Sequence: request.Sequence}
	switch request.Operation {
	case RepositoryRegister, RepositoryUnregister:
		if request.Sequence != 0 {
			response, err = s.mutate(ctx, request)
		} else if request.Operation == RepositoryRegister {
			err = s.Register(ctx, request.Capability, request.Binding)
		} else {
			err = s.Unregister(request.Capability, request.Binding)
		}
	case RepositoryHealth:
		if request.Health == nil {
			err = control.ErrBindingMismatch
		} else {
			response.HealthMAC, err = s.healthMAC(*request.Health)
		}
	default:
		err = control.ErrBindingMismatch
	}
	if err != nil {
		if errors.Is(err, ErrLogicalRootUnavailable) {
			response.ErrorCode = "logical_root_unavailable"
		} else {
			response.ErrorCode = "unauthenticated"
		}
	}
	if writeErr := codec.Write(stream, response); writeErr != nil {
		return writeErr
	}
	return err
}

func (s *RepositoryServer) mutate(ctx context.Context, request RepositoryControlRequest) (RepositoryControlResponse, error) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.lastMutation != nil && request.Sequence == s.nextSequence-1 && sameMutation(*s.lastMutation, request) {
		return s.lastResponse, nil
	}
	if request.Sequence != s.nextSequence || request.Incarnation == "" || request.Capability != "" || request.Health != nil {
		return RepositoryControlResponse{}, control.ErrUnauthenticatedCapability
	}
	var err error
	if request.Operation == RepositoryRegister {
		err = s.register(ctx, request.Binding, request.Incarnation)
	} else {
		err = s.unregister(request.Binding, request.Incarnation)
	}
	response := RepositoryControlResponse{Sequence: request.Sequence}
	if err != nil {
		if errors.Is(err, ErrLogicalRootUnavailable) {
			response.ErrorCode = "logical_root_unavailable"
		} else {
			response.ErrorCode = "unauthenticated"
		}
	}
	copyRequest := request
	s.lastMutation = &copyRequest
	s.lastResponse = response
	s.nextSequence++
	return response, err
}

func sameMutation(a, b RepositoryControlRequest) bool {
	return a.Operation == b.Operation && a.Sequence == b.Sequence && a.Incarnation == b.Incarnation &&
		a.Binding == b.Binding && a.Capability == b.Capability && a.Health == nil && b.Health == nil
}

func (s *RepositoryServer) healthMAC(challenge RepositoryHealthChallenge) ([sha256.Size]byte, error) {
	if challenge.Owner != s.owner || challenge.RepositoryKey != s.repositoryKey || challenge.VMID != s.vmID || challenge.Generation != s.generation ||
		challenge.Status.VMID != s.vmID || challenge.Status.Generation != s.generation || challenge.Status.Endpoint != s.endpoint {
		return [sha256.Size]byte{}, control.ErrBindingMismatch
	}
	mac := hmac.New(sha256.New, s.authorityKey)
	writeHealthString(mac, challenge.Owner)
	writeHealthString(mac, challenge.RepositoryKey)
	writeHealthString(mac, challenge.VMID)
	var number [8]byte
	binary.BigEndian.PutUint32(number[:4], challenge.Generation)
	_, _ = mac.Write(number[:4])
	_, _ = mac.Write(challenge.Nonce[:])
	if challenge.Status.Live {
		_, _ = mac.Write([]byte{1})
	} else {
		_, _ = mac.Write([]byte{0})
	}
	binary.BigEndian.PutUint32(number[:4], challenge.Status.Generation)
	_, _ = mac.Write(number[:4])
	writeHealthString(mac, challenge.Status.VMID)
	writeHealthString(mac, strconv.Itoa(challenge.Status.PID))
	writeHealthString(mac, challenge.Status.ProcessIdentity)
	writeHealthString(mac, challenge.Status.Endpoint)
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out, nil
}

func writeHealthString(writer interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
