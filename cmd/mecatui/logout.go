package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	registry, err := clientauth.OpenExistingRegistry(root)
	if err != nil {
		return fmt.Errorf("logout: opening the connection registry failed: %w", err)
	}

	var creds *clientauth.Credentials
	keys, keyErr := clientauth.NewExistingKeyringProvider(root)
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
		HTTPClientOwned: logoutHTTPClient,
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

type issuerRevocationTransport struct {
	public             http.RoundTripper
	private            http.RoundTripper
	privateAuthorities map[string]struct{}
}

func (t issuerRevocationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := t.privateAuthorities[strings.ToLower(req.URL.Host)]; ok {
		return t.private.RoundTrip(req)
	}
	return t.public.RoundTrip(req)
}

func (t issuerRevocationTransport) CloseIdleConnections() {
	if closer, ok := t.public.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	if closer, ok := t.private.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// logoutHTTPClient keeps private issuer roots confined to their authorities while
// public issuers continue to use the system trust store.
func logoutHTTPClient(ctx context.Context, conns []clientauth.Connection) (*http.Client, bool, error) {
	endpointCAs := make(map[string][]byte, len(conns))
	privateAuthorities := make(map[string]struct{}, len(conns))
	for _, conn := range conns {
		if conn.IssuerCAFile == "" {
			continue
		}
		ca, err := os.ReadFile(conn.IssuerCAFile)
		if err != nil {
			return nil, false, err
		}
		issuer, err := url.Parse(conn.Identity.Issuer)
		if err != nil {
			return nil, false, err
		}
		privateAuthorities[strings.ToLower(issuer.Host)] = struct{}{}
		endpointCAs[conn.Identity.Issuer] = append(append(endpointCAs[conn.Identity.Issuer], ca...), '\n')
	}

	publicTransport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{
		Transport: publicTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("HTTPS redirect refused")
		},
	}
	if len(endpointCAs) == 0 {
		return client, true, nil
	}
	privateClient, err := scopedhttps.NewClient(ctx, endpointCAs)
	if err != nil {
		return nil, false, err
	}
	client.Transport = issuerRevocationTransport{
		public:             publicTransport,
		private:            privateClient.Transport,
		privateAuthorities: privateAuthorities,
	}
	return client, true, nil
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
