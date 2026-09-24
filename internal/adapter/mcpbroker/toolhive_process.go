package mcpbroker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	toolHiveBasePath = "/v1/mcp/broker"
	toolHiveMCPPath  = toolHiveBasePath + "/mcp"
)

type ownedResource struct {
	name  string
	close func() error
}

type recoveredAttempt struct {
	assertion  contract.CustodyAssertion
	binding    session.ExternalBinding
	done       chan struct{}
	doneClosed bool
	success    bool
}

// Process is the single owner of a valid broker Runtime and all bundled
// ToolHive resources. ToolHive values never cross the neutral broker boundary.
type Process struct {
	Runtime      *Runtime
	Handlers     HandlerBundle
	CallbackPath string

	ctx               context.Context
	cancel            context.CancelFunc
	lifecycleMu       sync.Mutex
	continuityMu      sync.Mutex
	recoveredAttempts map[string]*recoveredAttempt
	closed            bool
	construction      toolHiveConstruction
	discovery         *authenticatedDiscovery
	protectedTarget   *oauthRoute
	protectedStorage  *protectedToolHiveStorage
	custody           *credentialCustody
	authStorage       storage.Storage
	authKeyProvider   keys.KeyProvider
	issuer            string
	profileDigest     [32]byte
	providers         []string
	// reservedToolNames is the immutable model-visible name set outside this Process's
	// broker catalogue (core/global tools), captured once at construction so a
	// later workspace-enrollment freeze can reuse it without re-deriving it.
	reservedToolNames  []string
	queryAuthenticated func(context.Context, oauth2.TokenSource, string) (AuthenticatedCapabilities, error)
	resources          []ownedResource
	closeOnce          sync.Once
	closeErr           error
	// diag receives per-backend authenticated-discovery outcomes during
	// workspace-enrollment catalogue freeze. Always non-nil (defaults to
	// port.NopDiagnostics{} in newToolHiveProcess).
	diag port.Diagnostics
}

type toolHiveProcessOptions struct {
	runtimeOptions                []Option
	brokerHTTPClient              *http.Client
	allowLoopbackUpstreamsForTest bool
}

// Process exposes the neutral runtime plus the process-owned continuity custody.
// Keeping this adapter as the production service prevents the custody capability
// from being lost when the ToolHive process is composed into the RPC server.
var _ contract.Service = (*Process)(nil)
var _ contract.BindingSessionDeleter = (*Process)(nil)
var _ contract.ExpectedBindingAttacher = (*Process)(nil)
var _ contract.CredentialContinuityService = (*Process)(nil)

func (p *Process) AttachSession(ctx context.Context, id session.SessionID) (contract.SessionHandle, contract.AttachOutcome, error) {
	if p == nil || p.Runtime == nil {
		return nil, "", contract.ErrStateUnavailable
	}
	return p.Runtime.AttachSession(ctx, id)
}

func (p *Process) DeleteSession(ctx context.Context, id session.SessionID) (contract.DeleteOutcome, error) {
	if p == nil || p.Runtime == nil {
		return "", contract.ErrStateUnavailable
	}
	return p.Runtime.DeleteSession(ctx, id)
}

func (p *Process) DeleteSessionIfBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.DeleteOutcome, error) {
	if p == nil || p.Runtime == nil {
		return "", contract.ErrStateUnavailable
	}
	return p.Runtime.DeleteSessionIfBinding(ctx, id, binding)
}

func (p *Process) AttachSessionExpectedBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.SessionHandle, contract.AttachOutcome, error) {
	if p == nil || p.Runtime == nil {
		return nil, "", contract.ErrStateUnavailable
	}
	return p.Runtime.AttachSessionExpectedBinding(ctx, id, binding)
}

func (p *Process) continuityProfile() ([32]byte, []string, error) {
	if p == nil {
		return [32]byte{}, nil, contract.ErrContinuityUnavailable
	}
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	if p.closed || len(p.providers) == 0 {
		return [32]byte{}, nil, contract.ErrContinuityUnavailable
	}
	return p.profileDigest, append([]string(nil), p.providers...), nil
}

func (p *Process) matchesCurrentContinuityProfile(guard contract.ContinuityGuard) bool {
	digest, providers, err := p.continuityProfile()
	if err != nil || len(providers) != len(guard.Providers) || subtle.ConstantTimeCompare(digest[:], guard.ProfileDigest[:]) != 1 {
		return false
	}
	matched := 1
	for i := range providers {
		matched &= subtle.ConstantTimeCompare([]byte(providers[i]), []byte(guard.Providers[i]))
	}
	return matched == 1
}

func (p *Process) CommitCredentialCustody(ctx context.Context, assertion contract.CustodyAssertion) error {
	if !p.matchesCurrentContinuityProfile(assertion.Guard) {
		return contract.ErrContinuityUnavailable
	}
	custody, err := p.continuityCustody()
	if err != nil {
		return err
	}
	if err := custody.Commit(ctx, custodyAssertionFromContract(assertion)); err != nil {
		return continuityCustodyError(ctx, err)
	}
	return nil
}

func (p *Process) TombstoneCredentialCustody(ctx context.Context, assertion contract.CustodyAssertion) error {
	custody, err := p.continuityCustody()
	if err != nil {
		return err
	}
	request := custodyRequest{Guard: custodyGuardFromContract(assertion.Guard), AttemptDeadline: assertion.AttemptDeadline}
	if err := custody.Tombstone(ctx, request, recoveryID(assertion.RecoveryReference)); err != nil {
		return continuityCustodyError(ctx, err)
	}
	return nil
}

