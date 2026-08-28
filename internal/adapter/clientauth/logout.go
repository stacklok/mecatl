package clientauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

const revocationTimeout = 5 * time.Second

// ErrIncompleteLogout indicates that local state could not be fully reconciled.
// Provider revocation failures do not produce this error: local logout remains
// successful when the issuer is unavailable.
var ErrIncompleteLogout = errors.New("clientauth: logout incomplete")

// IncompleteLogoutError carries only the secret-free outcome for a failed local
// reconciliation.
type IncompleteLogoutError struct{}

func (*IncompleteLogoutError) Error() string { return ErrIncompleteLogout.Error() }
func (*IncompleteLogoutError) Unwrap() error { return ErrIncompleteLogout }

// LogoutIssue is a secret-free description of local state that logout could not remove.
type LogoutIssue struct {
	Identity Identity
	Stage    string
}

// LogoutResult reports exactly which local halves were removed. It never contains tokens.
type LogoutResult struct {
	Target               string
	Entries              int
	CredentialsDeleted   int
	CredentialsMissing   int
	RegistryDeleted      bool
	RevocationsAttempted int
	RevocationsFailed    int
	Issues               []LogoutIssue
}

// LogoutConfig supplies local state and an optional issuer-scoped HTTP client.
// A nil Credentials repository retains registry metadata rather than making a
// credential unreachable. HTTPClient failures affect revocation only.
type LogoutConfig struct {
	Registry    *Registry
	Credentials *Credentials
	HTTPClient  func(context.Context, Connection) (*http.Client, error)
}

type pendingRevocation struct {
	conn  Connection
	token Token
}

