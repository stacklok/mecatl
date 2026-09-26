package control

import (
	"fmt"
	"net"
)

// PeerAuthenticator extracts kernel-authenticated credentials from a local connection.
type PeerAuthenticator interface {
	PeerUID(net.Conn) (uint32, error)
}

// ServiceConfig configures the private local microvmd control service.
type ServiceConfig struct {
	AccountUID        uint32
	Bindings          []Binding
	PeerAuthenticator PeerAuthenticator
}

// Service authorizes local control operations against kernel peer credentials and
// the complete immutable environment binding.
type Service struct {
	accountUID uint32
	bindings   map[string]Binding
	peers      PeerAuthenticator
}

// NewService constructs a local control service. Bindings are copied.
func NewService(cfg ServiceConfig) (*Service, error) {
	peers := cfg.PeerAuthenticator
	if peers == nil {
		peers = platformPeerAuthenticator{}
	}
	bindings := make(map[string]Binding, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		if err := binding.validate(); err != nil {
			return nil, err
		}
		if _, exists := bindings[binding.EnvironmentID]; exists {
			return nil, fmt.Errorf("duplicate microvm environment binding %q", binding.EnvironmentID)
		}
		bindings[binding.EnvironmentID] = binding
	}
	return &Service{accountUID: cfg.AccountUID, bindings: bindings, peers: peers}, nil
}

// Authenticate verifies the Unix peer before any caller-controlled identifier is read.
func (s *Service) Authenticate(conn net.Conn) error {
	if s == nil || s.peers == nil {
		return ErrUnauthenticatedPeer
	}
	uid, err := s.peers.PeerUID(conn)
	if err != nil || uid != s.accountUID {
		return ErrUnauthenticatedPeer
	}
	return nil
}

// Authorize authenticates the Unix peer before looking up or comparing identifiers.
func (s *Service) Authorize(conn net.Conn, claim Binding) error {
	if err := s.Authenticate(conn); err != nil {
		return err
	}
	expected, ok := s.bindings[claim.EnvironmentID]
	if !ok || expected != claim {
		return ErrBindingMismatch
	}
	return nil
}
