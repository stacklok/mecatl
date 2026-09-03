package clientauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// ErrIncompleteEnrollment means enrollment reached an ambiguous or partially
// reconciled local state. It never implies crash atomicity; a caller may safely
// inspect the registry and retry.
var ErrIncompleteEnrollment = errors.New("clientauth: enrollment incomplete")

// ErrTargetChanged means the target's registry or credential state at commit
// time no longer matches the ExpectedTarget/ExpectedCredential snapshot the
// caller supplied. Enroll performs no write in this case; the caller should
// report a distinct recovery reason rather than either silently succeeding
// (which could resurrect a logged-out target or clobber a newer enrollment)
// or crashing.
var ErrTargetChanged = errors.New("clientauth: target changed during sign-in")

// ExpectedCredentialState is Enroll's optional precondition on the enrolled
// identity's OWN credential state (not the registry's connection metadata --
// see EnrollmentConfig.ExpectedTarget for that), captured before an
// interactive step that can run arbitrarily long.
type ExpectedCredentialState struct {
	Found bool
	// Corrupt records that the preflight read hit ErrCorrupt for this
	// identity. Version is meaningless when Corrupt is true -- see
	// checkExpectedCredential.
	Corrupt bool
	Version credentialstore.Version
}

// EnrollmentConfig supplies the two durable halves of an enrollment.
type EnrollmentConfig struct {
	Registry    *Registry
	Credentials *Credentials
	// ExpectedTarget, when non-nil, is the target's connection snapshot taken
	// BEFORE an interactive step (e.g. a browser OAuth exchange) that can run
	// arbitrarily long. Enroll then requires the target's state at commit
	// time to match this snapshot exactly, returning ErrTargetChanged
	// otherwise -- closing the gap between a reauthentication preflight and
	// its eventual write-back, across which another process could log the
	// target out. A nil ExpectedTarget (the default, used by a fresh
	// enrollment) enrolls unconditionally.
	ExpectedTarget *[]Connection
	// ExpectedCredential, when non-nil, is the SAME identity's own credential
	// snapshot taken at the same preflight time. ExpectedTarget alone cannot
	// detect a newer credential enrolled for the identical identity (the
	// registry's connection metadata is unchanged; only the credential
	// store's version moved), so this closes that half of the same race:
	// Enroll rejects with ErrTargetChanged rather than overwrite a credential
	// newer than the one the caller preflighted against. A credential that
	// was corrupt at preflight time (ExpectedCredentialState.Corrupt) still
	// constrains the repair path -- see checkExpectedCredential -- rather
	// than leaving it entirely unconstrained.
	ExpectedCredential *ExpectedCredentialState
}

// checkExpectedTarget reports whether current matches cfg.ExpectedTarget (or
// whether no such precondition was requested at all).
func (cfg EnrollmentConfig) checkExpectedTarget(current []Connection) bool {
	return cfg.ExpectedTarget == nil || sameConnections(current, *cfg.ExpectedTarget)
}

// checkExpectedCredential reports whether current matches
// cfg.ExpectedCredential (or whether no such precondition was requested, or
// the credential is under repair -- see ExpectedCredential's doc comment).
func (cfg EnrollmentConfig) checkExpectedCredential(current credentialSnapshot) bool {
	expected := cfg.ExpectedCredential
	if expected == nil {
		return true
	}
	if expected.Corrupt {
		// The preflight saw an unusable record for this identity. Enroll's
		// repair path may proceed only if it is STILL unusable at commit
		// time -- if another process already repaired or replaced it, this
		// stale sign-in must not overwrite that healthy replacement.
		return current.unusable
	}
	if current.unusable {
		// The preflight saw either a healthy record or none; a record that
		// is corrupt NOW was changed by something else since preflight.
		return false
	}
	if current.found != expected.Found {
		return false
	}
	return !current.found || current.record.Version.Equal(expected.Version)
}

type credentialSnapshot struct {
	identity Identity
	record   CredentialRecord
	found    bool
	unusable bool
}

