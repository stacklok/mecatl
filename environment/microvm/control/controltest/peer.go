// Package controltest provides deterministic offline control-protocol fakes.
package controltest

import "net"

// StaticPeerAuthenticator returns fixed peer credentials without consulting the host.
type StaticPeerAuthenticator struct {
	UID uint32
	Err error
}

// PeerUID implements control.PeerAuthenticator.
func (a StaticPeerAuthenticator) PeerUID(net.Conn) (uint32, error) {
	return a.UID, a.Err
}
