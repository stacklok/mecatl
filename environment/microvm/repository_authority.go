package microvm

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strconv"
)

const repositoryAuthorityBytes = 32

// RepositoryBootAuthority is fresh secret material injected once into one
// repository VM generation. It is never written into a session preboot file.
type RepositoryBootAuthority struct {
	key [repositoryAuthorityBytes]byte
}

func newRepositoryBootAuthority() (RepositoryBootAuthority, error) {
	var authority RepositoryBootAuthority
	if _, err := rand.Read(authority.key[:]); err != nil {
		return RepositoryBootAuthority{}, err
	}
	return authority, nil
}

func repositoryBootAuthorityFromBytes(value []byte) (RepositoryBootAuthority, error) {
	if len(value) != repositoryAuthorityBytes {
		return RepositoryBootAuthority{}, errors.New("invalid repository VM boot authority")
	}
	var authority RepositoryBootAuthority
	copy(authority.key[:], value)
	return authority, nil
}

func (a RepositoryBootAuthority) bytes() []byte { return append([]byte(nil), a.key[:]...) }

func (a RepositoryBootAuthority) digest() string {
	digest := sha256.Sum256(a.key[:])
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// RepositoryHealthChallenge is one unpredictable host challenge bound to an exact
// repository generation. It carries no bearer proof: only the booted guest can
// produce the corresponding response MAC.
type RepositoryHealthChallenge struct {
	Owner         string
	RepositoryKey string
	VMID          string
	Generation    uint32
	Nonce         [repositoryAuthorityBytes]byte
}

// RepositoryHealthResponse authenticates both the challenge and the runtime
// status actually returned by the guest.
type RepositoryHealthResponse struct {
	Status RuntimeStatus
	MAC    [sha256.Size]byte
}

func (RepositoryBootAuthority) healthChallenge(record RepositoryVMRecord) (RepositoryHealthChallenge, error) {
	challenge := RepositoryHealthChallenge{Owner: record.Owner, RepositoryKey: record.RepositoryKey, VMID: record.VMID, Generation: record.bootGeneration()}
	if _, err := rand.Read(challenge.Nonce[:]); err != nil {
		return RepositoryHealthChallenge{}, err
	}
	return challenge, nil
}

// HealthResponse signs a challenge and the guest's actual status.
func (a RepositoryBootAuthority) HealthResponse(record RepositoryVMRecord, challenge RepositoryHealthChallenge, status RuntimeStatus) (RepositoryHealthResponse, error) {
	if challenge.Owner != record.Owner || challenge.RepositoryKey != record.RepositoryKey || challenge.VMID != record.VMID || challenge.Generation != record.bootGeneration() {
		return RepositoryHealthResponse{}, ErrRepositoryVMInconsistent
	}
	response := RepositoryHealthResponse{Status: status}
	copy(response.MAC[:], a.healthMAC(challenge, status))
	return response, nil
}

// VerifyHealth accepts only a response signed by the boot authority over the
// exact challenge, tuple, and returned health fields.
func (a RepositoryBootAuthority) VerifyHealth(record RepositoryVMRecord, challenge RepositoryHealthChallenge, response RepositoryHealthResponse) error {
	if challenge.Owner != record.Owner || challenge.RepositoryKey != record.RepositoryKey || challenge.VMID != record.VMID || challenge.Generation != record.bootGeneration() ||
		!hmac.Equal(response.MAC[:], a.healthMAC(challenge, response.Status)) {
		return ErrRepositoryVMInconsistent
	}
	return nil
}

func (a RepositoryBootAuthority) healthMAC(challenge RepositoryHealthChallenge, status RuntimeStatus) []byte {
	mac := hmac.New(sha256.New, a.key[:])
	writeAuthorityString(mac, challenge.Owner)
	writeAuthorityString(mac, challenge.RepositoryKey)
	writeAuthorityString(mac, challenge.VMID)
	var number [8]byte
	binary.BigEndian.PutUint32(number[:4], challenge.Generation)
	_, _ = mac.Write(number[:4])
	_, _ = mac.Write(challenge.Nonce[:])
	if status.Live {
		_, _ = mac.Write([]byte{1})
	} else {
		_, _ = mac.Write([]byte{0})
	}
	binary.BigEndian.PutUint32(number[:4], status.Generation)
	_, _ = mac.Write(number[:4])
	writeAuthorityString(mac, status.VMID)
	writeAuthorityString(mac, strconv.Itoa(status.PID))
	writeAuthorityString(mac, status.ProcessIdentity)
	writeAuthorityString(mac, status.Endpoint)
	return mac.Sum(nil)
}

func writeAuthorityString(mac interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = mac.Write(length[:])
	_, _ = mac.Write([]byte(value))
}