func newRecoveredCatalogueRef(deadline time.Time, services uint32) (contract.WorkspaceEnrollmentRef, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return contract.WorkspaceEnrollmentRef{}, err
	}
	return contract.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID("recovered-" + base64.RawURLEncoding.EncodeToString(raw[:])), RequiredServices: services, ExpiresAt: deadline}, nil
}

func (p *Process) recoveryAttemptKey(assertion contract.CustodyAssertion, requestID string) string {
	return base64.RawURLEncoding.EncodeToString(assertion.Guard.WorkloadPartition[:]) + ":" + requestID
}

func sameRecoveryAssertion(a, b contract.CustodyAssertion) bool {
	return a.RecoveryReference == b.RecoveryReference && a.AttemptDeadline.Equal(b.AttemptDeadline) && a.Guard.SessionID == b.Guard.SessionID && a.Guard.SessionIncarnation == b.Guard.SessionIncarnation && a.Guard.OwnerPartition == b.Guard.OwnerPartition && a.Guard.WorkloadPartition == b.Guard.WorkloadPartition && a.Guard.ProfileDigest == b.Guard.ProfileDigest && slices.Equal(a.Guard.Providers, b.Guard.Providers)
}

func (p *Process) reserveRecoveredAttempt(assertion contract.CustodyAssertion, requestID string) (*recoveredAttempt, bool, error) {
	if requestID == "" || len(requestID) > 256 || !utf8.ValidString(requestID) {
		return nil, false, contract.ErrContinuityUnavailable
	}
	key := p.recoveryAttemptKey(assertion, requestID)
	p.continuityMu.Lock()
	defer p.continuityMu.Unlock()
	if p.recoveredAttempts == nil {
		p.recoveredAttempts = make(map[string]*recoveredAttempt)
	}
	for key, attempt := range p.recoveredAttempts {
		if !attempt.assertion.AttemptDeadline.After(time.Now()) {
			delete(p.recoveredAttempts, key)
			if !attempt.doneClosed {
				attempt.doneClosed = true
				close(attempt.done)
			}
		}
	}
	if attempt := p.recoveredAttempts[key]; attempt != nil {
		if !sameRecoveryAssertion(attempt.assertion, assertion) {
			return nil, false, contract.ErrContinuityUnavailable
		}
		return attempt, false, nil
	}
	if len(p.recoveredAttempts) >= 128 {
		return nil, false, contract.ErrContinuityUnavailable
	}
	attempt := &recoveredAttempt{assertion: assertion, done: make(chan struct{})}
	p.recoveredAttempts[key] = attempt
	time.AfterFunc(time.Until(assertion.AttemptDeadline), func() {
		p.continuityMu.Lock()
		defer p.continuityMu.Unlock()
		if p.recoveredAttempts[key] == attempt {
			delete(p.recoveredAttempts, key)
			if !attempt.doneClosed {
				attempt.doneClosed = true
				close(attempt.done)
			}
		}
	})
	return attempt, true, nil
}

func (p *Process) finishRecoveredAttempt(assertion contract.CustodyAssertion, requestID string, attempt *recoveredAttempt, binding session.ExternalBinding, success bool) {
	p.continuityMu.Lock()
	defer p.continuityMu.Unlock()
	if p.recoveredAttempts[p.recoveryAttemptKey(assertion, requestID)] != attempt {
		return
	}
	attempt.binding, attempt.success = binding, success
	if !success {
		delete(p.recoveredAttempts, p.recoveryAttemptKey(assertion, requestID))
	}
	if !attempt.doneClosed {
		attempt.doneClosed = true
		close(attempt.done)
	}
}

func (p *Process) replayRecoveredAttempt(ctx context.Context, attempt *recoveredAttempt) (contract.RecoveredCredentialAttachment, error) {
	select {
	case <-attempt.done:
	case <-ctx.Done():
		return contract.RecoveredCredentialAttachment{}, ctx.Err()
	}
	if !attempt.success || attempt.binding == "" {
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}
	handle, _, err := p.Runtime.AttachSessionExpectedBinding(ctx, attempt.assertion.Guard.SessionID, attempt.binding)
	if err != nil {
		return contract.RecoveredCredentialAttachment{}, err
	}
	return contract.RecoveredCredentialAttachment{Attachment: handle}, nil
}

