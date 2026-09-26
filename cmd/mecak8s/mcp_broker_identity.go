package main

import (
	"errors"
	"io"
	"os"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/session"
)

const maxBrokerWorkloadTokenBytes = 64 << 10

// brokerWorkloadIdentity returns the issuer/subject of this agent's projected
// broker workload token. The broker verifies the same token and derives its
// continuity workload partition from these two claims, so the host needs the
// identical pair to build a matching guard. The signature is deliberately not
// checked here: the file is this pod's own mounted credential, the value only
// forms a guard that the broker independently verifies, and a wrong value makes
// custody fail closed. An empty path or unreadable token disables custody.
func brokerWorkloadIdentity(path string) (*session.Principal, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("read broker workload token")
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxBrokerWorkloadTokenBytes+1))
	if err != nil || len(raw) > maxBrokerWorkloadTokenBytes {
		return nil, errors.New("read broker workload token")
	}
	claims := jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(string(raw), &claims); err != nil {
		return nil, errors.New("parse broker workload token")
	}
	principal := &session.Principal{Issuer: claims.Issuer, Subject: claims.Subject}
	if principal.Issuer == "" || principal.Subject == "" || !principal.IdentityWellFramed() {
		return nil, errors.New("broker workload token has no usable identity")
	}
	return principal, nil
}
