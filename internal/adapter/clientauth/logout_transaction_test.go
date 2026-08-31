package clientauth

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestLogoutPostRenameErrorIsConfirmedCommitted(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	id := identity("logout-postrename.example:443")
	if _, err := reg.Upsert(Connection{Identity: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), id, Token{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	reg.writeFault = func(stage string) error {
		if stage == "postrename" {
			return errors.New("injected post-rename failure")
		}
		return nil
	}
	result, err := Logout(t.Context(), id.Target, LogoutConfig{Registry: reg, Credentials: creds})
	if err != nil || !result.RegistryDeleted {
		t.Fatalf("Logout = %#v, %v", result, err)
	}
}

func TestLogoutRegistryCASWinnerIsPreserved(t *testing.T) {
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	oldID := identity("logout-winner.example:443")
	winnerID := oldID
	winnerID.Issuer = "https://winner.example"
	if _, err := reg.Upsert(Connection{Identity: oldID}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), oldID, Token{AccessToken: "old", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), winnerID, Token{AccessToken: "winner", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	reg.writeFault = func(stage string) error {
		if stage != "precommit" {
			return nil
		}
		body, marshalErr := json.Marshal(struct {
			Version     int          `json:"version"`
			Connections []Connection `json:"connections"`
		}{Version: 1, Connections: []Connection{{Identity: winnerID}}})
		if marshalErr != nil {
			return marshalErr
		}
		if writeErr := os.WriteFile(reg.path, body, 0o600); writeErr != nil {
			return writeErr
		}
		return errors.New("injected registry CAS winner")
	}
	result, err := Logout(t.Context(), oldID.Target, LogoutConfig{Registry: reg, Credentials: creds})
	if !errors.Is(err, ErrIncompleteLogout) || result.RegistryDeleted {
		t.Fatalf("Logout = %#v, %v", result, err)
	}
	got, err := reg.FindTarget(oldID.Target)
	if err != nil || !got.Identity.Equal(winnerID) {
		t.Fatalf("registry winner = %#v, %v", got, err)
	}
	rec, err := creds.Load(t.Context(), winnerID)
	if err != nil || rec.Token.AccessToken != "winner" {
		t.Fatalf("credential winner = %#v, %v", rec.Token, err)
	}
}