func (p *Process) RecoverCredentialAttachment(ctx context.Context, assertion contract.CustodyAssertion, requestID string) (contract.RecoveredCredentialAttachment, error) {
	if !p.matchesCurrentContinuityProfile(assertion.Guard) {
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}
	attempt, creator, err := p.reserveRecoveredAttempt(assertion, requestID)
	if err != nil {
		return contract.RecoveredCredentialAttachment{}, err
	}
	if !creator {
		return p.replayRecoveredAttempt(ctx, attempt)
	}
	completed := false
	defer func() {
		if !completed {
			p.finishRecoveredAttempt(assertion, requestID, attempt, "", false)
		}
	}()
	custody, err := p.continuityCustody()
	if err != nil {
		return contract.RecoveredCredentialAttachment{}, err
	}
	custodyAssertion := custodyAssertionFromContract(assertion)
	record, err := custody.Load(ctx, custodyAssertion)
	if err != nil {
		return contract.RecoveredCredentialAttachment{}, continuityCustodyError(ctx, err)
	}
	for _, provider := range assertion.Guard.Providers {
		if _, err := custody.Resolve(ctx, custodyAssertion, provider); err != nil {
			return contract.RecoveredCredentialAttachment{}, continuityCustodyError(ctx, err)
		}
	}
	p.lifecycleMu.Lock()
	storage, keyProvider, issuer := p.authStorage, p.authKeyProvider, p.issuer
	clientID := ""
	if p.protectedTarget != nil {
		clientID = p.protectedTarget.clientID
	}
	open := !p.closed && p.Runtime != nil && p.discovery != nil
	p.lifecycleMu.Unlock()
	if !open || storage == nil || keyProvider == nil || clientID == "" {
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}

	var logical *logicalSession
	source := &recoveredCredentialSource{}
	source.active = func() bool {
		if logical == nil {
			return false
		}
		p.lifecycleMu.Lock()
		processOpen := !p.closed
		p.lifecycleMu.Unlock()
		if !processOpen {
			return false
		}
		logical.mu.RLock()
		valid := !logical.deleted && logical.recoveredSource == source
		logical.mu.RUnlock()
		return valid
	}
	guard := custodyGuardFromContract(assertion.Guard)
	source.validate = func(checkCtx context.Context) error {
		if !source.active() {
			return errCustodyUnavailable
		}
		_, err := custody.LoadCurrent(checkCtx, custodyAssertion.Recovery, guard)
		return err
	}
	source.issue = func(issueCtx context.Context) (*oauth2.Token, error) {
		if !source.active() {
			return nil, errCustodyUnavailable
		}
		logical.mu.RLock()
		provisional := logical.provisional
		logical.mu.RUnlock()
		var current custodyRecord
		var err error
		if provisional {
			current, err = custody.Load(issueCtx, custodyAssertion)
		} else {
			current, err = custody.LoadCurrent(issueCtx, custodyAssertion.Recovery, guard)
		}
		if err != nil || current.TSID != record.TSID {
			return nil, errCustodyUnavailable
		}
		return issueRecoveredBrokerCredential(issueCtx, storage, keyProvider, issuer, clientID, assertion.Guard.OwnerPartition, current.TSID, current.ExpiresAt)
	}
	handle, err := p.Runtime.newRecoveredProvisional(assertion.Guard.SessionID, assertion.AttemptDeadline, source)
	if err != nil {
		return contract.RecoveredCredentialAttachment{}, continuityCustodyError(ctx, err)
	}
	logical = handle.logical
	if _, err := source.Token(); err != nil {
		_ = handle.Abort(context.Background())
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}
	ref, err := newRecoveredCatalogueRef(assertion.AttemptDeadline, uint32(len(assertion.Guard.Providers)))
	if err != nil {
		_ = handle.Abort(context.Background())
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}
	if _, err := handle.FreezeAuthenticatedCatalogue(ctx, ref, p, source, p.reservedToolNames); err != nil {
		_ = handle.Abort(context.Background())
		return contract.RecoveredCredentialAttachment{}, continuityCustodyError(ctx, err)
	}
	if _, err := custody.Load(ctx, custodyAssertion); err != nil || source.validateCurrent(ctx) != nil {
		_ = handle.Abort(context.Background())
		return contract.RecoveredCredentialAttachment{}, contract.ErrContinuityUnavailable
	}
	attachment := contract.RecoveredCredentialAttachment{Attachment: handle}
	p.finishRecoveredAttempt(assertion, requestID, attempt, handle.Binding(), true)
	completed = true
	return attachment, nil
}

func (p *Process) continuityCustody() (*credentialCustody, error) {
	if p == nil {
		return nil, contract.ErrContinuityUnavailable
	}
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	if p.closed || p.custody == nil {
		return nil, contract.ErrContinuityUnavailable
	}
	return p.custody, nil
}

func custodyAssertionFromContract(assertion contract.CustodyAssertion) custodyAssertion {
	return custodyAssertion{custodyRequest: custodyRequest{Guard: custodyGuardFromContract(assertion.Guard), AttemptDeadline: assertion.AttemptDeadline}, Recovery: recoveryID(assertion.RecoveryReference)}
}

func custodyGuardFromContract(guard contract.ContinuityGuard) custodyGuard {
	return custodyGuard{SessionID: guard.SessionID, Incarnation: guard.SessionIncarnation, OwnerPartition: guard.OwnerPartition, WorkloadPartition: guard.WorkloadPartition, ProfileDigest: guard.ProfileDigest, Providers: append([]string(nil), guard.Providers...)}
}

func continuityCustodyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return contract.ErrContinuityUnavailable
}

func continuityProfileFromConfig(config ToolHiveConfig, issuer string, construction toolHiveConstruction) ([32]byte, []string, error) {
	profiles := make([]contract.ProtectedProfile, 0, len(construction.protectedBackends))
	for _, profile := range config.Profiles {
		if profile.Auth != authOAuth || profile.OAuth == nil {
			continue
		}
		provider, ok := construction.providerByBackend[profile.Name]
		if !ok {
			return [32]byte{}, nil, contract.ErrContinuityUnavailable
		}
		profiles = append(profiles, contract.ProtectedProfile{
			Provider: provider, Destination: profile.URL, Issuer: profile.OAuth.Issuer,
			AuthorizationEndpoint: profile.OAuth.AuthorizationEndpoint, TokenEndpoint: profile.OAuth.TokenEndpoint,
			DCRDiscoveryURL: profile.OAuth.DCRDiscoveryURL, ClientID: profile.OAuth.ClientID, AuthMode: profile.Auth,
			Scopes: append([]string(nil), profile.OAuth.Scopes...), RequestRefreshToken: profile.OAuth.RequestRefreshToken,
		})
	}
	if len(profiles) == 0 {
		return [32]byte{}, nil, nil
	}
	return contract.ProtectedProfileDigest(config.CallbackURL, issuer, profiles)
}

