package clientauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type renameThenSyncFaultStore struct {
	credentialstore.Store
	failNext bool
}

func (s *renameThenSyncFaultStore) Put(ctx context.Context, key, value []byte, expected *credentialstore.Version) (credentialstore.Record, error) {
	rec, err := s.Store.Put(ctx, key, value, expected)
	if err == nil && s.failNext {
		s.failNext = false
		return credentialstore.Record{}, errors.New("injected directory sync failure after rename")
	}
	return rec, err
}

func corruptEncryptedCredential(t *testing.T, root string) {
	t.Helper()
	var credentialPath string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && strings.HasSuffix(info.Name(), ".cred") {
			credentialPath = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentialPath == "" {
		t.Fatal("encrypted credential file not found")
	}
	if err := os.WriteFile(credentialPath, []byte("corrupt-envelope"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollRecoversCorruptCurrentCredentialWithoutStrandingRegistry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*testing.T, string, *credentialstore.EncryptedFileStore, *Credentials, Identity)
	}{
		{name: "authenticated record has malformed payload", corrupt: func(t *testing.T, _ string, store *credentialstore.EncryptedFileStore, creds *Credentials, id Identity) {
			rec, err := creds.Load(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			key, _ := id.recordKey()
			if _, err := store.Put(t.Context(), key, []byte("{"), &rec.Version); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "encrypted envelope is unreadable", corrupt: func(t *testing.T, root string, _ *credentialstore.EncryptedFileStore, _ *Credentials, _ Identity) {
			corruptEncryptedCredential(t, root)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			reg, err := OpenRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			store, err := credentialstore.NewEncryptedFile(root, "reauth-recovery", bytesOf(12))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			creds, _ := NewCredentials(store)
			id := identity("reauth-corrupt.example:443")
			conn := Connection{Identity: id, IssuerCAFile: "/issuer-ca.pem"}
			if _, err := reg.Upsert(conn); err != nil {
				t.Fatal(err)
			}
			if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, root, store, creds, id)
			newToken := Token{AccessToken: "new", RefreshToken: "new-refresh", TokenType: "Bearer"}
			if err := Enroll(t.Context(), conn, newToken, EnrollmentConfig{Registry: reg, Credentials: creds}); err != nil {
				t.Fatalf("Enroll recovery: %v", err)
			}
			gotConn, err := reg.FindTarget(id.Target)
			if err != nil || !gotConn.Identity.Equal(id) {
				t.Fatalf("registry after recovery = %#v, %v", gotConn, err)
			}
			rec, err := creds.Load(t.Context(), id)
			if err != nil || !sameToken(rec.Token, newToken) {
				t.Fatalf("credential after recovery = %#v, %v", rec.Token, err)
			}
		})
	}
}

func TestEnrollPreservesQuarantinedRegistryRows(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentialstore.NewEncryptedFile(root, "enroll-quarantine", bytesOf(14))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, _ := NewCredentials(store)
	id := identity("reenroll.example:443")
	conn := Connection{Identity: id, IssuerCAFile: "/issuer-ca.pem"}
	if _, err := reg.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	badRow := json.RawMessage(`{"identity":{"Target":"quarantined.example:443"},"unknown_future_field":"keep-byte-for-byte"}`)
	body, err := json.Marshal(map[string]any{"version": 1, "connections": []json.RawMessage{badRow, mustJSON(t, conn)}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "clientauth-connections.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(t.Context(), conn, Token{AccessToken: "new", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(persisted, badRow) {
		t.Fatalf("enrollment dropped quarantined raw row: %s", persisted)
	}
	if _, err := reg.FindTarget("quarantined.example:443"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("quarantined row became publicly readable: %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEnrollCorruptRecoveryRegistryFailureRetainsReachableReplacement(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentialstore.NewEncryptedFile(root, "reauth-reachable", bytesOf(13))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, _ := NewCredentials(store)
	id := identity("reauth-reachable.example:443")
	oldConn := Connection{Identity: id, IssuerCAFile: "/old-issuer-ca.pem"}
	conn := Connection{Identity: id, IssuerCAFile: "/new-issuer-ca.pem"}
	if _, err := reg.Upsert(oldConn); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	corruptEncryptedCredential(t, root)
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			return errors.New("injected registry failure")
		}
		return nil
	}
	newToken := Token{AccessToken: "replacement", TokenType: "Bearer"}
	if err := Enroll(t.Context(), conn, newToken, EnrollmentConfig{Registry: reg, Credentials: creds}); !errors.Is(err, ErrIncompleteEnrollment) {
		t.Fatalf("Enroll error = %v", err)
	}
	if _, err := reg.FindTarget(id.Target); err != nil {
		t.Fatalf("registry was stranded: %v", err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || rec.Token.AccessToken != newToken.AccessToken {
		t.Fatalf("replacement was destroyed: %#v, %v", rec.Token, err)
	}
}

func TestEnrollReconcilesCredentialRenameThenSyncFailure(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentialstore.NewEncryptedFile(root, "enroll-ambiguous-save", bytesOf(4))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	faults := &renameThenSyncFaultStore{Store: store, failNext: true}
	creds, err := NewCredentials(faults)
	if err != nil {
		t.Fatal(err)
	}
	id := identity("ambiguous-save.example:443")
	token := Token{AccessToken: "intended-secret", RefreshToken: "refresh-secret", TokenType: "Bearer"}
	if err := Enroll(t.Context(), Connection{Identity: id}, token, EnrollmentConfig{Registry: reg, Credentials: creds}); err != nil {
		t.Fatalf("Enroll error = %v", err)
	}
	conn, err := reg.FindTarget(id.Target)
	if err != nil || !conn.Identity.Equal(id) {
		t.Fatalf("registry commit = %#v, %v", conn, err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || !sameToken(rec.Token, token) {
		t.Fatalf("credential was not retained after reconciled save: %v", err)
	}
}

func TestEnrollCompensatesReconciledAmbiguousSave(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			return errors.New("injected registry failure")
		}
		return nil
	}
	store, err := credentialstore.NewEncryptedFile(root, "enroll-ambiguous-compensation", bytesOf(5))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	faults := &renameThenSyncFaultStore{Store: store, failNext: true}
	creds, err := NewCredentials(faults)
	if err != nil {
		t.Fatal(err)
	}
	id := identity("ambiguous-compensation.example:443")
	token := Token{AccessToken: "must-not-leak", TokenType: "Bearer"}
	err = Enroll(t.Context(), Connection{Identity: id}, token, EnrollmentConfig{Registry: reg, Credentials: creds})
	if err == nil || strings.Contains(err.Error(), token.AccessToken) {
		t.Fatalf("Enroll returned unsafe or missing error: %v", err)
	}
	if _, err := reg.FindTarget(id.Target); !IsNotEnrolled(err) {
		t.Fatalf("registry unexpectedly committed: %v", err)
	}
	if _, err := creds.Load(t.Context(), id); !IsNotEnrolled(err) {
		t.Fatalf("reconciled credential survived compensation: %v", err)
	}
}

func TestEnrollPrecommitFailureRestoresPriorCredential(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("rollback.example:443")
	old := Token{AccessToken: "old", TokenType: "Bearer"}
	if _, err := creds.Upsert(t.Context(), id, old); err != nil {
		t.Fatal(err)
	}
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			return errors.New("injected precommit failure")
		}
		return nil
	}
	err = Enroll(t.Context(), Connection{Identity: id}, Token{AccessToken: "new", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds})
	if err == nil || errors.Is(err, ErrIncompleteEnrollment) {
		t.Fatalf("Enroll error = %v", err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || rec.Token.AccessToken != old.AccessToken {
		t.Fatalf("credential after rollback = %#v, %v", rec.Token, err)
	}
	if _, err := reg.FindTarget(id.Target); !IsNotEnrolled(err) {
		t.Fatalf("registry unexpectedly committed: %v", err)
	}
}

func TestEnrollCancellationAfterCredentialWriteCompensates(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("cancel-compensate.example:443")
	ctx, cancel := context.WithCancel(t.Context())
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := Enroll(ctx, Connection{Identity: id}, Token{AccessToken: "new", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Enroll error = %v", err)
	}
	if _, err := creds.Load(t.Context(), id); !IsNotEnrolled(err) {
		t.Fatalf("credential survived cancelled enrollment: %v", err)
	}
}

func TestEnrollCreatedCredentialCompensationDeletesOnlyWrittenVersion(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("created-rollback.example:443")
	reg.writeFault = func(stage string) error {
		if stage == "precommit" {
			return errors.New("injected precommit failure")
		}
		return nil
	}
	if err := Enroll(t.Context(), Connection{Identity: id}, Token{AccessToken: "operation", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds}); err == nil {
		t.Fatal("Enroll unexpectedly succeeded")
	}
	if _, err := creds.Load(t.Context(), id); !IsNotEnrolled(err) {
		t.Fatalf("operation-created credential survived compensation: %v", err)
	}
}

func TestEnrollPostRenameErrorIsConfirmedCommitted(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("postrename.example:443")
	reg.writeFault = func(stage string) error {
		if stage == "postrename" {
			return errors.New("injected post-rename failure")
		}
		return nil
	}
	if err := Enroll(t.Context(), Connection{Identity: id}, Token{AccessToken: "new", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.FindTarget(id.Target); err != nil {
		t.Fatalf("committed registry unavailable: %v", err)
	}
}

func TestEnrollCompensationPreservesConcurrentCredentialWinner(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("winner.example:443")
	winner := Token{AccessToken: "winner", TokenType: "Bearer"}
	reg.writeFault = func(stage string) error {
		if stage != "precommit" {
			return nil
		}
		written, loadErr := creds.Load(t.Context(), id)
		if loadErr != nil {
			return loadErr
		}
		_, saveErr := creds.Save(t.Context(), id, winner, &written.Version)
		if saveErr != nil {
			return saveErr
		}
		return errors.New("injected precommit failure")
	}
	err = Enroll(t.Context(), Connection{Identity: id}, Token{AccessToken: "operation", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds})
	if !errors.Is(err, ErrIncompleteEnrollment) {
		t.Fatalf("Enroll error = %v", err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || rec.Token.AccessToken != winner.AccessToken {
		t.Fatalf("CAS winner = %#v, %v", rec.Token, err)
	}
}

func TestTargetTransactionsAreContextAwareAndTargetScoped(t *testing.T) {
	root := t.TempDir()
	first, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := first.lockTarget(t.Context(), "locked.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := second.lockTarget(ctx, "locked.example:443"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error = %v", err)
	}
	otherUnlock, err := second.lockTarget(t.Context(), "other.example:443")
	if err != nil {
		t.Fatalf("independent target blocked: %v", err)
	}
	otherUnlock()
}

func TestEnrollAndLogoutSerializeAcrossRegistryHandles(t *testing.T) {
	root := t.TempDir()
	first, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("serialized.example:443")
	if _, err := first.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	unlock, err := first.lockTarget(t.Context(), id.Target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, logoutErr := Logout(ctx, id.Target, LogoutConfig{Registry: second, Credentials: creds})
		done <- logoutErr
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("contended logout error = %v", err)
	}
	unlock()
	if _, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: second, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedCrashAfterRegistryCommitCanLeaveDisplacedCredential(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	oldID := identity("displaced-crash.example:443")
	newID := oldID
	newID.Issuer = "https://new.example"
	if _, err := creds.Upsert(t.Context(), oldID, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), newID, Token{AccessToken: "new", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	// This is the accepted no-journal crash point: registry rename committed but
	// the process stopped before CAS-deleting the displaced credential. The old
	// identity is no longer discoverable from registry state, so no crash-atomic
	// cleanup claim is made.
	if _, err := reg.Upsert(Connection{Identity: newID}); err != nil {
		t.Fatal(err)
	}
	if rec, err := creds.Load(t.Context(), oldID); err != nil || rec.Token.AccessToken != "old" {
		t.Fatalf("accepted displaced crash residual = %#v, %v", rec.Token, err)
	}
}

func TestEnrollReconcilesAcceptedCredentialOnlyCrashState(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("crash-partial.example:443")
	// No journal exists: a process can stop after this credential write and before
	// registry commit. A later enrollment treats that accepted partial state as its
	// old credential snapshot and reconciles it with CAS.
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "partial", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(t.Context(), Connection{Identity: id}, Token{AccessToken: "retry", TokenType: "Bearer"}, EnrollmentConfig{Registry: reg, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	rec, err := creds.Load(t.Context(), id)
	if err != nil || rec.Token.AccessToken != "retry" {
		t.Fatalf("reconciled credential = %#v, %v", rec.Token, err)
	}
}
