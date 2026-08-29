package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func runRemoteLogout(address string, args []string) error {
	fs := flag.NewFlagSet("mecatui logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mecatui logout ADDRESS")
		fmt.Fprintln(os.Stderr, "Remove the saved target and credential. Provider revocation is bounded best effort; local removal still proceeds if the issuer is unavailable.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("logout: unexpected arguments after ADDRESS; usage: mecatui logout ADDRESS")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	registry, err := clientauth.OpenRegistry(root)
	if err != nil {
		return fmt.Errorf("logout: opening the connection registry failed: %w", err)
	}

	var creds *clientauth.Credentials
	keys, keyErr := clientauth.NewKeyringProvider(root)
	var store *credentialstore.EncryptedFileStore
	var storeErr error
	if keyErr == nil {
		store, storeErr = clientauth.OpenExistingStore(ctx, root, keys)
	} else {
		storeErr = keyErr
	}
	if storeErr == nil {
		defer func() { _ = store.Close() }()
		creds, _ = clientauth.NewCredentials(store)
	}
	result, err := clientauth.Logout(ctx, address, clientauth.LogoutConfig{
		Registry: registry, Credentials: creds,
		HTTPClient: func(ctx context.Context, conn clientauth.Connection) (*http.Client, error) {
			ca, err := os.ReadFile(conn.IssuerCAFile)
			if err != nil {
				return nil, err
			}
			return scopedhttps.NewClient(ctx, []string{conn.Identity.Issuer}, ca)
		},
	})
	if err != nil {
		if errors.Is(err, clientauth.ErrIncompleteLogout) {
			writeLogoutResult(os.Stderr, result)
			return fmt.Errorf("logout: local cleanup incomplete: %w", err)
		}
		return fmt.Errorf("logout: %w", err)
	}
	writeLogoutResult(os.Stderr, result)
	return nil
}

func writeLogoutResult(out io.Writer, result clientauth.LogoutResult) {
	if result.Entries == 0 {
		_, _ = fmt.Fprintf(out, "no saved login for %s\n", result.Target)
		return
	}
	if result.RegistryDeleted {
		_, _ = fmt.Fprintf(out, "removed saved login for %s (%d credential(s) removed, %d already absent)\n", result.Target, result.CredentialsDeleted, result.CredentialsMissing)
	} else {
		_, _ = fmt.Fprintf(out, "logout for %s is incomplete; saved metadata was retained to keep unresolved credentials reachable\n", result.Target)
	}
	if result.RevocationsFailed > 0 {
		_, _ = fmt.Fprintln(out, "provider revocation was unavailable or incomplete; local cleanup was attempted independently")
		if result.RevocationError != "" {
			_, _ = fmt.Fprintf(out, "revocation cause: %s\n", result.RevocationError)
		}
	}
	for _, issue := range result.Issues {
		if issue.Identity.ClientID != "" {
			_, _ = fmt.Fprintf(out, "retained identity issuer=%q client-id=%q: %s\n", issue.Identity.Issuer, issue.Identity.ClientID, issue.Stage)
		} else {
			_, _ = fmt.Fprintf(out, "logout state: %s\n", issue.Stage)
		}
	}
}