func continuityProfileForProcess(config ToolHiveConfig, issuer string, construction toolHiveConstruction) ([32]byte, []string, error) {
	digest, providers, err := continuityProfileFromConfig(config, issuer, construction)
	if err != nil && config.ProtectedStorage != nil {
		return [32]byte{}, nil, err
	}
	if err != nil {
		return [32]byte{}, nil, nil
	}
	return digest, providers, nil
}

// NewToolHiveProcess discovers anonymous upstreams, constructs one ordered
// ToolHive process, and returns only after the Runtime and every owned resource
// are valid. Any partial construction is rolled back in reverse dependency order.
func NewToolHiveProcess(ctx context.Context, config ToolHiveConfig, options ...Option) (*Process, error) {
	return newToolHiveProcess(ctx, config, toolHiveProcessOptions{runtimeOptions: append([]Option(nil), options...)})
}

//nolint:gocyclo // Broker construction is one ordered admission transaction with reverse-order rollback.
func newToolHiveProcess(ctx context.Context, config ToolHiveConfig, options toolHiveProcessOptions) (*Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	diag := config.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	diag = diag.With("component", "mcpbroker")
	issuer, err := toolHiveIssuer(config.CallbackURL, hasProtected(config.Profiles))
	if err != nil {
		return nil, err
	}
	construction, err := compileToolHiveConstruction(config.Profiles, issuer)
	if err != nil {
		return nil, err
	}
	if options.allowLoopbackUpstreamsForTest {
		for i := range construction.upstreams {
			if oauth := construction.upstreams[i].OAuth2Config; oauth != nil {
				oauth.AllowPrivateIPs = true
				oauth.InsecureAllowHTTP = true
			}
		}
	}
	routes, err := discoverAnonymous(ctx, construction.anonymous, config.ReservedToolNames)
	if err != nil {
		return nil, err
	}
	protectedTarget, err := newToolHiveProtectedTarget(issuer, config.CallbackURL, len(construction.upstreams) != 0)
	if err != nil {
		return nil, err
	}
	staticRoutes, err := compileStaticProtectedRoutes(construction, protectedTarget, routes, config.ReservedToolNames)
	if err != nil {
		return nil, err
	}
	// Static protected declarations are visible before enrollment so a first call
	// can initiate the ToolHive-owned bundle authorization. catalogueInputs
	// filters these oauth routes when it builds the enrolled replacement.
	routes = append(routes, staticRoutes...)
	sortRoutes(routes)
	catalogue := &Catalogue{routes: routes}
	caller := anonymousCaller(construction.anonymous)
	runtimeOptions := append([]Option(nil), options.runtimeOptions...)
	runtimeOptions = append(runtimeOptions,
		withDiagnostics(diag),
		WithAuthorizedCaller(toolHiveProtectedCaller(issuer+"/mcp", options.brokerHTTPClient, diag, nil)),
		WithQueryCaller(toolHiveQueryCaller(construction.anonymous, issuer+"/mcp", options.brokerHTTPClient)),
	)
	if protectedTarget != nil {
		// Every configured protected upstream may lack a static tool
		// declaration (workspace enrollment only), in which case the compiled
		// catalogue has no oauth route at all: force the hardened token client
		// into existence for the Process-owned target regardless.
		runtimeOptions = append(runtimeOptions, withHardenedTokenEndpoint(protectedTarget.tokenEndpoint))
	}
	runtime, err := New(catalogue, caller, runtimeOptions...)
	if err != nil {
		return nil, err
	}
	client := options.brokerHTTPClient
	if client == nil && runtime.oauth.allowLoopback {
		// The loopback-only OAuth option may also supply the trusted client for
		// the in-process TLS vMCP endpoint. Production callers cannot enable this
		// path because WithOAuthLoopbackForTest requires a test helper.
		client = runtime.oauth.testBrokerHTTPClient
		if client == nil {
			client = runtime.oauth.httpClient
		}
	}
	runtime.authorizedCaller = toolHiveProtectedCaller(issuer+"/mcp", client, diag, runtime)
	runtime.queryCaller = toolHiveQueryCaller(construction.anonymous, issuer+"/mcp", client)

	processCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	profileDigest, providers, profileErr := continuityProfileForProcess(config, issuer, construction)
	if profileErr != nil {
		cancel()
		_ = runtime.Close()
		return nil, errors.New("mcpbroker: protected continuity profile unavailable")
	}
	process := &Process{Runtime: runtime, ctx: processCtx, cancel: cancel, construction: construction, protectedTarget: protectedTarget, reservedToolNames: append([]string(nil), config.ReservedToolNames...), profileDigest: profileDigest, providers: providers, issuer: issuer, diag: diag}
	runtime.process = process
	process.resources = append(process.resources, ownedResource{name: "process-context", close: func() error { cancel(); return nil }})
	rollback := func(cause error) (*Process, error) {
		process.rollback()
		return nil, cause
	}

	outgoing := vmcpauth.NewDefaultOutgoingAuthRegistry()
	if err := outgoing.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()); err != nil {
		return rollback(fmt.Errorf("mcpbroker: register anonymous strategy: %w", err))
	}
	if len(construction.upstreams) != 0 {
		if err := outgoing.RegisterStrategy("upstream_inject", strategies.NewUpstreamInjectStrategy()); err != nil {
			return rollback(fmt.Errorf("mcpbroker: register protected strategy: %w", err))
		}
	}

	var auth authserver.Server
	var authKeyProvider keys.KeyProvider
	var incoming func(http.Handler) http.Handler
	var authInfo http.Handler
	var protectedStorage *protectedToolHiveStorage
	if config.ProtectedStorage != nil {
		if config.AuthStorage != nil || config.AuthRedisClient != nil || len(construction.upstreams) == 0 {
			return rollback(errors.New("mcpbroker: protected storage configuration conflicts with storage seams"))
		}
		protectedStorage, err = newProtectedToolHiveStorage(processCtx, *config.ProtectedStorage)
		if err != nil {
			return rollback(errors.New("mcpbroker: protected storage unavailable"))
		}
		process.protectedStorage = protectedStorage
		process.resources = append(process.resources, ownedResource{name: "protected-storage", close: protectedStorage.Close})
	}
	if len(construction.upstreams) != 0 {
		// A restart between a user starting an OAuth authorization and
		// completing it in their browser must not lose the pending-state
		// record. Composition supplies a Redis-backed store whenever the
		// operator already configured Redis for the session store
		// (ToolHiveConfig.AuthStorage); otherwise this falls back to the
		// in-memory default, which does not survive a process restart.
		var authStore storage.Storage
		if protectedStorage != nil {
			authStore = protectedStorage.storage
		} else {
			authStore = config.AuthStorage
			if authStore == nil && config.AuthRedisClient != nil {
				authStore = storage.NewRedisStorageWithClient(config.AuthRedisClient, toolHiveAuthStoragePrefix)
			}
			if authStore == nil {
				authStore = storage.NewMemoryStorage()
			}
		}
		if protectedTarget == nil {
			return rollback(fmt.Errorf("%w: protected ToolHive target is required", ErrInvalidCatalogue))
		}
		client, err := registration.New(registration.Config{
			ID:                      protectedTarget.clientID,
			Secret:                  protectedTarget.clientSecret,
			RedirectURIs:            []string{config.CallbackURL},
			TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
			GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
			ResponseTypes:           []string{oauthproto.ResponseTypeCode},
			Scopes:                  []string{"openid", "offline_access"},
			Audience:                []string{issuer},
		})
		if err != nil {
			return rollback(fmt.Errorf("mcpbroker: construct embedded authorization client: %w", err))
		}
		if err := authStore.RegisterClient(processCtx, client); err != nil {
			return rollback(fmt.Errorf("mcpbroker: register embedded authorization client: %w", err))
		}
		// TODO: Replace this narrow constructor with
		// runner.NewEmbeddedAuthServerWithStorage once ToolHive releases support
		// for propagating the configured token-endpoint authentication method.
		auth, authKeyProvider, err = newToolHiveAuthServer(processCtx, issuer, construction.upstreams, authStore)
		if err != nil {
			return rollback(fmt.Errorf("mcpbroker: create embedded auth server: %w", err))
		}
		process.authStorage = authStore
		process.authKeyProvider = authKeyProvider
		process.resources = append(process.resources, ownedResource{name: "authserver", close: auth.Close})
		reader := upstreamtoken.NewInProcessService(auth.IDPTokenStorage(), auth.UpstreamTokenRefresher())
		if protectedStorage != nil {
			process.custody, err = newCredentialCustody(protectedStorage.client, protectedStorage.keys, reader, wallclock.Clock{}, protectedStorage.storage)
			if err != nil {
				return rollback(errors.New("mcpbroker: protected custody unavailable"))
			}
		}
		verifiedReader := upstreamtoken.TokenReader(reader)
		if protectedStorage != nil {
			verifiedReader = &capturingTokenReader{next: reader}
		}
		incoming, _, authInfo, err = factory.NewIncomingAuthMiddleware(processCtx, &vmcpconfig.IncomingAuthConfig{
			Type: "oidc", OIDC: &vmcpconfig.OIDCConfig{Issuer: issuer, Audience: issuer, Resource: issuer, JWKSURL: issuer + "/.well-known/jwks.json"},
		}, "mecatl-broker", nil, verifiedReader, authKeyProvider, issuer)
		if err != nil {
			return rollback(fmt.Errorf("mcpbroker: create incoming auth: %w", err))
		}
	}

	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoing)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create backend client: %w", err))
	}
	aggregationConfig := toolHiveAggregationConfig()
	resolver, err := aggregator.NewConflictResolver(aggregationConfig)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create conflict resolver: %w", err))
	}
	capabilityAggregator := aggregator.NewDefaultAggregator(backendClient, resolver, aggregationConfig, nil)
	serverConfig := &vmcpserver.Config{Name: "mecatl-broker", Version: "v1", EndpointPath: toolHiveMCPPath,
		AuthMiddleware: incoming, AuthInfoHandler: authInfo,
		Aggregator: capabilityAggregator, SessionFactory: vmcpsession.NewSessionFactory(outgoing),
	}
	backendRegistry := vmcp.NewImmutableRegistry(construction.backends)
	server, err := vmcpserver.New(processCtx, serverConfig, router.NewSessionRouter(&vmcp.RoutingTable{}), backendClient, backendRegistry, nil)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create vMCP server: %w", err))
	}
	if len(construction.protectedBackends) != 0 {
		process.discovery = &authenticatedDiscovery{
			capabilities: capabilityAggregator,
			backends:     backendRegistry,
			incoming:     incoming,
			captureTSID:  protectedStorage != nil,
		}
	}
	process.resources = append(process.resources, ownedResource{name: "vmcp", close: func() error { return server.Stop(context.Background()) }})
	vmcpHandler, err := server.Handler(processCtx)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create vMCP handler: %w", err))
	}
	if incoming != nil {
		// The vMCP endpoint is published only when the OIDC middleware exists to
		// protect it. With zero protected upstreams there is no incoming-auth
		// mechanism at all, so publishing it would be an anonymous
		// tool-execution surface (CWE-306 / OWASP API2:2023).
		process.Handlers.VMCP = vmcpHandler
	}
	if auth != nil {
		embedded := http.StripPrefix(toolHiveBasePath, bodylimit.Middleware(handlers.MaxDCRBodySize)(auth.Handler()))
		process.Handlers.Authorization = embedded
		process.Handlers.Token = embedded
		process.Handlers.UpstreamCallback = embedded
		process.Handlers.Discovery = embedded
		process.Handlers.JWKS = embedded
		process.Handlers.ProtectedResource = authInfo
	}
	if config.CallbackURL != "" {
		callbackHandlers, callbackPath, handlerErr := runtime.Handlers(config.CallbackURL)
		if handlerErr != nil {
			return rollback(handlerErr)
		}
		process.Handlers.Callback = callbackHandlers.Callback
		process.CallbackPath = callbackPath
	}
	return process, nil
}

