package anthropicsub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Device and session identity reported to the provider.
//
// The device id is derived, never random per process: the provider counts
// distinct devices, so a fresh value on every start would inflate that count
// for one installation. It is a hash, so the install identity it derives from
// is not recoverable from the wire.
const (
	deviceDomainInstall = "mecatl-claude-device-id-v1:"
	deviceDomainAccount = "mecatl-claude-device-id-v2"
)

var (
	installOnce sync.Once
	installID   string
)

// SetInstallID pins the installation identity the device id derives from.
// Callers supply the host's stable installation id; when unset a per-process
// value is generated, which is correct for one-shot commands but inflates the
// provider's device count if used by a long-lived daemon.
func SetInstallID(id string) {
	installOnce.Do(func() { installID = id })
}

func resolveInstallID() string {
	installOnce.Do(func() {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			installID = "mecatl-unknown-install"
			return
		}
		installID = hex.EncodeToString(raw)
	})
	return installID
}

// deriveDeviceID produces the stable per-install, per-account device id.
func deriveDeviceID(accountID string) string {
	install := resolveInstallID()
	if accountID == "" {
		sum := sha256.Sum256([]byte(deviceDomainInstall + install))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte(deviceDomainAccount + "\x00" + install + "\x00" + accountID))
	return hex.EncodeToString(sum[:])
}

// deriveSessionID produces a stable session id for an account when the caller
// supplies none, so repeated requests are not counted as distinct sessions.
func deriveSessionID(accountID string) string {
	sum := sha256.Sum256([]byte("mecatl-claude-session-v1:" + resolveInstallID() + "\x00" + accountID))
	return uuidFromBytes(sum[:16])
}

// newRequestID is a fresh per-request correlation id.
func newRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return uuidFromBytes(make([]byte, 16))
	}
	return uuidFromBytes(raw)
}

// uuidFromBytes renders 16 bytes as a version-4-shaped UUID. The provider
// parses the shape, not the version semantics.
func uuidFromBytes(raw []byte) string {
	var b [16]byte
	copy(b[:], raw)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}
