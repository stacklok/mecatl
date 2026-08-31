package clientauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// revocationTimeout must clear DNS resolution, not just the HTTP round trips:
// a hostname ending in "cluster.local" (Kubernetes' default cluster domain) is
// resolved via mDNS on macOS clients, which imposes a deterministic ~5s stall
// per lookup before falling back to /etc/hosts. scopedhttps resolves twice by
// design (once to approve the endpoint at construction, once more as a TOCTOU
// check on the first dial) before any connection is reused, so the guaranteed
// floor is ~10s; 15s leaves room for TLS plus discovery and revoke on top.
const revocationTimeout = 15 * time.Second

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
	// RevocationError is the first cause of a revocation failure (a DNS,
	// discovery, or HTTP error), secret-free. Empty unless RevocationsFailed > 0.
	RevocationError string
	Issues          []LogoutIssue
}

// LogoutConfig supplies local state and an optional issuer-scoped HTTP client.
// A nil Credentials repository retains registry metadata rather than making a
// credential unreachable. HTTPClient failures affect revocation only. Both
// client builders are called AT MOST ONCE per Logout call, with every
// retained connection needing revocation, so a single client (its scopedhttps
// dial-approval policy spans every retained issuer) is reused across the
// whole operation instead of rebuilt per credential.
type LogoutConfig struct {
	Registry    *Registry
	Credentials *Credentials
	HTTPClient  func(context.Context, []Connection) (*http.Client, error)
	// HTTPClientOwned may return one client for this whole revocation attempt
	// and whether Logout owns its idle-connection cleanup. HTTPClient remains
	// caller-owned.
	HTTPClientOwned func(context.Context, []Connection) (*http.Client, bool, error)
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
	// An existing-only registry whose root was never created has no state to log
	// out of; lockTarget's flock file would otherwise fail with ENOENT trying to
	// create a lock inside a directory that doesn't exist. Nothing else can have
	// created it between the two calls below except a genuine enrollment, which
	// legitimately races with logout the same way any check-then-act would.
	if _, statErr := os.Stat(cfg.Registry.root); errors.Is(statErr, os.ErrNotExist) {
		return result, nil
	}
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

	revokeLogoutTokens(ctx, revoke, cfg.HTTPClient, cfg.HTTPClientOwned, &result)
	return result, nil
}

func revokeLogoutTokens(ctx context.Context, revoke []pendingRevocation, clientFor func(context.Context, []Connection) (*http.Client, error), ownedClientFor func(context.Context, []Connection) (*http.Client, bool, error), result *LogoutResult) {
	// Revocation is deliberately after local cleanup and outside the target
	// transaction. One operation-wide budget covers client construction (including
	// DNS), discovery, and every token; a stalled issuer must not block enrollment.
	cleanupCtx, cancelCleanup := context.WithTimeout(ctx, revocationTimeout)
	defer cancelCleanup()

	var conns []Connection
	for _, item := range revoke {
		if revocableTokenCount(item.token) > 0 {
			conns = append(conns, item.conn)
		}
	}
	if len(conns) == 0 || (clientFor == nil && ownedClientFor == nil) {
		return
	}

	var (
		client *http.Client
		owned  bool
		err    error
	)
	if ownedClientFor != nil {
		client, owned, err = ownedClientFor(cleanupCtx, conns)
	} else {
		client, err = clientFor(cleanupCtx, conns)
	}
	if err != nil {
		// A partially-constructed owned client (client non-nil alongside a
		// non-nil error) still needs its idle-connection cleanup -- the builder
		// failed some step after opening connections, not before.
		if owned && client != nil {
			client.CloseIdleConnections()
		}
		// The one operation-wide client failed to build; every retained
		// credential's revocation is unattempted.
		for _, item := range revoke {
			if remaining := revocableTokenCount(item.token); remaining > 0 {
				result.RevocationsFailed += remaining
				recordRevocationError(result, item.conn.Identity, err)
			}
		}
		return
	}
	if owned && client != nil {
		defer client.CloseIdleConnections()
	}

	for _, item := range revoke {
		remaining := revocableTokenCount(item.token)
		if remaining == 0 {
			continue
		}
		if err := cleanupCtx.Err(); err != nil {
			result.RevocationsFailed += remaining
			recordRevocationError(result, item.conn.Identity, err)
			continue
		}
		attempted, failed, err := revokeTokens(cleanupCtx, client, item.conn.Identity, item.token)
		result.RevocationsAttempted += attempted
		result.RevocationsFailed += failed
		if err != nil {
			recordRevocationError(result, item.conn.Identity, err)
		}
	}
}

// recordRevocationError keeps only the first cause, which is almost always
// the one that actually explains the failure (a later error in the same
// exhausted-budget run is usually just "context deadline exceeded" again).
// The recorded string is sanitized: err can originate from an http.Client
// call against a provider-controlled endpoint (discovery or
// revocation_endpoint), and ADR 0277 excludes provider-controlled discovery
// endpoint values from errors, diagnostics, and UI state.
func recordRevocationError(result *LogoutResult, id Identity, err error) {
	if result.RevocationError == "" {
		result.RevocationError = fmt.Sprintf("issuer %s: %s", id.Issuer, sanitizeRevocationError(err))
	}
}

// revocationStatusError reports a non-2xx response from a provider's
// revocation or discovery endpoint. It carries only the token kind (or
// "discovery") and the status code, both harness-authored -- never the
// endpoint URL or any provider response body.
type revocationStatusError struct {
	stage string
	code  int
}

func (e *revocationStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.stage, e.code)
}

// sanitizeRevocationError classifies a revocation-path error into a closed,
// harness-authored category. A raw http.Client error is typically a
// *url.Error whose Error() embeds the request URL verbatim (Op "URL": Err);
// that URL is provider-controlled (from OIDC discovery), so it must never be
// echoed. Only recognized, URL-free shapes render their own detail.
func sanitizeRevocationError(err error) string {
	var statusErr *revocationStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "timed out"
		}
		return "network unreachable"
	}
	return "revocation failed"
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

func revokeTokens(ctx context.Context, client *http.Client, id Identity, token Token) (attempted, failed int, lastErr error) {
	remaining := revocableTokenCount(token)
	if client == nil {
		return 0, remaining, errors.New("no HTTP client")
	}
	if err := ctx.Err(); err != nil {
		return 0, remaining, err
	}
	endpoint, err := discoverRevocationEndpoint(ctx, client, id)
	if err != nil {
		return 0, remaining, err
	}
	if endpoint == "" {
		return 0, remaining, errors.New("revocation discovery: no revocation_endpoint published")
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
			lastErr = doErr
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			failed++
			lastErr = &revocationStatusError{stage: item.hint + " revocation", code: res.StatusCode}
		}
	}
	return attempted, failed, lastErr
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