func newToolHiveAuthServer(
	ctx context.Context,
	issuer string,
	runConfigs []authserver.UpstreamRunConfig,
	stor storage.Storage,
) (authserver.Server, keys.KeyProvider, error) {
	embedded, err := runner.NewEmbeddedAuthServerWithStorage(ctx, &authserver.RunConfig{
		Issuer:           issuer,
		Upstreams:        runConfigs,
		AllowedAudiences: []string{issuer},
	}, stor)
	if err != nil {
		return nil, nil, err
	}
	return embedded, embedded.KeyProvider(), nil
}

func discoverAnonymous(ctx context.Context, profiles []ToolHiveProfile, reservedToolNames []string) ([]route, error) {
	configs := make([]mcpadapter.ServerConfig, len(profiles))
	for i, profile := range profiles {
		configs[i] = mcpadapter.ServerConfig{Name: profile.Name, URL: profile.URL}
	}
	definitions := make([]ToolDefinition, 0)
	if len(configs) != 0 {
		manager, err := mcpadapter.NewManager(ctx, configs, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("mcpbroker: discover anonymous upstreams: %w", err)
		}
		defer func() { _ = manager.Close() }()
		for _, wrapped := range manager.Tools() {
			spec := wrapped.Spec()
			for _, profile := range profiles {
				if strings.HasPrefix(spec.Name, "mcp__"+profile.Name+"__") {
					definitions = append(definitions, ToolDefinition{Backend: profile.Name, Name: spec.Name, Description: spec.Description, Schema: spec.Schema, ReadOnly: wrapped.ReadOnly()})
					break
				}
			}
		}
	}
	declarations := make([]ToolHiveProfile, len(profiles))
	copy(declarations, profiles)
	backends := make(map[string]ToolHiveProfile, len(declarations))
	for _, profile := range declarations {
		backends[strings.ToLower(profile.Name)] = profile
	}
	seen := make(map[string]struct{}, len(reservedToolNames)+len(definitions))
	for _, name := range reservedToolNames {
		if name == "" {
			return nil, fmt.Errorf("%w: reserved tool name is empty", ErrInvalidCatalogue)
		}
		seen[name] = struct{}{}
	}
	routes := make([]route, 0, len(definitions))
	for _, definition := range definitions {
		profile, ok := backends[strings.ToLower(definition.Backend)]
		if !ok {
			return nil, fmt.Errorf("%w: discovery references missing upstream %q", ErrInvalidCatalogue, definition.Backend)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: model-visible tool name collision %q", ErrInvalidCatalogue, definition.Name)
		}
		seen[definition.Name] = struct{}{}
		routes = append(routes, route{backend: profile.Name, spec: tool.ToolSpec{Name: definition.Name, Description: definition.Description, Schema: append([]byte(nil), definition.Schema...)}, readOnly: definition.ReadOnly})
	}
	sortRoutes(routes)
	return routes, nil
}

