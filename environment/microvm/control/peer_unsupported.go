//go:build !linux && !darwin

package control

import (
	"fmt"
	"net"
)

type platformPeerAuthenticator struct{}

func (platformPeerAuthenticator) PeerUID(net.Conn) (uint32, error) {
	return 0, fmt.Errorf("Unix peer credentials are unsupported on this platform")
}
