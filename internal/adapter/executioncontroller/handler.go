// Package executioncontroller implements the authenticated provider HTTP boundary.
package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// ClientPolicy defines capabilities assigned to an authenticated provider client.
type ClientPolicy struct {
	MayAttestOwner bool
	Administrator  bool
}

// GrantSigner configures short-lived environment grant issuance.
type GrantSigner struct {
	KeyID      string
	PrivateKey ed25519.PrivateKey
	Issuer     string
	Audience   string
	Lifetime   time.Duration
}

// HandlerConfig configures authentication and capability grants.
type HandlerConfig struct {
	Clients  map[string]ClientPolicy
	Signer   GrantSigner
	Verifier executionenv.GrantVerifier
	Ready    func() bool
}

// Profile is the externally visible immutable execution profile.
type Profile struct {
	Name               string
	Digest             string
	MaxFileBytes       int64
	MaxCommandBytes    int64
	MaxCommandDuration time.Duration
	Capabilities       []string
}

// Allocation is an exact environment allocation and authorization binding.
type Allocation struct {
	Environment executionenv.EnvironmentRef
	Epoch       uint64
	OwnerHash   string
	BindingID   string
	Client      string
	Ready       bool
}

// Backend implements provider-side authorization state and executor dispatch.
type Backend interface {
	ValidateProfile(context.Context, string) (Profile, error)
	Ensure(context.Context, string, string, string, string, string) (Allocation, error) // client, owner hash, binding, profile, immutable fingerprint
	Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error)
	ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error
	Retire(context.Context, executionenv.EnvironmentRef, string) error
	File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error)
	StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error)
	CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
	CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
}

// Handler is the authenticated private execution-provider HTTP boundary.
type Handler struct {
	cfg     HandlerConfig
	backend Backend
	mux     *http.ServeMux
}

// NewHandler constructs the authenticated private HTTP boundary.
func NewHandler(cfg HandlerConfig, b Backend) *Handler {
	h := &Handler{cfg: cfg, backend: b, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/profiles/validate", h.validate)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/environments/ensure", h.ensure)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/environments/attach", h.attach)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/references/release", h.release)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/environments/retire", h.retire)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/files", h.file)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/commands/start", h.startCommand)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/commands/status", h.commandStatus)
	h.mux.HandleFunc("POST "+executionenv.BasePath+"/commands/cancel", h.cancelCommand)
	return h
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Ready != nil && !h.cfg.Ready() {
		writeError(w, http.StatusServiceUnavailable, executionenv.CodeNotReady, "provider startup fencing is incomplete", true)
		return
	}
	id, policy, err := h.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, executionenv.CodeUnauthenticated, "authenticated client certificate required", false)
		return
	}
	ctx := context.WithValue(r.Context(), clientContextKey{}, authenticatedClient{id, policy})
	h.mux.ServeHTTP(w, r.WithContext(ctx))
}

type clientContextKey struct{}
type authenticatedClient struct {
	id     string
	policy ClientPolicy
}

