package control

import (
	"fmt"
	"path/filepath"

	gomicrovm "github.com/stacklok/go-microvm"
)

// GuestControlPort is the reserved versioned guest-control vsock port.
const GuestControlPort uint32 = 10777

// GuestControlOption wires the guest control channel through go-microvm's
// vsock-to-Unix-domain-socket primitive. SSH and host-local fallbacks are not part
// of the control protocol.
func GuestControlOption(socketPath string) (gomicrovm.Option, error) {
	if socketPath == "" || !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("microvm guest control socket path must be absolute")
	}
	return gomicrovm.WithVsock(GuestControlPort, socketPath), nil
}