func compileStaticProtectedRoutes(construction toolHiveConstruction, protectedTarget *oauthRoute, base []route, reservedToolNames []string) ([]route, error) {
	seen := make(map[string]struct{}, len(reservedToolNames)+len(base))
	for _, name := range reservedToolNames {
		if name == "" {
			return nil, fmt.Errorf("%w: reserved tool name is empty", ErrInvalidCatalogue)
		}
		seen[name] = struct{}{}
	}
	for _, route := range base {
		seen[route.spec.Name] = struct{}{}
	}

	routes := make([]route, 0)
	for _, backend := range construction.backends {
		declaredTools := construction.staticByBackend[backend.ID]
		if len(declaredTools) == 0 {
			continue
		}
		if protectedTarget == nil {
			return nil, fmt.Errorf("%w: static protected tools require the ToolHive authorization target", ErrInvalidCatalogue)
		}
		for _, declared := range declaredTools {
			name := "mcp__" + backend.ID + "__" + declared.Name
			candidate, err := validateAuthenticatedRoute(backend.ID, ToolDefinition{
				Backend: backend.ID, Name: name, Description: declared.Description,
				Schema: append([]byte(nil), declared.Schema...), ReadOnly: declared.ReadOnly,
			}, seen)
			if err != nil {
				return nil, fmt.Errorf("%w: static tool declaration %q", err, name)
			}
			candidate.oauth = protectedTarget
			candidate.broker = true
			seen[name] = struct{}{}
			routes = append(routes, candidate)
		}
	}
	return routes, nil
}

func toolHiveAggregationConfig() *vmcpconfig.AggregationConfig {
	return &vmcpconfig.AggregationConfig{
		ConflictResolution: vmcp.ConflictStrategyPrefix,
		ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat: "{workload}.",
		},
	}
}

