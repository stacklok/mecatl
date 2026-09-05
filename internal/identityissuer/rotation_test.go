package identityissuer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestIdentityIssuerSubstrate_Scenario5_PrepublishEvidence(t *testing.T) {
	oldKey := testPKCS8(t)
	newKey := testPKCS8(t)
	issuer := loadRotation(t, 41, RotationPhasePrepublish, 7, 0, map[string][]byte{"old": oldKey, "new": newKey}, "old")
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	digest := bundleDigest(bundle)
	published, sequence, err := parseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	newSigner, err := parseP256PKCS8(newKey)
	if err != nil {
		t.Fatal(err)
	}
	newKID, err := publicJWKThumbprint(&newSigner.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.Generation() != 41 || sequence != 7 || len(published) != 2 || published[issuer.ActiveKID()] == nil || published[newKID] == nil || issuer.ActiveKID() == newKID {
		t.Fatalf("prepublish generation=%d sequence=%d active=%q keys=%d", issuer.Generation(), sequence, issuer.ActiveKID(), len(published))
	}
	if err := ValidatePrepublishEvidence(41, digest, []ReplicaEvidence{
		{Ready: true, Generation: 41, BundleDigest: digest},
		{Ready: true, Generation: 41, BundleDigest: digest},
	}); err != nil {
		t.Fatalf("ValidatePrepublishEvidence(): %v", err)
	}
	if err := ValidatePrepublishEvidence(41, digest, []ReplicaEvidence{{Ready: true, Generation: 41, BundleDigest: digest}, {Ready: true, Generation: 42, BundleDigest: digest}}); err == nil {
		t.Fatal("ValidatePrepublishEvidence() accepted a replica from another generation")
	}
	if err := ValidatePrepublishEvidence(41, digest, []ReplicaEvidence{{Ready: true, Generation: 41, BundleDigest: digest}, {Ready: true, Generation: 41, BundleDigest: "different"}}); err == nil {
		t.Fatal("ValidatePrepublishEvidence() accepted mismatched bundle evidence")
	}

	oldToken, err := issuer.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := verifierForBundle(t, bundle)
	if _, err := verifier.Verify(oldToken); err != nil {
		t.Fatalf("old signer token did not verify against prepublish bundle: %v", err)
	}
}

func TestIdentityIssuerSubstrate_Scenario5_ActivateAndRestart(t *testing.T) {
	oldKey := testPKCS8(t)
	newKey := testPKCS8(t)
	keys := map[string][]byte{"old": oldKey, "new": newKey}
	prepublish := loadRotation(t, 41, RotationPhasePrepublish, 7, 0, keys, "old")
	cachedBundle, err := prepublish.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := prepublish.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	activated := loadRotation(t, 42, RotationPhaseActive, 8, 0, keys, "new")
	newToken, err := activated.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cachedVerifier := verifierForBundle(t, cachedBundle)
	for _, token := range []string{oldToken, newToken} {
		if _, err := cachedVerifier.Verify(token); err != nil {
			t.Fatalf("cached prepublish verifier rejected overlap token: %v", err)
		}
	}

	restarted := loadRotation(t, 42, RotationPhaseActive, 8, 0, keys, "new")
	if restarted.Generation() != 42 || restarted.RotationPhase() != RotationPhaseActive || restarted.ActiveKID() != activated.ActiveKID() || restarted.BundleSequence() != 8 {
		t.Fatalf("restart state = generation %d, phase %q, active %q, sequence %d", restarted.Generation(), restarted.RotationPhase(), restarted.ActiveKID(), restarted.BundleSequence())
	}
	bundle, err := restarted.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := bundleSequence(t, bundle); got != 8 {
		t.Fatalf("restarted bundle sequence = %d, want 8", got)
	}
}

func TestADR_0300_RetirementOverlapBound(t *testing.T) {
	oldKey := testPKCS8(t)
	newKey := testPKCS8(t)
	keys := map[string][]byte{"old": oldKey, "new": newKey}
	prepublish := loadRotation(t, 41, RotationPhasePrepublish, 7, 0, keys, "old")
	oldToken, err := prepublish.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	lastOldIssuance := time.Unix(1_700_000_000, 0).UTC()
	overlap := testConfig().TokenTTL + testConfig().ClockSkew + testConfig().BundleCacheTTL
	retireManifest := rotationManifest(43, RotationPhaseRetired, 9, lastOldIssuance.Unix(), []rotationTestKey{{Name: "new", Active: true}})
	if _, err := loadAt(testConfig(), retireManifest, func(name string) ([]byte, error) { return keys[name], nil }, lastOldIssuance.Add(overlap-time.Nanosecond)); err == nil {
		t.Fatal("Load() accepted retirement before T+S+R elapsed")
	}
	retired, err := loadAt(testConfig(), retireManifest, func(name string) ([]byte, error) { return keys[name], nil }, lastOldIssuance.Add(overlap))
	if err != nil {
		t.Fatalf("Load() rejected retirement after T+S+R: %v", err)
	}
	bundle, err := retired.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsedKeys, _, err := parseBundle(bundle)
	if err != nil || len(parsedKeys) != 1 || parsedKeys[retired.ActiveKID()] == nil {
		t.Fatalf("retired bundle did not remove old authority: keys=%d err=%v", len(parsedKeys), err)
	}
	if _, err := verifierForBundle(t, bundle).Verify(oldToken); err == nil {
		t.Fatal("retired verifier accepted old-key token")
	}
}

type rotationTestKey struct {
	Name   string
	Active bool
}

func loadRotation(t *testing.T, generation uint64, phase RotationPhase, sequence uint64, lastOldIssuance int64, keys map[string][]byte, active string) *Issuer {
	t.Helper()
	entries := make([]rotationTestKey, 0, len(keys))
	for _, name := range []string{"old", "new"} {
		if _, ok := keys[name]; ok {
			entries = append(entries, rotationTestKey{Name: name, Active: name == active})
		}
	}
	issuer, err := Load(testConfig(), rotationManifest(generation, phase, sequence, lastOldIssuance, entries), func(name string) ([]byte, error) { return keys[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func rotationManifest(generation uint64, phase RotationPhase, sequence uint64, lastOldIssuance int64, keys []rotationTestKey) []byte {
	items := ""
	for i, key := range keys {
		if i > 0 {
			items += ","
		}
		items += fmt.Sprintf(`{"name":%q,"active":%t}`, key.Name, key.Active)
	}
	return []byte(fmt.Sprintf(`{"version":2,"generation":%d,"phase":%q,"sequence":%d,"last_old_issuance_unix":%d,"keys":[%s]}`, generation, phase, sequence, lastOldIssuance, items))
}

func verifierForBundle(t *testing.T, bundle []byte) *Verifier {
	t.Helper()
	cfg := VerifierConfig{TrustDomain: testConfig().TrustDomain, Audience: testConfig().Audience, TokenTTL: testConfig().TokenTTL, ClockSkew: testConfig().ClockSkew, BundleCacheTTL: time.Minute, HTTPSBootstrapURL: testConfig().HTTPSBootstrapURL}
	verifier, err := NewVerifier(cfg, BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func bundleDigest(bundle []byte) string {
	digest := sha256.Sum256(bundle)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func bundleSequence(t *testing.T, bundle []byte) uint64 {
	t.Helper()
	_, sequence, err := parseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return sequence
}
