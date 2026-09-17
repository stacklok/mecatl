package executioncontroller

import (
	"context"
	"crypto/x509"
	"errors"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RPCLimiter bounds active unary RPCs globally and per authorized client.
type RPCLimiter struct {
	security  *SecurityManager
	globalMax int
	clientMax int
	mu        sync.Mutex
	global    int
	clients   map[string]int
}

// NewRPCLimiter constructs a limiter whose client map is bounded by the security allowlist.
func NewRPCLimiter(security *SecurityManager, globalMax, clientMax int) (*RPCLimiter, error) {
	if security == nil || globalMax < 1 || clientMax < 1 || clientMax > globalMax {
		return nil, status.Error(codes.InvalidArgument, "invalid RPC concurrency limits")
	}
	return &RPCLimiter{security: security, globalMax: globalMax, clientMax: clientMax, clients: map[string]int{}}, nil
}

// UnaryInterceptor authenticates against current policy before reserving capacity.
func (l *RPCLimiter) UnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	chain, _, err := clientCertificates(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "execution provider request failed")
	}
	id, _, err := l.security.authorize(ctx, chain)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "execution provider request failed")
	}
	if !l.acquire(id) {
		return nil, status.Error(codes.ResourceExhausted, "execution provider is at its concurrency limit")
	}
	defer l.release(id)
	return handler(ctx, req)
}

func clientCertificates(ctx context.Context) ([]*x509.Certificate, bool, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, false, errors.New("peer is unavailable")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, false, errors.New("client certificate is unavailable")
	}
	return tlsInfo.State.PeerCertificates, len(tlsInfo.State.VerifiedChains) != 0, nil
}

func (l *RPCLimiter) acquire(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global >= l.globalMax || l.clients[id] >= l.clientMax {
		return false
	}
	l.global++
	l.clients[id]++
	return true
}

func (l *RPCLimiter) release(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global--
	if l.clients[id] == 1 {
		delete(l.clients, id)
	} else {
		l.clients[id]--
	}
}