func toolHiveAdvertisedToolName(backend, modelVisibleName string) (string, error) {
	toolName, ok := strings.CutPrefix(modelVisibleName, "mcp__"+backend+"__")
	if backend == "" || !ok || toolName == "" {
		return "", fmt.Errorf("%w: tool %q does not belong to backend %q", ErrInvalidCatalogue, modelVisibleName, backend)
	}
	return backend + "." + toolName, nil
}

func newToolHiveProtectedTarget(issuer, callbackURL string, required bool) (*oauthRoute, error) {
	if !required {
		return nil, nil
	}
	clientID, err := opaque(rand.Read)
	if err != nil {
		return nil, fmt.Errorf("%w: create ToolHive authorization client: %v", ErrInvalidCatalogue, err)
	}
	clientSecret, err := registration.GenerateClientSecret()
	if err != nil {
		return nil, fmt.Errorf("%w: create ToolHive authorization client secret: %v", ErrInvalidCatalogue, err)
	}
	return &oauthRoute{
		authorizationEndpoint: issuer + "/oauth/authorize",
		tokenEndpoint:         issuer + "/oauth/token",
		callbackURL:           callbackURL,
		clientID:              clientID,
		clientSecret:          clientSecret,
		scopes:                []string{"openid", "offline_access"},
		requestRefresh:        true,
		resource:              issuer,
	}, nil
}

func (r *Runtime) revokeBrokerCredential(ref SessionRef) {
	r.mu.RLock()
	logical := r.sessions[ref.id]
	r.mu.RUnlock()
	if logical == nil {
		return
	}
	logical.mu.Lock()
	defer logical.mu.Unlock()
	if logical.ref == ref && logical.brokerCredential != nil {
		clearGrantToken(logical.brokerCredential)
		logical.brokerCredential = nil
	}
}

func toolHiveProtectedCaller(endpoint string, client *http.Client, diag port.Diagnostics, runtime *Runtime) AuthorizedCaller {
	return func(ctx context.Context, ref SessionRef, backend string, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
		if endpoint == "" || backend == "" {
			return session.ToolResult{}, fmt.Errorf("%w: protected ToolHive target is not configured", ErrInvalidCatalogue)
		}
		if tokens == nil {
			return session.ToolResult{}, fmt.Errorf("%w: protected upstream token source is required", ErrInvalidCatalogue)
		}
		advertisedName, err := toolHiveAdvertisedToolName(backend, call.Name)
		if err != nil {
			return session.ToolResult{}, err
		}
		wrappedName := "mcp__broker__" + advertisedName
		config := mcpadapter.ServerConfig{Name: "broker", URL: endpoint, TokenSource: tokens, HTTPClient: client}
		diag.Log(ctx, port.LevelDebug, "MCP broker: protected connection starting",
			"session", string(ref.SessionID()), "backend", backend)
		started := time.Now()
		upstream, err := mcpadapter.Connect(ctx, config, nil)
		diag.Log(ctx, port.LevelDebug, "MCP broker: protected connection completed",
			"session", string(ref.SessionID()), "backend", backend, "duration", time.Since(started), "success", err == nil)
		if err != nil {
			if strings.HasSuffix(err.Error(), `sending "initialize": Unauthorized`) && runtime != nil {
				runtime.revokeBrokerCredential(ref)
				return session.ToolResult{}, contract.ErrAuthorizationNotFound
			}
			return session.ToolResult{}, fmt.Errorf("mcpbroker: connect protected ToolHive target: %w", err)
		}
		defer func() { _ = upstream.Close() }()
		for _, wrapped := range upstream.Tools() {
			if wrapped.Spec().Name != wrappedName {
				continue
			}
			forwarded := call
			forwarded.Name = wrapped.Spec().Name
			diag.Log(ctx, port.LevelDebug, "MCP broker: protected tool execution starting",
				"session", string(ref.SessionID()), "backend", backend)
			started = time.Now()
			result, executeErr := wrapped.Execute(ctx, forwarded, tool.Environment{})
			diag.Log(ctx, port.LevelDebug, "MCP broker: protected tool execution completed",
				"session", string(ref.SessionID()), "backend", backend, "duration", time.Since(started), "success", executeErr == nil)
			return result, executeErr
		}
		return session.ToolResult{}, fmt.Errorf("mcpbroker: protected ToolHive target omitted tool %q", call.Name)
	}
}

func toolHiveQueryCaller(anonymous []ToolHiveProfile, protectedEndpoint string, client *http.Client) QueryCaller {
	servers := make(map[string]mcpadapter.ServerConfig, len(anonymous))
	for _, profile := range anonymous {
		servers[profile.Name] = mcpadapter.ServerConfig{Name: profile.Name, URL: profile.URL}
	}
	return func(ctx context.Context, _ SessionRef, backend string, call session.ToolCall, tokens oauth2.TokenSource, filter string) (session.ToolResult, error) {
		config, anonymousRoute := servers[backend]
		if !anonymousRoute {
			if protectedEndpoint == "" || tokens == nil {
				return session.ToolResult{}, fmt.Errorf("%w: broker query target is not configured", ErrInvalidCatalogue)
			}
			advertisedName, err := toolHiveAdvertisedToolName(backend, call.Name)
			if err != nil {
				return session.ToolResult{}, err
			}
			config = mcpadapter.ServerConfig{Name: "broker", URL: protectedEndpoint, TokenSource: tokens, HTTPClient: client}
			call.Name = "mcp__broker__" + advertisedName
		}
		upstream, err := mcpadapter.Connect(ctx, config, nil)
		if err != nil {
			return session.ToolResult{}, fmt.Errorf("mcpbroker: connect query target: %w", err)
		}
		defer func() { _ = upstream.Close() }()
		return upstream.QueryToolOnce(ctx, call, filter)
	}
}

