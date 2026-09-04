package identityissuer

import (
	"encoding/base64"
	"errors"
	"io"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/governance"
)

const maxLogicalAgentCompactTokenBytes = 16 << 10

// LogicalAgentIssueRequest is the complete typed input to constrained
// logical-agent issuance. The issuer owns every JWT and JOSE field.
type LogicalAgentIssueRequest struct {
	Identity LogicalAgentIdentity
	Instance string
	Tools    []string
	Source   governance.CapabilitySet
}

type logicalAgentJWTClaims struct {
	jwt.RegisteredClaims
	LogicalAgent LogicalAgentClaim `json:"https://mecatl.dev/claims/logical-agent/v1"`
}

// IssueLogicalAgent mints a JWT-SVID only when the requested exact tool set is
// contained by the source authority. It does not expose a general signing API.
func (i *Issuer) IssueLogicalAgent(request LogicalAgentIssueRequest) (string, error) {
	if i == nil || i.key == nil || i.now == nil || i.random == nil {
		return "", errors.New("logical agent issuer is unavailable")
	}
	identity, err := NewLogicalAgentIdentity(i.trustDomain, request.Identity.Tier, request.Identity.Name)
	if err != nil || identity != request.Identity {
		return "", errors.New("logical agent identity is invalid")
	}
	claim, err := NewLogicalAgentClaim(identity.Tier, identity.Name, request.Instance, request.Tools)
	if err != nil {
		return "", err
	}
	if !request.Source.Contains(governance.CapabilitySet{Tools: claim.Tools}) {
		return "", errors.New("logical agent requested tools exceed source authority")
	}
	var rawID [16]byte
	if _, err := io.ReadFull(i.random, rawID[:]); err != nil {
		return "", errors.New("logical agent JWT ID generation failed")
	}
	now := i.now().UTC()
	claims := logicalAgentJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "spiffe://" + i.trustDomain,
			Subject:   identity.Subject,
			Audience:  jwt.ClaimStrings{i.audience},
			ID:        base64.RawURLEncoding.EncodeToString(rawID[:]),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-i.clockSkew)),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.tokenTTL)),
		},
		LogicalAgent: claim,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = i.kid
	compact, err := token.SignedString(i.key)
	if err != nil {
		return "", err
	}
	if len(compact) > i.maxTokenBytes {
		return "", errors.New("logical agent JWT exceeds size limit")
	}
	return compact, nil
}
