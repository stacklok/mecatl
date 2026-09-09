package guestagent

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

// RepositoryChannelPurpose identifies the protocol that follows guest-origin
// authentication. It is covered by the generation boot authority proof.
type RepositoryChannelPurpose string

const (
	// RepositoryChannelControl authenticates registration, health, and unregister traffic.
	RepositoryChannelControl RepositoryChannelPurpose = "control"
	// RepositoryChannelData authenticates one logical Workspace/exec stream.
	RepositoryChannelData RepositoryChannelPurpose = "data"
)

// ErrUnauthenticatedRepositoryChannel reports a peer that lacks the repository boot authority.
var ErrUnauthenticatedRepositoryChannel = errors.New("repository guest channel is not authenticated")

const (
	repositoryChannelAuthVersion = 1
	repositoryChannelAuthTimeout = 250 * time.Millisecond
)

type repositoryChannelChallenge struct {
	Version       uint32                   `json:"version"`
	Owner         string                   `json:"owner"`
	RepositoryKey string                   `json:"repository_key"`
	VMID          string                   `json:"vm_id"`
	Generation    uint32                   `json:"generation"`
	Purpose       RepositoryChannelPurpose `json:"purpose"`
	Nonce         [32]byte                 `json:"nonce"`
}

type repositoryChannelResponse struct {
	MAC [sha256.Size]byte `json:"mac"`
}

// AuthenticateHostRepositoryChannel challenges a newly accepted connection and
// returns only after it proves possession of the repository generation's boot
// authority. The caller must not send capabilities or protocol payloads first.
func AuthenticateHostRepositoryChannel(ctx context.Context, stream io.ReadWriteCloser, key []byte, owner, repositoryKey, vmID string, generation uint32, purpose RepositoryChannelPurpose) error {
	if stream == nil || len(key) < 32 || owner == "" || repositoryKey == "" || vmID == "" || generation == 0 || !validRepositoryChannelPurpose(purpose) {
		return ErrUnauthenticatedRepositoryChannel
	}
	challenge := repositoryChannelChallenge{
		Version: repositoryChannelAuthVersion, Owner: owner, RepositoryKey: repositoryKey,
		VMID: vmID, Generation: generation, Purpose: purpose,
	}
	if _, err := rand.Read(challenge.Nonce[:]); err != nil {
		return err
	}
	authCtx, cancel := context.WithTimeout(ctx, repositoryChannelAuthTimeout)
	defer cancel()
	stopClose := context.AfterFunc(authCtx, func() { _ = stream.Close() })
	defer stopClose()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(stream, challenge); err != nil {
		return errors.Join(ErrUnauthenticatedRepositoryChannel, err)
	}
	var response repositoryChannelResponse
	if err := codec.Read(stream, &response); err != nil {
		return errors.Join(ErrUnauthenticatedRepositoryChannel, err)
	}
	if !hmac.Equal(response.MAC[:], repositoryChannelMAC(key, challenge)) {
		return ErrUnauthenticatedRepositoryChannel
	}
	return nil
}

func authenticateGuestRepositoryChannel(ctx context.Context, stream io.ReadWriteCloser, key []byte, owner, repositoryKey, vmID string, generation uint32) (RepositoryChannelPurpose, error) {
	if stream == nil || len(key) < 32 {
		return "", ErrUnauthenticatedRepositoryChannel
	}
	stopClose := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopClose()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	var challenge repositoryChannelChallenge
	if err := codec.Read(stream, &challenge); err != nil {
		return "", errors.Join(ErrUnauthenticatedRepositoryChannel, err)
	}
	if challenge.Version != repositoryChannelAuthVersion || challenge.Owner != owner || challenge.RepositoryKey != repositoryKey ||
		challenge.VMID != vmID || challenge.Generation != generation || !validRepositoryChannelPurpose(challenge.Purpose) {
		return "", ErrUnauthenticatedRepositoryChannel
	}
	response := repositoryChannelResponse{}
	copy(response.MAC[:], repositoryChannelMAC(key, challenge))
	if err := codec.Write(stream, response); err != nil {
		return "", errors.Join(ErrUnauthenticatedRepositoryChannel, err)
	}
	return challenge.Purpose, nil
}

func validRepositoryChannelPurpose(purpose RepositoryChannelPurpose) bool {
	return purpose == RepositoryChannelControl || purpose == RepositoryChannelData
}

func repositoryChannelMAC(key []byte, challenge repositoryChannelChallenge) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mecatl.repository-channel-auth.v1"))
	writeChannelAuthString(mac, challenge.Owner)
	writeChannelAuthString(mac, challenge.RepositoryKey)
	writeChannelAuthString(mac, challenge.VMID)
	var generation [4]byte
	binary.BigEndian.PutUint32(generation[:], challenge.Generation)
	_, _ = mac.Write(generation[:])
	writeChannelAuthString(mac, string(challenge.Purpose))
	_, _ = mac.Write(challenge.Nonce[:])
	return mac.Sum(nil)
}

func writeChannelAuthString(writer io.Writer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