func anonymousCaller(profiles []ToolHiveProfile) Caller {
	servers := make(map[string]mcpadapter.ServerConfig, len(profiles))
	for _, profile := range profiles {
		servers[profile.Name] = mcpadapter.ServerConfig{Name: profile.Name, URL: profile.URL}
	}
	return func(ctx context.Context, _ SessionRef, backend string, call session.ToolCall) (session.ToolResult, error) {
		config, ok := servers[backend]
		if !ok {
			return session.ToolResult{}, fmt.Errorf("%w: anonymous upstream is not configured", ErrInvalidCatalogue)
		}
		upstream, err := mcpadapter.Connect(ctx, config, nil)
		if err != nil {
			return session.ToolResult{}, fmt.Errorf("mcpbroker: connect anonymous upstream: %w", err)
		}
		defer func() { _ = upstream.Close() }()
		for _, wrapped := range upstream.Tools() {
			if wrapped.Spec().Name == call.Name {
				return wrapped.Execute(ctx, call, tool.Environment{})
			}
		}
		return session.ToolResult{}, fmt.Errorf("mcpbroker: anonymous upstream omitted tool %q", call.Name)
	}
}

func sortRoutes(routes []route) {
	slices.SortFunc(routes, func(a, b route) int { return strings.Compare(a.spec.Name, b.spec.Name) })
}

func toolHiveIssuer(callbackURL string, protected bool) (string, error) {
	if !protected {
		return "http://mecatl.invalid" + toolHiveBasePath, nil
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.String() != callbackURL {
		return "", fmt.Errorf("%w: protected upstreams require a canonical HTTPS callback URL", ErrInvalidCatalogue)
	}
	return parsed.Scheme + "://" + parsed.Host + toolHiveBasePath, nil
}

func hasProtected(profiles []ToolHiveProfile) bool {
	for _, profile := range profiles {
		if profile.Auth == authOAuth {
			return true
		}
	}
	return false
}

func (p *Process) rollback() {
	// closeAndDrain blocks (bounded by closeDrainTimeout) until every in-flight
	// attachment operation has actually returned, so closeResources below can
	// never tear down the vMCP/authserver resources those operations still
	// depend on. Runtime.Close alone would not wait for them.
	if p.Runtime != nil {
		_ = p.Runtime.closeAndDrain(closeDrainTimeout)
	}
	_ = p.closeResources()
}

func (p *Process) closeResources() error {
	var result error
	for i := len(p.resources) - 1; i >= 0; i-- {
		result = errors.Join(result, p.resources[i].close())
	}
	return result
}

// Ready verifies every process-owned serving prerequisite without attaching a
// session, minting credentials, running discovery, or invoking an upstream tool.
func (p *Process) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.ready(ctx)
}

func (p *Process) ready(ctx context.Context) error {
	if p == nil {
		return errors.New("mcpbroker: ToolHive process is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.lifecycleMu.Lock()
	if p.closed || p.Runtime == nil || p.cancel == nil {
		p.lifecycleMu.Unlock()
		return errors.New("mcpbroker: ToolHive process is unavailable")
	}
	protectedStorage := p.protectedStorage
	construction := p.construction
	handlerBundle := p.Handlers
	discovery := p.discovery
	p.lifecycleMu.Unlock()
	if protectedStorage != nil {
		healthCtx, cancel := context.WithTimeout(ctx, protectedStorage.healthTimeout)
		err := protectedStorage.Health(healthCtx)
		cancel()
		if err != nil {
			return errors.New("mcpbroker: protected storage unavailable")
		}
	}
	if len(construction.upstreams) != 0 {
		if handlerBundle.VMCP == nil || p.protectedTarget == nil || handlerBundle.Authorization == nil || handlerBundle.Token == nil ||
			handlerBundle.UpstreamCallback == nil || handlerBundle.Discovery == nil || handlerBundle.JWKS == nil ||
			handlerBundle.ProtectedResource == nil || handlerBundle.Callback == nil {
			return errors.New("mcpbroker: protected ToolHive route is unavailable")
		}
		if len(construction.protectedBackends) != 0 && discovery == nil {
			return errors.New("mcpbroker: authenticated ToolHive discovery is unavailable")
		}
	}
	return nil
}

// WorkspaceEnrollmentRequired reports whether at least one configured
// protected upstream has no trusted static tool declaration, so its complete
// tool catalogue can only be learned by authenticating first and then running
// live authenticated discovery (workspace enrollment). Composition uses this
// to decide whether to advertise the enrollment capability.
func (p *Process) WorkspaceEnrollmentRequired() bool {
	return p != nil && len(p.construction.protectedBackends) > 0
}

// diagnostics returns p's Diagnostics sink, defaulting to port.NopDiagnostics{}
// for a nil Process or a Process built without going through
// newToolHiveProcess (e.g. a test fixture constructing &Process{} directly) —
// every caller of this accessor stays nil-safe regardless of construction path.
func (p *Process) diagnostics() port.Diagnostics {
	if p == nil || p.diag == nil {
		return port.NopDiagnostics{}
	}
	return p.diag
}

// Close first cancels process-owned work, then drains the neutral Runtime
// (waiting for in-flight attachment operations to actually return, bounded by
// closeDrainTimeout), stops vMCP, and closes authserver. It is idempotent.
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.lifecycleMu.Lock()
		p.closed = true
		cancel := p.cancel
		p.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if p.Runtime != nil {
			p.closeErr = p.Runtime.closeAndDrain(closeDrainTimeout)
		}
		p.closeErr = errors.Join(p.closeErr, p.closeResources())
	})
	return p.closeErr
}
