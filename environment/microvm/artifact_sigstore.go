package microvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/stacklok/toolhive-core/container/verifier"
)

// SigstoreVerifier verifies stored Sigstore bundles in process.
type SigstoreVerifier struct {
	publicKey         []byte
	publicKeyIdentity string
}

// NewSigstoreVerifier creates an offline keyless verifier using ToolHive Core's
// embedded Sigstore trusted material.
func NewSigstoreVerifier() *SigstoreVerifier {
	return &SigstoreVerifier{}
}

// NewSigstoreKeyVerifier creates an offline verifier pinned to one public key.
// The key bytes are copied so later caller mutation cannot change the trust root.
func NewSigstoreKeyVerifier(publicKey []byte) (*SigstoreVerifier, error) {
	if len(publicKey) == 0 {
		return nil, errors.New("sigstore public key is required")
	}
	key := append([]byte(nil), publicKey...)
	return &SigstoreVerifier{publicKey: key, publicKeyIdentity: PublicKeyIdentity(key)}, nil
}

// PublicKeyIdentity returns the stable identity operator policy binds to exact
// public-key bytes.
func PublicKeyIdentity(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Verify checks that bundle signs statement under the configured exact keyless
// certificate identity or public-key identity.
func (v *SigstoreVerifier) Verify(ctx context.Context, statement, bundle []byte, identity, issuer string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if v == nil || identity == "" || len(statement) == 0 || len(bundle) == 0 {
		return errors.New("incomplete Sigstore verification input")
	}
	digest := sha256.Sum256(statement)
	subject := "sha256:" + hex.EncodeToString(digest[:])
	if len(v.publicKey) > 0 {
		if issuer != "" || identity != v.publicKeyIdentity {
			return errors.New("sigstore public-key identity mismatch")
		}
		_, err := verifier.VerifyBundleOfflineWithKey(bundle, subject, v.publicKey)
		return err
	}
	if issuer == "" {
		return errors.New("incomplete Sigstore keyless identity policy")
	}
	_, err := verifier.VerifyBundleOffline(bundle, subject, &verifier.Identity{
		SignerIdentity: identity,
		CertIssuer:     issuer,
	})
	return err
}
