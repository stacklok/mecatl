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

// EnrollmentConfig supplies the two durable halves of an enrollment.
type EnrollmentConfig struct {
	Registry    *Registry
	Credentials *Credentials
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
	id, err := conn.Identity.Canonical()
	if err != nil {
		return err
	}
	if !validIssuerCAFile(conn.IssuerCAFile) {
		return errors.New("clientauth: issuer CA path must be absolute and clean")
	}
	conn.Identity = id
	unlock, err := cfg.Registry.lockTarget(ctx, id.Target)
	if err != nil {
		return err
	}
	defer unlock()

	oldEntries, err := cfg.Registry.targetSnapshot(id.Target)
	if err != nil {
		return err
	}
	snapshots, err := snapshotEnrollmentCredentials(ctx, cfg.Credentials, id, oldEntries)
	if err != nil {
		return err
	}
	newSnapshot := snapshots[0]
	written, err := storeEnrollmentCredential(ctx, cfg.Credentials, id, token, newSnapshot)
	if err != nil {
		return err
	}

	// From here onward cancellation must not strand a state we can reconcile.
	txnCtx := context.WithoutCancel(ctx)
	desired := []Connection{conn}
	committed, registryErr := commitEnrollmentRegistry(cfg.Registry, id.Target, oldEntries, desired)
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

func commitEnrollmentRegistry(registry *Registry, target string, expected, desired []Connection) (bool, error) {
	err := registry.replaceTarget(target, expected, desired)
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
