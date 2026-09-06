//go:build darwin

package managedtemp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"syscall"
)

// processStartIdentity derives an opaque identity from Darwin's kernel process
// record. It deliberately does not fall back to the PID: a reused PID has a
// different kinfo_proc record and therefore a different identity.
func processStartIdentity(pid int) (string, error) {
	record, err := syscall.Sysctl(fmt.Sprintf("kern.proc.pid.%d", pid))
	if err != nil || record == "" {
		if err == nil {
			err = fmt.Errorf("empty process record")
		}
		return "", fmt.Errorf("managedtemp: read process start identity: %w", err)
	}
	sum := sha256.Sum256([]byte(record))
	return hex.EncodeToString(sum[:]), nil
}
