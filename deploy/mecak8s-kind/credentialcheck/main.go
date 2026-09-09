// Command credentialcheck qualifies only the disposable Kind credential fixture.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var errCheck = errors.New("credential-storage-check: failed")

func main() {
	if err := checkCredentials(os.Getenv("CHECK"), os.Getenv("ROOT"), os.Getenv("TARGET")); err != nil {
		// Never print store, provider, or identity-bearing errors.
		fmt.Fprintln(os.Stderr, "credential-storage-check=failed")
		os.Exit(1)
	}
}

func checkCredentials(check, root, target string) error {
	if check != "post-login" && check != "refresh" && check != "post-logout" {
		return errCheck
	}
	fixture, err := validateFixtureRoot(root)
	if err != nil {
		return err
	}
	if err := privateModes(root); err != nil {
		return err
	}
	if err := requireFilePin(root); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, backend, err := clientauth.OpenExistingCredentialStore(ctx, root)
	if err != nil {
		return errCheck
	}
	defer func() { _ = store.Close() }()
	if backend != clientauth.CredentialBackendFile {
		return errCheck
	}
	registry, err := clientauth.OpenExistingRegistry(root)
	if err != nil {
		return errCheck
	}
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		return errCheck
	}
	// The qualification fixture has one fixed, explicitly enrolled identity.
	// Retaining that identity here permits post-logout verification without reading
	// raw records or writing an identity/token sidecar before logout.
	id, err := (clientauth.Identity{Target: target, Issuer: "https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl", ClientID: "mecatui-kind", Audience: "mecak8s", RedirectURI: oauthlogin.ExactRedirectURL, Scopes: []string{"openid", "profile", "mecak8s:access", "offline_access"}}).Canonical()
	if err != nil {
		return errCheck
	}
	conn, registryErr := registry.Find(target)
	before, credentialErr := creds.Load(ctx, id)
	if check == "post-logout" {
		if !errors.Is(registryErr, credentialstore.ErrNotFound) || !errors.Is(credentialErr, credentialstore.ErrNotFound) {
			return errCheck
		}
		fmt.Println("backend=file private-modes=ok registry=absent credential=absent")
		return nil
	}
	if registryErr != nil || credentialErr != nil || !conn.Identity.Equal(id) {
		return errCheck
	}
	if check == "post-login" {
		fmt.Println("backend=file private-modes=ok registry=present credential=present")
		return nil
	}
	return checkRefresh(ctx, fixture, creds, registry, id, before)
}

func checkRefresh(ctx context.Context, fixture string, creds *clientauth.Credentials, registry *clientauth.Registry, id clientauth.Identity, before clientauth.CredentialRecord) error {
	expiry, err := time.Parse(time.RFC3339, before.Token.Expiry)
	if err != nil || time.Until(expiry) > 30*time.Second {
		return errCheck
	}
	ca, err := os.ReadFile(filepath.Join(fixture, "fixture-ca.crt"))
	if err != nil {
		return errCheck
	}
	source, err := clientauth.NewRefreshSource(ctx, creds, clientauth.LoginConfig{Identity: id, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPrivate, TrustedCAPEM: ca, Registry: registry})
	if err != nil {
		return errCheck
	}
	defer func() { _ = source.Close() }()
	access, err := source.Token(ctx)
	if err != nil || access == "" {
		return errCheck
	}
	after, err := creds.Load(ctx, id)
	if err != nil || after.Token.AccessToken != access || access == before.Token.AccessToken || after.Token.Expiry == before.Token.Expiry {
		return errCheck
	}
	expiry, err = time.Parse(time.RFC3339, after.Token.Expiry)
	if err != nil || !expiry.After(time.Now()) || after.Token.RefreshToken == "" {
		return errCheck
	}
	rotation := "retained"
	if after.Token.RefreshToken != before.Token.RefreshToken {
		rotation = "rotated"
	}
	fmt.Println("refresh-persisted refresh-token=" + rotation)
	return nil
}

// Inspect only non-secret backend metadata before invoking the authoritative
// opener, so qualification can never contact an unrelated real keyring.
func requireFilePin(root string) error {
	// #nosec G703 -- validateFixtureRoot confines root to the disposable fixture and privateModes rejects links; the authoritative opener validates the marker again.
	file, err := os.Open(filepath.Join(root, "clientauth-credential-backend.json"))
	if err != nil {
		return errCheck
	}
	defer func() { _ = file.Close() }()
	var marker struct {
		Version int    `json:"version"`
		Backend string `json:"backend"`
	}
	if json.NewDecoder(io.LimitReader(file, 4096)).Decode(&marker) != nil || marker.Version != 1 || marker.Backend != "file" {
		return errCheck
	}
	return nil
}

func validateFixtureRoot(root string) (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		return "", errCheck
	}
	fixture := filepath.Join(cwd, ".scratch", "kind", "mecatl-dev")
	physical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) {
		return "", errCheck
	}
	relative, err := filepath.Rel(fixture, physical)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errCheck
	}
	return fixture, nil
}

func privateModes(root string) error {
	// #nosec G703 -- validateFixtureRoot confined this operator-supplied root to the disposable fixture; WalkDir never follows symlinks.
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return errCheck
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errCheck
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0700 {
				return errCheck
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errCheck
		}
		return nil
	})
}