func (h *Handler) authenticate(r *http.Request) (string, ClientPolicy, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", ClientPolicy{}, errors.New("no peer certificate")
	}
	id, err := canonicalClientIdentity(r.TLS.PeerCertificates[0])
	if err != nil {
		return "", ClientPolicy{}, err
	}
	p, ok := h.cfg.Clients[id]
	if !ok {
		return "", ClientPolicy{}, errors.New("client not allowlisted")
	}
	return id, p, nil
}
func canonicalClientIdentity(c *x509.Certificate) (string, error) {
	ids := make([]string, 0, len(c.URIs))
	for _, u := range c.URIs {
		if u == nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		v := *u
		v.Scheme = strings.ToLower(v.Scheme)
		v.Host = strings.ToLower(v.Host)
		ids = append(ids, v.String())
	}
	if len(ids) == 0 {
		return "", errors.New("certificate has no canonical URI SAN")
	}
	sort.Strings(ids)
	return ids[0], nil
}

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	var q executionenv.ValidateProfileRequest
	if !decode(w, r, &q) {
		return
	}
	if q.Profile == "" || len(q.Profile) > 63 {
		writeError(w, 400, executionenv.CodeInvalidArgument, "invalid profile", false)
		return
	}
	if h.backend == nil {
		writeError(w, 503, executionenv.CodeNotReady, "provider backend unavailable", true)
		return
	}
	p, err := h.backend.ValidateProfile(r.Context(), q.Profile)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, executionenv.ValidateProfileResponse{Profile: p.Name, Digest: p.Digest, Capabilities: p.Capabilities, MaxFileBytes: p.MaxFileBytes, MaxCommandBytes: p.MaxCommandBytes, MaxCommandDurationMillis: p.MaxCommandDuration.Milliseconds()})
}
func (h *Handler) ensure(w http.ResponseWriter, r *http.Request) {
	var q executionenv.EnsureEnvironmentRequest
	if !decode(w, r, &q) {
		return
	}
	c := r.Context().Value(clientContextKey{}).(authenticatedClient)
	if !c.policy.MayAttestOwner || q.Owner.Issuer == "" || q.Owner.Subject == "" || len(q.Owner.Issuer) > executionenv.MaxIdentityBytes || len(q.Owner.Subject) > executionenv.MaxIdentityBytes || q.BindingID == "" || len(q.BindingID) > executionenv.MaxBindingBytes || q.Profile == "" || len(q.Profile) > 63 {
		writeError(w, 403, executionenv.CodePermissionDenied, "owner attestation or required allocation identity missing", false)
		return
	}
	owner := ownerHash(q.Owner)
	fp := fingerprint(c.id, owner, q.BindingID, q.Profile)
	a, err := h.backend.Ensure(r.Context(), c.id, owner, q.BindingID, q.Profile, fp)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	grant, expiresAt, err := h.sign(a, c.id)
	if err != nil {
		writeError(w, 500, executionenv.CodeInternal, "grant issuance failed", false)
		return
	}
	writeJSON(w, 200, executionenv.EnsureEnvironmentResponse{Environment: a.Environment, Epoch: a.Epoch, Ready: a.Ready, Grant: grant, GrantExpiresAt: expiresAt})
}
func (h *Handler) attach(w http.ResponseWriter, r *http.Request) {
	var q executionenv.AttachEnvironmentRequest
	if !decode(w, r, &q) {
		return
	}
	if q.Purpose != executionenv.PurposeSession {
		writeError(w, 400, executionenv.CodeInvalidArgument, "unsupported attachment purpose", false)
		return
	}
	c := r.Context().Value(clientContextKey{}).(authenticatedClient)
	if !c.policy.MayAttestOwner || q.Context.Owner.Issuer == "" || q.Context.Owner.Subject == "" || len(q.Context.Owner.Issuer) > executionenv.MaxIdentityBytes || len(q.Context.Owner.Subject) > executionenv.MaxIdentityBytes || q.Context.BindingID == "" || len(q.Context.BindingID) > executionenv.MaxBindingBytes || q.Context.Environment.ID == "" || q.Context.Environment.Revision == "" || len(q.Context.Environment.ID) > executionenv.MaxIdentityBytes || len(q.Context.Environment.Revision) > executionenv.MaxIdentityBytes {
		writeError(w, 403, executionenv.CodePermissionDenied, "owner attestation or attachment identity missing", false)
		return
	}
	owner := ownerHash(q.Context.Owner)
	a, err := h.backend.Attach(r.Context(), q.Context.Environment, c.id, owner, q.Context.BindingID)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	grant, expiresAt, err := h.sign(a, c.id)
	if err != nil {
		writeError(w, 500, executionenv.CodeInternal, "grant issuance failed", false)
		return
	}
	writeJSON(w, 200, executionenv.AttachEnvironmentResponse{Environment: a.Environment, Epoch: a.Epoch, Ready: a.Ready, Grant: grant, GrantExpiresAt: expiresAt})
}
func (h *Handler) release(w http.ResponseWriter, r *http.Request) {
	var q executionenv.ReferenceReleaseRequest
	if !decode(w, r, &q) {
		return
	}
	c, owner, ok := h.authorize(w, r, q.Context, executionenv.OpReferenceRelease)
	if !ok {
		return
	}
	if err := h.backend.ReleaseReference(r.Context(), q.Context.Environment, c.id, owner, q.Context.BindingID); err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, executionenv.EmptyResponse{})
}
func (h *Handler) retire(w http.ResponseWriter, r *http.Request) {
	var q executionenv.RetireEnvironmentRequest
	if !decode(w, r, &q) {
		return
	}
	c := r.Context().Value(clientContextKey{}).(authenticatedClient)
	if !c.policy.Administrator {
		writeError(w, 403, executionenv.CodePermissionDenied, "administrative identity required", false)
		return
	}
	_, owner, ok := h.authorize(w, r, q.Context, executionenv.OpRetire)
	if !ok {
		return
	}
	if err := h.backend.Retire(r.Context(), q.Context.Environment, owner); err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, executionenv.EmptyResponse{})
}
func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	var q executionenv.FileRequest
	if !decode(w, r, &q) {
		return
	}
	if len(q.Data) > executionenv.MaxFileBytes || !q.Operation.Valid() || len(q.Path) > executionenv.MaxPathBytes || len(q.Destination) > executionenv.MaxPathBytes || len(q.Pattern) > executionenv.MaxPathBytes || q.Limit < 0 || q.Limit > executionenv.MaxListEntries {
		writeError(w, 400, executionenv.CodeInvalidArgument, "invalid or oversized file request", false)
		return
	}
	c, owner, ok := h.authorize(w, r, q.Context, q.Operation)
	if !ok {
		return
	}
	resp, err := h.backend.File(r.Context(), c.id, owner, q)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, resp)
}
func (h *Handler) startCommand(w http.ResponseWriter, r *http.Request) {
	var q executionenv.CommandStartRequest
	if !decode(w, r, &q) {
		return
	}
	if len(q.Command) == 0 || len(q.Command) > executionenv.MaxCommandBytes || q.TimeoutMillis < 0 {
		writeError(w, 400, executionenv.CodeInvalidArgument, "invalid command size", false)
		return
	}
	c, owner, ok := h.authorize(w, r, q.Context, executionenv.OpCommandStart)
	if !ok {
		return
	}
	v, err := h.backend.StartCommand(r.Context(), c.id, owner, q)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *Handler) commandStatus(w http.ResponseWriter, r *http.Request) {
	h.commandQuery(w, r, executionenv.OpCommandStatus)
}
func (h *Handler) cancelCommand(w http.ResponseWriter, r *http.Request) {
	h.commandQuery(w, r, executionenv.OpCommandCancel)
}
func (h *Handler) commandQuery(w http.ResponseWriter, r *http.Request, op executionenv.Operation) {
	var q executionenv.CommandQueryRequest
	if !decode(w, r, &q) {
		return
	}
	c, owner, ok := h.authorize(w, r, q.Context, op)
	if !ok {
		return
	}
	var v executionenv.CommandStatusResponse
	var err error
	if op == executionenv.OpCommandStatus {
		v, err = h.backend.CommandStatus(r.Context(), c.id, owner, q)
	} else {
		v, err = h.backend.CancelCommand(r.Context(), c.id, owner, q)
	}
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, rc executionenv.RequestContext, op executionenv.Operation) (authenticatedClient, string, bool) {
	c := r.Context().Value(clientContextKey{}).(authenticatedClient)
	if rc.Owner.Issuer == "" || rc.Owner.Subject == "" || len(rc.Owner.Issuer) > executionenv.MaxIdentityBytes || len(rc.Owner.Subject) > executionenv.MaxIdentityBytes || rc.BindingID == "" || len(rc.BindingID) > executionenv.MaxBindingBytes || rc.Environment.ID == "" || rc.Environment.Revision == "" || rc.Epoch == 0 || rc.Grant == "" || len(rc.Grant) > executionenv.MaxGrantBytes {
		writeError(w, 403, executionenv.CodePermissionDenied, "request authorization denied", false)
		return c, "", false
	}
	owner := ownerHash(rc.Owner)
	if _, err := h.cfg.Verifier.Verify(rc.Grant, executionenv.GrantExpectation{Client: c.id, OwnerHash: owner, BindingID: rc.BindingID, Environment: rc.Environment, Epoch: rc.Epoch, Operation: op}); err != nil {
		writeError(w, 403, executionenv.CodePermissionDenied, "request authorization denied", false)
		return c, "", false
	}
	return c, owner, true
}
func (h *Handler) sign(a Allocation, client string) (string, time.Time, error) {
	life := h.cfg.Signer.Lifetime
	if life <= 0 {
		life = time.Minute
	}
	now := time.Now().UTC()
	expiresAt := now.Add(life)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	ops := []executionenv.Operation{executionenv.OpAttach, executionenv.OpReferenceRelease, executionenv.OpRetire, executionenv.OpFileRead, executionenv.OpFileResolveAuthority, executionenv.OpFileStat, executionenv.OpFileCreate, executionenv.OpFileReplace, executionenv.OpFileList, executionenv.OpFileRemove, executionenv.OpFileRename, executionenv.OpFileCopy, executionenv.OpFileGlob, executionenv.OpFileGrep, executionenv.OpCommandStart, executionenv.OpCommandStatus, executionenv.OpCommandCancel}
	grant, err := executionenv.SignGrant(h.cfg.Signer.PrivateKey, executionenv.GrantClaims{KeyID: h.cfg.Signer.KeyID, Issuer: h.cfg.Signer.Issuer, Audience: h.cfg.Signer.Audience, Client: client, OwnerHash: a.OwnerHash, BindingID: a.BindingID, Environment: a.Environment, Epoch: a.Epoch, Operations: ops, NotBefore: now, ExpiresAt: expiresAt, Nonce: hex.EncodeToString(nonce)})
	return grant, expiresAt, err
}
func ownerHash(o executionenv.Owner) string {
	s := sha256.Sum256([]byte(o.Issuer + "\x00" + o.Subject))
	return hex.EncodeToString(s[:])
}
func fingerprint(fields ...string) string {
	h := sha256.New()
	for _, f := range fields {
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, f)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, executionenv.MaxJSONBody+1))
	if err != nil {
		writeError(w, 413, executionenv.CodeResourceExhausted, "request body exceeds limit", false)
		return false
	}
	if err := executionenv.DecodeStrict(body, dst); err != nil {
		writeError(w, 400, executionenv.CodeInvalidArgument, "invalid request body", false)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code executionenv.ErrorCode, msg string, retry bool) {
	writeJSON(w, status, executionenv.ErrorResponse{Error: &executionenv.Error{Code: code, Message: msg, Retryable: retry}})
}
func writeBackendError(w http.ResponseWriter, err error) {
	var e *executionenv.Error
	if errors.As(err, &e) {
		status := 400
		switch e.Code {
		case executionenv.CodeNotFound:
			status = 404
		case executionenv.CodeAlreadyExists, executionenv.CodeConflict, executionenv.CodeVersionMismatch:
			status = 409
		case executionenv.CodeNotReady, executionenv.CodeFenceUnknown:
			status = 503
		case executionenv.CodePermissionDenied:
			status = 403
		}
		writeError(w, status, e.Code, e.Message, e.Retryable)
		return
	}
	writeError(w, 500, executionenv.CodeInternal, "provider operation failed", false)
}

// TLSConfig returns the provider's TLS 1.3 mutual-authentication policy.
func TLSConfig(server tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
}