// Logout removes credentials before their target metadata. CAS conflicts and
// unreadable credentials retain the registry entry so another process's token
// rotation never becomes an unreachable orphan. Provider revocation is bounded
// best effort and never blocks local deletion.
func Logout(ctx context.Context, target string, cfg LogoutConfig) (LogoutResult, error) {
	result := LogoutResult{Target: target}
	if cfg.Registry == nil {
		return result, errors.New("clientauth: registry is required")
	}
	canonical, err := canonicalTarget(target)
	if err != nil {
		return result, ErrInvalidIdentity
	}
	result.Target = canonical
	unlock, err := cfg.Registry.lockTarget(ctx, canonical)
	if err != nil {
		return result, err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	all, err := cfg.Registry.List()
	if err != nil {
		return result, err
	}
	entries := make([]Connection, 0, 1)
	for _, conn := range all {
		if conn.Identity.Target == canonical {
			entries = append(entries, conn)
		}
	}
	result.Entries = len(entries)
	if len(entries) == 0 {
		return result, nil
	}

	canDeleteRegistry, revoke := removeLogoutCredentials(ctx, entries, cfg.Credentials, &result)
	if !canDeleteRegistry {
		return result, &IncompleteLogoutError{}
	}
	removed, err := cfg.Registry.DeleteTarget(canonical, entries)
	if err != nil {
		current, readErr := cfg.Registry.targetSnapshot(canonical)
		if readErr == nil && len(current) == 0 {
			// Rename is the commit point. A later chmod/fsync-style error does not
			// make an already-absent target present again.
			removed = len(entries)
		} else {
			stage := "registry_delete_failed"
			if errors.Is(err, credentialstore.ErrConflict) || (readErr == nil && !sameConnections(current, entries)) {
				stage = "registry_changed"
			}
			result.Issues = append(result.Issues, LogoutIssue{Stage: stage})
			return result, &IncompleteLogoutError{}
		}
	}
	result.RegistryDeleted = removed > 0
	unlock()
	locked = false

	revokeLogoutTokens(ctx, revoke, cfg.HTTPClient, &result)
	return result, nil
}

func revokeLogoutTokens(ctx context.Context, revoke []pendingRevocation, clientFor func(context.Context, Connection) (*http.Client, error), result *LogoutResult) {
	// Revocation is deliberately after local cleanup and outside the target
	// transaction. One operation-wide budget covers client construction (including
	// DNS), discovery, and every token; a stalled issuer must not block enrollment.
	cleanupCtx, cancelCleanup := context.WithTimeout(ctx, revocationTimeout)
	defer cancelCleanup()
	for _, item := range revoke {
		remaining := revocableTokenCount(item.token)
		if remaining == 0 || clientFor == nil {
			continue
		}
		if cleanupCtx.Err() != nil {
			result.RevocationsFailed += remaining
			continue
		}
		client, err := clientFor(cleanupCtx, item.conn)
		if err != nil {
			result.RevocationsFailed += remaining
			continue
		}
		attempted, failed := revokeTokens(cleanupCtx, client, item.conn.Identity, item.token)
		result.RevocationsAttempted += attempted
		result.RevocationsFailed += failed
	}
}

func removeLogoutCredentials(ctx context.Context, entries []Connection, creds *Credentials, result *LogoutResult) (bool, []pendingRevocation) {
	canDeleteRegistry := true
	var revoke []pendingRevocation
	for _, conn := range entries {
		if creds == nil {
			result.Issues = append(result.Issues, LogoutIssue{Identity: conn.Identity, Stage: "credential_store_unavailable"})
			canDeleteRegistry = false
			continue
		}
		rec, deleted, missing, stage := removeCredential(ctx, creds, conn.Identity)
		if missing {
			result.CredentialsMissing++
			continue
		}
		if stage != "" {
			result.Issues = append(result.Issues, LogoutIssue{Identity: conn.Identity, Stage: stage})
			canDeleteRegistry = false
			continue
		}
		if deleted {
			result.CredentialsDeleted++
			revoke = append(revoke, pendingRevocation{conn: conn, token: rec.Token})
		}
	}
	return canDeleteRegistry, revoke
}

func removeCredential(ctx context.Context, creds *Credentials, id Identity) (CredentialRecord, bool, bool, string) {
	rec, err := creds.Load(ctx, id)
	if IsNotEnrolled(err) {
		return CredentialRecord{}, false, true, ""
	}
	if err != nil {
		return CredentialRecord{}, false, false, "credential_unreadable"
	}
	deleteErr := creds.Delete(ctx, id, rec.Version)
	if errors.Is(deleteErr, credentialstore.ErrConflict) {
		// One reload-and-retry is enough to follow a concurrent refresh without
		// turning logout into an unbounded race with a writer.
		current, reloadErr := creds.Load(ctx, id)
		if IsNotEnrolled(reloadErr) {
			return CredentialRecord{}, true, false, ""
		}
		if reloadErr != nil {
			return CredentialRecord{}, false, false, "credential_unreadable"
		}
		rec = current
		deleteErr = creds.Delete(ctx, id, rec.Version)
	}
	if deleteErr != nil && !IsNotEnrolled(deleteErr) {
		if errors.Is(deleteErr, credentialstore.ErrConflict) {
			return CredentialRecord{}, false, false, "credential_changed"
		}
		return CredentialRecord{}, false, false, "credential_delete_failed"
	}
	return rec, true, false, ""
}

func revocableTokenCount(token Token) int {
	count := 0
	if token.RefreshToken != "" {
		count++
	}
	if token.AccessToken != "" {
		count++
	}
	return count
}

func revokeTokens(ctx context.Context, client *http.Client, id Identity, token Token) (attempted, failed int) {
	remaining := revocableTokenCount(token)
	if client == nil || ctx.Err() != nil {
		return 0, remaining
	}
	endpoint, err := discoverRevocationEndpoint(ctx, client, id)
	if err != nil || endpoint == "" {
		return 0, remaining
	}
	values := []struct {
		value string
		hint  string
	}{{token.RefreshToken, "refresh_token"}, {token.AccessToken, "access_token"}}
	for i, item := range values {
		if item.value == "" {
			continue
		}
		if ctx.Err() != nil {
			for _, skipped := range values[i:] {
				if skipped.value != "" {
					failed++
				}
			}
			break
		}
		form := url.Values{"token": {item.value}, "token_type_hint": {item.hint}, "client_id": {id.ClientID}}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if reqErr != nil {
			failed++
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if ctx.Err() != nil {
			failed++
			for _, skipped := range values[i+1:] {
				if skipped.value != "" {
					failed++
				}
			}
			break
		}
		attempted++
		res, doErr := client.Do(req)
		if doErr != nil {
			failed++
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			failed++
		}
	}
	return attempted, failed
}

func discoverRevocationEndpoint(ctx context.Context, client *http.Client, id Identity) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(id.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("revocation discovery failed")
	}
	var doc struct {
		Issuer             string `json:"issuer"`
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return "", errors.New("revocation discovery failed")
	}
	issuer, err := canonicalIssuerURL(doc.Issuer)
	if err != nil || issuer != id.Issuer {
		return "", errors.New("revocation issuer mismatch")
	}
	if doc.RevocationEndpoint == "" {
		return "", nil
	}
	u, err := url.Parse(doc.RevocationEndpoint)
	if err != nil || u.Scheme != httpsScheme || u.Host != mustHost(id.Issuer) || u.User != nil || u.Fragment != "" {
		return "", errors.New("revocation endpoint drift")
	}
	return u.String(), nil
}
