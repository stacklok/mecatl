package identityissuer

import (
	"context"
	"errors"
	"time"
)

var errHostUnavailable = errors.New("identity issuer host is unavailable")

// HostConfig selects the issuer-only composition. It deliberately contains no
// agent, provider, catalog, runner, or broker lifecycle dependency.
type HostConfig struct {
	Issuer           Config
	Manifest         []byte
	LoadKey          KeyLoader
	RefreshHint      time.Duration
	ExecutionSurface bool
}

// GenerationStatus is the complete safe projection of the in-memory signer
// generation. It intentionally contains neither key material nor JWTs.
type GenerationStatus struct {
	Enabled   bool   `json:"enabled"`
	Ready     bool   `json:"ready"`
	Algorithm string `json:"algorithm,omitempty"`
}

// Host is the shell-less issuer composition used by the future combined broker.
// It owns no listener, persistence, background worker, or minting endpoint.
type Host struct {
	issuer      *Issuer
	refreshHint time.Duration
	verified    bool
}

// NewDisabledHost returns the explicit no-issuer composition without loading
// configuration or key material.
func NewDisabledHost() *Host { return &Host{} }

// NewHost validates custody before opening any key item. An issuer may never
// share a process composition that declares an execution surface.
func NewHost(cfg HostConfig) (*Host, error) {
	if cfg.ExecutionSurface {
		return nil, errHostUnavailable
	}
	if !cfg.Issuer.Enabled {
		return NewDisabledHost(), nil
	}
	if cfg.RefreshHint <= 0 || cfg.RefreshHint > maxBundleCache || cfg.RefreshHint%time.Second != 0 {
		return nil, errHostUnavailable
	}
	issuer, err := Load(cfg.Issuer, cfg.Manifest, cfg.LoadKey)
	if err != nil {
		return nil, errHostUnavailable
	}
	host := &Host{issuer: issuer, refreshHint: cfg.RefreshHint}
	if err := host.verifyStartupCanary(); err != nil {
		return nil, errHostUnavailable
	}
	host.verified = true
	return host, nil
}

// Live reports whether the host process can answer its fixed liveness probe.
func (h *Host) Live() bool { return h != nil }

// Status exposes only safe generation state.
func (h *Host) Status() GenerationStatus {
	if h == nil || h.issuer == nil {
		return GenerationStatus{}
	}
	return GenerationStatus{Enabled: true, Ready: h.verified, Algorithm: h.issuer.Algorithm()}
}

// Bundle returns the canonical public bundle for the ready immutable generation.
func (h *Host) Bundle() ([]byte, error) {
	if h == nil || h.issuer == nil {
		return nil, errHostUnavailable
	}
	return h.issuer.Bundle(h.refreshHint)
}

// IssueLogicalAgent mints the one constrained logical-agent JWT-SVID profile.
// It exposes neither an arbitrary signing operation nor signer material.
func (h *Host) IssueLogicalAgent(request LogicalAgentIssueRequest) (string, error) {
	if h == nil || h.issuer == nil || !h.verified {
		return "", errHostUnavailable
	}
	return h.issuer.IssueLogicalAgent(request)
}

func (h *Host) verifyStartupCanary() error {
	verifier, err := NewVerifier(VerifierConfig{
		TrustDomain:       h.issuer.trustDomain,
		Audience:          h.issuer.audience,
		TokenTTL:          h.issuer.tokenTTL,
		ClockSkew:         h.issuer.clockSkew,
		BundleCacheTTL:    h.refreshHint,
		HTTPSBootstrapURL: "https://issuer.invalid/bundle",
	}, BundleFetcherFunc(func(context.Context, string) ([]byte, error) {
		return h.Bundle()
	}))
	if err != nil {
		return err
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		return err
	}
	token, err := h.issuer.IssueJWTSubject("spiffe://"+h.issuer.trustDomain+"/broker-verifier-canary", h.issuer.audience, h.issuer.tokenTTL)
	if err != nil {
		return err
	}
	_, err = verifier.Verify(token)
	return err
}