// Enroll stores a login token and makes conn the sole registry entry for its
// target. The caller must complete the interactive login before calling Enroll:
// target serialization begins only once a token is ready.
func Enroll(ctx context.Context, conn Connection, token Token, cfg EnrollmentConfig) error {
	if cfg.Registry == nil || cfg.Credentials == nil {
		return errors.New("clientauth: registry and credentials are required")
	}
	conn, err := normalizeConnection(conn)
	if err != nil {
		return err
	}
	id := conn.Identity
	if conn.ResourceURL != "" {
		unlockResource, lockErr := cfg.Registry.lockTarget(ctx, "resource:"+conn.ResourceURL)
		if lockErr != nil {
			return lockErr
		}
		defer unlockResource()
	}
	unlock, err := cfg.Registry.lockTarget(ctx, id.Target)
	if err != nil {
		return err
	}
	defer unlock()

	targetEntries, err := cfg.Registry.targetSnapshot(id.Target)
	if err != nil {
		return err
	}
	displacedEntries, err := cfg.Registry.enrollmentSnapshot(id.Target, conn.ResourceURL)
	if err != nil {
		return err
	}
	if !cfg.checkExpectedTarget(targetEntries) {
		return ErrTargetChanged
	}
	snapshots, err := snapshotEnrollmentCredentials(ctx, cfg.Credentials, id, displacedEntries)
	if err != nil {
		return err
	}
	newSnapshot := snapshots[0]
	if !cfg.checkExpectedCredential(newSnapshot) {
		return ErrTargetChanged
	}
	written, err := storeEnrollmentCredential(ctx, cfg.Credentials, id, token, newSnapshot)
	if err != nil {
		return err
	}

	// From here onward cancellation must not strand a state we can reconcile.
	txnCtx := context.WithoutCancel(ctx)
	desired := []Connection{conn}
	committed, registryErr := commitEnrollmentRegistry(cfg.Registry, id.Target, conn.ResourceURL, targetEntries, desired)
	if !committed {
		if newSnapshot.unusable {
			// The registry already identifies this exact credential. The repaired
			// version remains reachable even when a metadata rewrite fails, so never
			// destroy it in an attempt to restore unreadable bytes.
			return fmt.Errorf("%w: registry not committed after credential recovery", ErrIncompleteEnrollment)
		}
		if compensateErr := compensateEnrollment(txnCtx, cfg.Credentials, newSnapshot, written.Version); compensateErr != nil {
			return fmt.Errorf("%w: registry not committed and credential compensation lost CAS", ErrIncompleteEnrollment)
		}
		return fmt.Errorf("clientauth: commit enrollment registry: %w", registryErr)
	}

	for _, snapshot := range snapshots[1:] {
		if !snapshot.found {
			continue
		}
		if err := cfg.Credentials.Delete(txnCtx, snapshot.identity, snapshot.record.Version); err != nil && !IsNotEnrolled(err) {
			return fmt.Errorf("%w: superseded credential cleanup did not win CAS", ErrIncompleteEnrollment)
		}
	}
	return nil
}

func snapshotEnrollmentCredentials(ctx context.Context, creds *Credentials, id Identity, oldEntries []Connection) ([]credentialSnapshot, error) {
	identities := []Identity{id}
	for _, old := range oldEntries {
		if !containsIdentity(identities, old.Identity) {
			identities = append(identities, old.Identity)
		}
	}
	snapshots := make([]credentialSnapshot, 0, len(identities))
	for _, identity := range identities {
		rec, err := creds.Load(ctx, identity)
		if IsNotEnrolled(err) {
			snapshots = append(snapshots, credentialSnapshot{identity: identity})
			continue
		}
		if errors.Is(err, ErrCorrupt) && identity.Equal(id) && connectionsContainIdentity(oldEntries, id) {
			snapshots = append(snapshots, credentialSnapshot{identity: identity, found: true, unusable: true})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("clientauth: snapshot enrollment credential: %w", err)
		}
		snapshots = append(snapshots, credentialSnapshot{identity: identity, record: rec, found: true})
	}
	return snapshots, nil
}

func storeEnrollmentCredential(ctx context.Context, creds *Credentials, id Identity, token Token, snapshot credentialSnapshot) (CredentialRecord, error) {
	var (
		written CredentialRecord
		err     error
	)
	if snapshot.unusable {
		written, err = creds.replaceUnusable(ctx, id, token)
	} else {
		var expected *credentialstore.Version
		if snapshot.found {
			expected = &snapshot.record.Version
		}
		written, err = creds.Save(ctx, id, token, expected)
	}
	if err == nil {
		return written, nil
	}
	// A durable store may report a directory-sync failure after its rename
	// committed. Re-read under the target lock so an intended committed token
	// is not left unreachable merely because Save could not report its version.
	current, loadErr := creds.Load(context.WithoutCancel(ctx), id)
	if loadErr == nil && sameToken(current.Token, token) {
		return current, nil
	}
	if loadErr != nil && !IsNotEnrolled(loadErr) {
		return CredentialRecord{}, fmt.Errorf("%w: credential commit could not be determined", ErrIncompleteEnrollment)
	}
	return CredentialRecord{}, fmt.Errorf("clientauth: write enrollment credential: %w", err)
}

func commitEnrollmentRegistry(registry *Registry, target, resource string, expected, desired []Connection) (bool, error) {
	err := registry.replaceEnrollment(target, resource, expected, desired)
	if err == nil {
		return true, nil
	}
	current, readErr := registry.targetSnapshot(target)
	if readErr != nil {
		return false, fmt.Errorf("%w: registry commit could not be determined", ErrIncompleteEnrollment)
	}
	return sameConnections(current, desired), err
}

func sameToken(a, b Token) bool {
	return a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken && a.TokenType == b.TokenType && a.Expiry == b.Expiry
}

func compensateEnrollment(ctx context.Context, creds *Credentials, old credentialSnapshot, written credentialstore.Version) error {
	if old.found {
		_, err := creds.Save(ctx, old.identity, old.record.Token, &written)
		return err
	}
	return creds.Delete(ctx, old.identity, written)
}

func connectionsContainIdentity(all []Connection, identity Identity) bool {
	for _, conn := range all {
		if conn.Identity.Equal(identity) {
			return true
		}
	}
	return false
}

func containsIdentity(all []Identity, identity Identity) bool {
	for _, existing := range all {
		if existing.Equal(identity) {
			return true
		}
	}
	return false
}
