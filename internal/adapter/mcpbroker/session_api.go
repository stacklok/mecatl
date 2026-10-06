package mcpbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

const sessionAPIPrefix = "mecatl:poc:broker-session:v1:"

// SessionAPI is a single-replica PoC facade over the donor Process. Redis owns
// lifecycle metadata only; all credentials remain in native encryptedAuthStorage.
// The caller owns Process and Redis. Do not run two facades over one namespace.
type SessionAPI struct {
	mu       sync.Mutex
	process  *Process
	redis    redis.UniversalClient
	workload func(context.Context) *session.Principal
	owner    func(context.Context) ([32]byte, error)
	states   map[c.SessionRef]*apiState
	closed   bool
	cancel   context.CancelFunc
	ctx      context.Context
	workers  sync.WaitGroup
	now      func() time.Time
}
type apiRecord struct {
	Ref             c.SessionRef
	Owner, Workload [32]byte
	Incarnation     session.IncarnationID
	Binding         session.ExternalBinding
	Catalogue       c.CatalogueRef
	ExpiresAt       time.Time
	Connected       bool
	Withdrawing     bool
	Connection      string
	Anonymous       []apiDescriptor
	Profile         [32]byte
	Account         [32]byte
	Custody         *c.StagedCredentialCustody
}
type apiDescriptor struct {
	Backend  string
	Spec     tool.ToolSpec
	ReadOnly bool
}
type apiState struct {
	record     apiRecord
	attachment *Attachment
	catalogue  c.Catalogue
	enrollment *apiEnrollment
	parked     map[c.AuthorizationRef]*apiParked
	receipts   map[session.ToolCallID]*apiReceipt
	recovering bool
}
type apiEnrollment struct {
	ref    c.EnrollmentRef
	native c.WorkspaceEnrollmentRef
	prompt c.BrowserPrompt
	status c.FlowStatus
}
type apiParked struct {
	call       c.Call
	native     session.ExternalAuthorization
	connection string
	descriptor [32]byte
	account    [32]byte
	completed  c.Catalogue
	terminal   *c.FlowStatus
}
type apiReceipt struct {
	digest  [32]byte
	done    chan struct{}
	outcome c.InvocationOutcome
}

var _ c.SessionService = (*SessionAPI)(nil)

// NewSessionAPI requires verified owner context and an independently verified
// workload resolver. Every configured backend must complete discovery before publication.
func NewSessionAPI(p *Process, r redis.UniversalClient, workload func(context.Context) *session.Principal) (*SessionAPI, error) {
	if p == nil || r == nil || workload == nil {
		return nil, c.ErrStateUnavailable
	}
	if len(p.construction.protectedBackends) > 0 && p.custody == nil {
		return nil, c.ErrContinuityUnavailable
	}
	ctx, cancel := context.WithCancel(p.ctx)
	return &SessionAPI{process: p, redis: r, workload: workload, owner: func(ctx context.Context) ([32]byte, error) {
		return c.ContinuityPrincipalPartition(c.ContinuityPartitionOwner, session.PrincipalFromContext(ctx))
	}, states: make(map[c.SessionRef]*apiState), ctx: ctx, cancel: cancel, now: time.Now}, nil
}

// NewProcessSessionAPI borrows native protected Redis when present, otherwise the
// caller-owned metadata client. Neither path changes native credential ownership.
// The explicit resolver never treats the donor workload as a human owner.
func NewProcessSessionAPI(p *Process, owner func(context.Context) ([32]byte, error), workload func(context.Context) *session.Principal, metadata ...redis.UniversalClient) (*SessionAPI, error) {
	if p == nil || owner == nil || len(metadata) > 1 {
		return nil, c.ErrStateUnavailable
	}
	var client redis.UniversalClient
	if p.protectedStorage != nil {
		if len(metadata) != 0 {
			return nil, c.ErrStateUnavailable
		}
		client = p.protectedStorage.client
	} else if len(metadata) == 1 {
		client = metadata[0]
	}
	api, err := NewSessionAPI(p, client, workload)
	if err != nil {
		return nil, err
	}
	api.owner = owner
	return api, nil
}

func (s *SessionAPI) InspectConnectors(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef) (c.ConnectorInventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validAPIRef(string(ref)) || !validAPIRef(string(cat)) {
		return c.ConnectorInventory{}, c.ErrStateUnavailable
	}
	o, w, err := s.partitions(ctx)
	if err != nil {
		return c.ConnectorInventory{}, c.ErrStateUnavailable
	}
	var record apiRecord
	if st := s.states[ref]; st != nil {
		record = st.record
		if st.catalogue == nil || st.catalogue.Ref() != cat {
			return c.ConnectorInventory{}, c.ErrStateUnavailable
		}
	} else {
		b, err := s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Bytes()
		if errors.Is(err, redis.Nil) {
			return c.ConnectorInventory{}, c.ErrStateUnavailable
		}
		if err != nil {
			return c.ConnectorInventory{}, err
		}
		if json.Unmarshal(b, &record) != nil {
			return c.ConnectorInventory{}, c.ErrStateUnavailable
		}
	}
	if record.Ref != ref || record.Owner != o || record.Workload != w || record.Profile != s.profile() || !s.now().Before(record.ExpiresAt) || record.Catalogue != cat {
		return c.ConnectorInventory{}, c.ErrStateUnavailable
	}
	return s.process.Runtime.InspectConnectors(ctx, session.SessionID(record.Ref), record.Binding)
}

func (s *SessionAPI) Ready(ctx context.Context) error {
	return s.redis.Ping(ctx).Err()
}

// Close drains process-local work without deleting durable session identity or custody.
// Close this facade before closing its borrowed Process and Redis client.
func (s *SessionAPI) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for ref, st := range s.states {
		if st.attachment != nil {
			_, e := st.attachment.Close(context.Background())
			err = errors.Join(err, e)
		}
		delete(s.states, ref)
	}
	return err
}
func apiRef() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func validAPIRef(v string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(v)
	return err == nil && len(b) == 32 && len(v) == 43
}
func (s *SessionAPI) partitions(ctx context.Context) ([32]byte, [32]byte, error) {
	if s.closed {
		return [32]byte{}, [32]byte{}, c.ErrStateUnavailable
	}
	o, err := s.owner(ctx)
	if err != nil {
		return o, [32]byte{}, err
	}
	w, err := c.ContinuityPrincipalPartition(c.ContinuityPartitionWorkload, s.workload(ctx))
	return o, w, err
}
func (s *SessionAPI) profile() [32]byte {
	b, _ := json.Marshal(s.process.construction.backends)
	return sha256.Sum256(b)
}
func (s *SessionAPI) save(ctx context.Context, st *apiState) error {
	b, err := json.Marshal(st.record)
	if err != nil {
		return err
	}
	ttl := st.record.ExpiresAt.Sub(s.now())
	if ttl <= 0 {
		return c.ErrStateUnavailable
	}
	key := sessionAPIPrefix + string(st.record.Ref)
	if err = s.redis.Set(ctx, key, b, ttl).Err(); err == nil {
		return nil
	}
	// A lost write acknowledgement is not proof that adoption failed.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stored, readErr := s.redis.Get(checkCtx, key).Bytes()
	if readErr == nil && bytes.Equal(stored, b) {
		return nil
	}
	return err
}
func (s *SessionAPI) assertion(st *apiState) c.CustodyAssertion {
	r := st.record
	deadline := s.now().Add(c.ContinuityAttemptTTL)
	if r.Custody.ExpiresAt.Before(deadline) && s.now().Before(r.Custody.ExpiresAt) {
		deadline = r.Custody.ExpiresAt
	}
	return c.CustodyAssertion{Guard: c.ContinuityGuard{SessionID: session.SessionID(r.Ref), SessionIncarnation: r.Incarnation, OwnerPartition: r.Owner, WorkloadPartition: r.Workload, ProfileDigest: r.Custody.ProfileDigest, Providers: r.Custody.Providers}, RecoveryReference: r.Custody.RecoveryReference, AttemptDeadline: deadline}
}
func (s *SessionAPI) empty(st *apiState) error {
	cat, err := c.NewCatalogue(st.record.Catalogue, nil)
	st.catalogue = cat
	return err
}

// Reap only identities already owned by this facade, never a caller-supplied ID.
func (s *SessionAPI) reap(ctx context.Context) error {
	for ref, st := range s.states {
		expired := !s.now().Before(st.record.ExpiresAt)
		if !expired {
			err := s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Err()
			if err != nil && !errors.Is(err, redis.Nil) {
				return c.ErrStateUnavailable
			}
			expired = errors.Is(err, redis.Nil)
		}
		if !expired {
			continue
		}
		if _, err := s.process.DeleteSession(ctx, session.SessionID(ref)); err != nil {
			return err
		}
		if st.attachment != nil {
			if _, err := st.attachment.Close(context.Background()); err != nil {
				return err
			}
		}
		delete(s.states, ref)
	}
	return nil
}

func (s *SessionAPI) finishRecovery(ctx context.Context, st *apiState) error {
	// Retain the exact handle and candidate revision across both save ambiguity
	// and Commit cancellation. Abort would invalidate a possibly adopted binding.
	if err := s.save(ctx, st); err != nil {
		return err
	}
	if err := st.attachment.Commit(ctx); err != nil {
		return err
	}
	cat, err := s.catalogue(st, st.record.Catalogue, st.attachment.Tools(), st.record.Account)
	if err != nil {
		return err
	}
	st.catalogue = cat
	st.recovering = false
	return nil
}

func (s *SessionAPI) state(ctx context.Context, ref c.SessionRef) (*apiState, error) {
	if !validAPIRef(string(ref)) {
		return nil, c.ErrStateUnavailable
	}
	o, w, err := s.partitions(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.reap(ctx); err != nil {
		return nil, err
	}
	// Check durable existence even for cached handles: Redis failure is fail-closed.
	b, err := s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Bytes()
	if err != nil {
		return nil, c.ErrStateUnavailable
	}
	var record apiRecord
	if json.Unmarshal(b, &record) != nil || record.Ref != ref || record.Owner != o || record.Workload != w || record.Profile != s.profile() || !s.now().Before(record.ExpiresAt) {
		return nil, c.ErrStateUnavailable
	}
	st := s.states[ref]
	if st == nil {
		if len(s.states) >= 32 {
			return nil, c.ErrCapacity
		}
		st = &apiState{record: record, parked: make(map[c.AuthorizationRef]*apiParked), receipts: make(map[session.ToolCallID]*apiReceipt)}
		if record.Withdrawing {
			if err = s.disconnect(ctx, st); err != nil {
				return nil, err
			}
			record = st.record
		}
		if record.Connected && record.Custody != nil && s.now().Before(record.Custody.ExpiresAt) {
			if err = s.process.CommitCredentialCustody(ctx, s.assertion(st)); err != nil {
				return nil, err
			}
			recovered, err := s.process.RecoverCredentialAttachment(ctx, s.assertion(st), apiRef())
			if err != nil {
				return nil, err
			}
			st.attachment = recovered.Attachment.(*Attachment)
			st.record.Binding = st.attachment.Binding()
			st.record.Catalogue = c.CatalogueRef(apiRef())
			st.recovering = true
			s.states[ref] = st
			if err = s.finishRecovery(ctx, st); err != nil {
				return nil, err
			}
		} else if record.Connected && record.Custody == nil && len(s.process.construction.protectedBackends) == 0 {
			if err = s.attach(ctx, st); err != nil {
				return nil, err
			}
			routes := make([]route, 0, len(record.Anonymous))
			for _, d := range record.Anonymous {
				routes = append(routes, route{backend: d.Backend, spec: d.Spec, readOnly: d.ReadOnly})
			}
			native := c.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID(apiRef()), RequiredServices: uint32(len(s.process.construction.anonymous)), ExpiresAt: record.ExpiresAt}
			completed := &completedWorkspaceEnrollment{ref: native, routes: routes}
			cat, e := st.attachment.installCompletedEnrollment(completed)
			if e != nil {
				return nil, e
			}
			st.record.Catalogue = c.CatalogueRef(apiRef())
			st.catalogue, err = s.catalogue(st, st.record.Catalogue, cat.Tools(), st.record.Account)
			if err != nil {
				return nil, err
			}
			if err = s.save(ctx, st); err != nil {
				return nil, err
			}
			st.attachment.logical.mu.Lock()
			st.attachment.logical.completedEnrollment = completed
			st.attachment.logical.mu.Unlock()
		} else {
			// Native custody expiry withdraws authority, not the stable session identity.
			if err = s.disconnect(ctx, st); err != nil {
				return nil, err
			}
		}
		s.states[ref] = st
	}
	if st.recovering {
		if err = s.finishRecovery(ctx, st); err != nil {
			return nil, err
		}
	}
	if st.record.Withdrawing {
		if err = s.disconnect(ctx, st); err != nil {
			return nil, err
		}
	}
	if st.record.Connected && st.record.Custody != nil && !s.now().Before(st.record.Custody.ExpiresAt) {
		if err = s.disconnect(ctx, st); err != nil {
			return nil, err
		}
	}
	for _, p := range st.parked {
		if p.terminal == nil && !s.now().Before(p.native.ExpiresAt) {
			if _, err := st.attachment.CancelAuthorization(ctx, p.native); err != nil {
				return nil, err
			}
			status := c.FlowStatus{Kind: c.FlowExpired}
			p.terminal = &status
			p.call.Arguments = nil
		}
	}
	return st, nil
}
func (s *SessionAPI) attach(ctx context.Context, st *apiState) error {
	if st.attachment != nil {
		return nil
	}
	a, _, err := s.process.AttachSession(ctx, session.SessionID(st.record.Ref))
	if err != nil {
		return err
	}
	st.attachment = a.(*Attachment)
	st.record.Binding = a.Binding()
	if err = s.save(ctx, st); err != nil {
		return err
	}
	return a.Commit(ctx)
}
func (s *SessionAPI) OpenSession(ctx context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st *apiState
	var err error
	if saved != nil {
		st, err = s.state(ctx, *saved)
	} else {
		o, w, e := s.partitions(ctx)
		if e != nil {
			return c.SessionSnapshot{}, e
		}
		if e = s.reap(ctx); e != nil {
			return c.SessionSnapshot{}, e
		}
		if len(s.states) >= 32 {
			return c.SessionSnapshot{}, c.ErrCapacity
		}
		st = &apiState{record: apiRecord{Ref: c.SessionRef(apiRef()), Owner: o, Workload: w, Profile: s.profile(), Incarnation: session.NewIncarnationID(), Catalogue: c.CatalogueRef(apiRef()), ExpiresAt: s.now().Add(30 * 24 * time.Hour)}, parked: make(map[c.AuthorizationRef]*apiParked), receipts: make(map[session.ToolCallID]*apiReceipt)}
		err = s.empty(st)
		if err == nil {
			err = s.save(ctx, st)
		}
		if err == nil {
			s.states[st.record.Ref] = st
		}
	}
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	return c.SessionSnapshot{Ref: st.record.Ref, ExpiresAt: st.record.ExpiresAt, Catalogue: st.catalogue}, nil
}

// apiAuthorizationTool adds per-call authorization only to the session facade;
// the donor's ordinary enrolled catalogue keeps its existing wrapper contract.
type apiAuthorizationTool struct {
	*protectedSessionTool
	account [32]byte
}

func (t *apiAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	a := t.attachment
	a.mu.RLock()
	tsid := a.verifiedTSID
	a.mu.RUnlock()
	if t.account == ([32]byte{}) {
		return session.ExternalAuthorization{}, false, errors.New("mcpbroker: native account identity unavailable; resume refused")
	}
	ready, err := a.runtime.process.nativeGrantReady(ctx, tsid)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	if ready {
		account, err := a.runtime.process.nativeAccount(ctx, tsid)
		if err != nil || account != t.account {
			return session.ExternalAuthorization{}, false, errors.New("mcpbroker: native account identity unavailable or changed; resume refused")
		}
		a.logical.mu.RLock()
		grant, recovered := a.logical.brokerCredential, a.logical.recoveredSource
		a.logical.mu.RUnlock()
		if grant != nil {
			_, err = (&brokerTokenSource{runtime: a.runtime, logical: a.logical, ctx: ctx}).Token()
			if err != nil {
				a.logical.mu.RLock()
				ready = a.logical.brokerCredential != nil
				a.logical.mu.RUnlock()
				if ready {
					return session.ExternalAuthorization{}, false, err
				}
			}
		} else if recovered != nil {
			if _, err := recovered.Token(); err != nil {
				return session.ExternalAuthorization{}, false, err
			}
		} else {
			ready = false
		}
		if ready {
			return session.ExternalAuthorization{}, false, nil
		}
	}
	// Native credential readiness established non-dispatch. Discard only the
	// outer credential; browser OAuth and upstream storage remain donor-owned.
	a.logical.mu.Lock()
	clearGrantToken(a.logical.brokerCredential)
	a.logical.brokerCredential = nil
	a.logical.mu.Unlock()
	return t.protectedSessionTool.RequestAuthorization(ctx, call)
}

func (s *SessionAPI) catalogue(st *apiState, ref c.CatalogueRef, tools []tool.Tool, account [32]byte) (c.Catalogue, error) {
	tools = append([]tool.Tool(nil), tools...)
	if s.process.custody != nil {
		for i, candidate := range tools {
			route, ok := st.attachment.lookupRoute(candidate.Spec().Name)
			if ok && route.broker {
				route.oauth = s.process.protectedTarget
				tools[i] = &apiAuthorizationTool{protectedSessionTool: &protectedSessionTool{&sessionTool{attachment: st.attachment, route: route}}, account: account}
			}
		}
	}
	if len(tools) > 0 {
		if query, ok := st.attachment.CallMcpWithQueryTool().(*attachmentQueryTool); ok {
			targets := make(map[string]route, len(tools))
			for _, candidate := range tools {
				if route, ok := st.attachment.lookupRoute(candidate.Spec().Name); ok {
					targets[candidate.Spec().Name] = route
				}
			}
			attachment := st.attachment
			query.targetTool = func(_ context.Context, call session.ToolCall, filter string) (tool.Tool, error) {
				route, ok := targets[call.Name]
				if !ok {
					return nil, errors.New("broker tool route is unavailable")
				}
				base := &sessionTool{attachment: attachment, route: route, queryFilter: filter}
				if route.broker && s.process.custody != nil {
					base.route.oauth = s.process.protectedTarget
					return &apiAuthorizationTool{protectedSessionTool: &protectedSessionTool{base}, account: account}, nil
				}
				if route.oauth != nil {
					return &protectedSessionTool{base}, nil
				}
				return base, nil
			}
			tools = append(tools, query)
		}
	}
	return c.NewCatalogue(ref, tools)
}

func (s *SessionAPI) publish(ctx context.Context, st *apiState, tools []tool.Tool, enrollment c.WorkspaceEnrollmentRef) error {
	next := st.record
	next.Connected = true
	if next.Connection == "" {
		next.Connection = apiRef()
	}
	if len(s.process.construction.protectedBackends) > 0 {
		st.attachment.mu.RLock()
		tsid := st.attachment.verifiedTSID
		st.attachment.mu.RUnlock()
		account, err := s.process.nativeAccount(ctx, tsid)
		if err != nil {
			return err
		}
		if next.Account != ([32]byte{}) && next.Account != account {
			return errors.New("mcpbroker: account changed; original action cannot be resumed")
		}
		next.Account = account
		if next.Custody != nil {
			record, err := s.process.custody.LoadCurrent(ctx, recoveryID(next.Custody.RecoveryReference), custodyGuardFromContract(s.assertion(st).Guard))
			if err != nil {
				return c.ErrContinuityUnavailable
			}
			if record.TSID != tsid {
				next.Custody = nil
			}
		}
	}
	if len(s.process.construction.protectedBackends) > 0 && next.Custody == nil {
		staged, err := st.attachment.StageCredentialCustody(ctx, apiRef(), c.ContinuityGuard{SessionID: session.SessionID(next.Ref), SessionIncarnation: next.Incarnation, OwnerPartition: next.Owner, WorkloadPartition: next.Workload}, enrollment, s.now().Add(c.ContinuityAttemptTTL))
		if err != nil {
			return err
		}
		next.Custody = &staged
	}
	cat, err := s.catalogue(st, c.CatalogueRef(apiRef()), tools, next.Account)
	if err != nil {
		return err
	}
	next.Catalogue = cat.Ref()
	old := st.record
	st.record = next
	if err = s.save(ctx, st); err != nil {
		st.record = old
		return err
	}
	if next.Custody != nil {
		if err = s.process.CommitCredentialCustody(ctx, s.assertion(st)); err != nil {
			return err
		}
	}
	st.catalogue = cat
	return nil
}
func (s *SessionAPI) BeginEnrollment(ctx context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if len(s.process.construction.backends) == 0 {
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	if st.record.Connected && st.record.Catalogue == st.catalogue.Ref() {
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentAlreadyConnected}, nil
	}
	if st.enrollment != nil && st.enrollment.status.Kind == c.FlowPending && s.now().Before(st.enrollment.prompt.ExpiresAt) {
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentStartedKind, Started: &c.EnrollmentStarted{Ref: st.enrollment.ref, Prompt: st.enrollment.prompt}}, nil
	}
	if err = s.attach(ctx, st); err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if len(s.process.construction.protectedBackends) == 0 {
		if err = st.attachment.ResetWorkspaceEnrollment(ctx); err != nil {
			return c.BeginEnrollmentOutcome{}, err
		}
		routes, err := discoverCompleteAnonymous(ctx, s.process.construction.anonymous, s.process.reservedToolNames, s.process.diag)
		if err != nil {
			return c.BeginEnrollmentOutcome{}, err
		}
		st.record.Anonymous = nil
		for _, r := range routes {
			st.record.Anonymous = append(st.record.Anonymous, apiDescriptor{Backend: r.backend, Spec: copySpec(r.spec), ReadOnly: r.readOnly})
		}
		native := c.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID(apiRef()), RequiredServices: uint32(len(s.process.construction.anonymous)), ExpiresAt: st.record.ExpiresAt}
		completed := &completedWorkspaceEnrollment{ref: native, routes: routes}
		cat, e := st.attachment.installCompletedEnrollment(completed)
		if e != nil {
			return c.BeginEnrollmentOutcome{}, e
		}
		if err = s.publish(ctx, st, cat.Tools(), native); err != nil {
			return c.BeginEnrollmentOutcome{}, err
		}
		st.attachment.logical.mu.Lock()
		st.attachment.logical.completedEnrollment = completed
		st.attachment.logical.mu.Unlock()
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentCompletedKind, Catalogue: st.catalogue}, nil
	}
	p, err := st.attachment.BeginWorkspaceEnrollment(ctx)
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	st.enrollment = &apiEnrollment{ref: c.EnrollmentRef(apiRef()), native: p.Ref, prompt: c.BrowserPrompt{URL: p.URL, ExpiresAt: p.Ref.ExpiresAt}, status: c.FlowStatus{Kind: c.FlowPending}}
	return c.BeginEnrollmentOutcome{Kind: c.EnrollmentStartedKind, Started: &c.EnrollmentStarted{Ref: st.enrollment.ref, Prompt: st.enrollment.prompt}}, nil
}
func flow(status session.AuthorizationStatus) c.FlowStatus {
	switch status {
	case session.AuthorizationPending:
		return c.FlowStatus{Kind: c.FlowPending}
	case session.AuthorizationCancelled:
		return c.FlowStatus{Kind: c.FlowCancelled}
	case session.AuthorizationExpired:
		return c.FlowStatus{Kind: c.FlowExpired}
	default:
		return c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureAuthorizationFailed}
	}
}
func (s *SessionAPI) ObserveEnrollment(ctx context.Context, ref c.SessionRef, e c.EnrollmentRef) (c.FlowStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return c.FlowStatus{}, err
	}
	if st.enrollment == nil || st.enrollment.ref != e {
		return c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureInterrupted}, nil
	}
	if st.enrollment.status.Kind != c.FlowPending {
		return st.enrollment.status, nil
	}
	r, err := st.attachment.ObserveWorkspaceEnrollment(ctx, st.enrollment.native)
	if err != nil {
		return c.FlowStatus{}, err
	}
	if r.Status == c.WorkspaceEnrollmentConnected {
		if err = s.publish(ctx, st, r.Catalogue.Tools(), r.Ref); err != nil {
			return c.FlowStatus{}, err
		}
		st.enrollment.status = c.FlowStatus{Kind: c.FlowCompleted, Catalogue: st.catalogue}
	} else {
		st.enrollment.status = flow(session.AuthorizationStatus(r.Status))
	}
	return st.enrollment.status, nil
}
func (s *SessionAPI) CancelEnrollment(ctx context.Context, ref c.SessionRef, e c.EnrollmentRef) (c.CancelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return 0, err
	}
	if st.enrollment == nil || st.enrollment.ref != e || st.enrollment.status.Kind != c.FlowPending {
		return c.AlreadyResolved, nil
	}
	r, err := st.attachment.CancelWorkspaceEnrollment(ctx, st.enrollment.native)
	if err != nil {
		return 0, err
	}
	st.enrollment.status = flow(session.AuthorizationStatus(r.Status))
	if r.Status == c.WorkspaceEnrollmentCancelled {
		return c.Cancelled, nil
	}
	return c.AlreadyResolved, nil
}
func noDispatch(r c.FailureReason) c.InvocationOutcome {
	return c.InvocationOutcome{Kind: c.InvocationNotDispatched, Reason: r}
}
func descriptor(t tool.Tool) [32]byte {
	b, _ := json.Marshal(struct {
		Spec                   tool.ToolSpec
		ReadOnly, Serial, Auth bool
	}{t.Spec(), t.ReadOnly(), isSerial(t), isAuth(t)})
	return sha256.Sum256(b)
}
func (st *apiState) invocationDescriptor(t tool.Tool, call c.Call) ([32]byte, error) {
	if t == nil {
		return [32]byte{}, c.ErrStateUnavailable
	}
	if call.Name == "CallMcpWithQuery" {
		native, _, err := (&attachmentQueryTool{}).target(session.ToolCall{ID: call.ID, Name: call.Name, Args: call.Arguments})
		if err != nil {
			return [32]byte{}, err
		}
		t = find(st.catalogue, native.Name)
		if t == nil {
			return [32]byte{}, c.ErrStateUnavailable
		}
	}
	return descriptor(t), nil
}

func isSerial(t tool.Tool) bool { _, ok := t.(tool.DispatchSerial); return ok }
func isAuth(t tool.Tool) bool   { _, ok := t.(tool.AuthorizationRequester); return ok }
func find(cat c.Catalogue, name string) tool.Tool {
	for _, t := range cat.Tools() {
		if t.Spec().Name == name {
			return t
		}
	}
	return nil
}
func callDigest(call c.Call) [32]byte { b, _ := json.Marshal(call); return sha256.Sum256(b) }
func validCall(call c.Call) bool {
	return len(call.ID) > 0 && len(call.ID) <= 256 && utf8.ValidString(string(call.ID)) && len(call.Name) > 0 && len(call.Name) <= 256 && utf8.ValidString(call.Name) && len(call.Arguments) <= 256*1024 && json.Valid(call.Arguments)
}
func (s *SessionAPI) prepareInvocation(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call) (c.InvocationOutcome, *apiReceipt, error) {
	if err := ctx.Err(); err != nil {
		return c.InvocationOutcome{}, nil, err
	}
	if !validCall(call) {
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	active := 0
	for ref, p := range st.parked {
		if p.terminal != nil {
			if len(st.parked) >= 32 {
				delete(st.parked, ref)
			}
			continue
		}
		active++
		if p.call.ID == call.ID && callDigest(p.call) != callDigest(call) {
			return noDispatch(c.FailureCallChanged), nil, nil
		}
	}
	if cat != st.catalogue.Ref() || cat != st.record.Catalogue {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	if !st.record.Connected {
		return noDispatch(c.FailureAuthorityWithdrawn), nil, nil
	}
	t := find(st.catalogue, call.Name)
	if t == nil {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	invocation, err := st.invocationDescriptor(t, call)
	if err != nil {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	digest := callDigest(call)
	if receipt := st.receipts[call.ID]; receipt != nil {
		if receipt.digest != digest {
			return noDispatch(c.FailureCallChanged), nil, nil
		}
		return c.InvocationOutcome{}, receipt, nil
	}
	if len(st.receipts) >= 64 || active >= 16 {
		return noDispatch(c.FailureCapacity), nil, nil
	}
	native := session.ToolCall{ID: call.ID, Name: call.Name, Args: append([]byte(nil), call.Arguments...)}
	if requester, ok := t.(tool.AuthorizationRequester); ok {
		if s.process.custody != nil {
			// A callback may have installed the new outer grant before Observe
			// adopts its TSID/catalogue. Do not clear that grant on another preflight.
			for ref, p := range st.parked {
				if p.terminal == nil && p.completed == nil {
					if !s.now().Before(p.native.ExpiresAt) {
						return noDispatch(c.FailureExpired), nil, nil
					}
					if callDigest(p.call) == digest {
						return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: ref}, nil, nil
					}
					return noDispatch(c.FailureAuthorizationFailed), nil, nil
				}
			}
		}
		auth, required, err := requester.RequestAuthorization(ctx, native)
		if err != nil {
			return c.InvocationOutcome{}, nil, err
		}
		if required {
			for ref, p := range st.parked {
				if p.terminal == nil && p.native.ID == auth.ID && p.native.Binding == auth.Binding {
					if callDigest(p.call) != digest {
						return noDispatch(c.FailureCallChanged), nil, nil
					}
					return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: ref}, nil, nil
				}
			}
			ref := c.AuthorizationRef(apiRef())
			call.Arguments = append([]byte(nil), call.Arguments...)
			st.parked[ref] = &apiParked{call: call, native: auth, connection: st.record.Connection, account: st.record.Account, descriptor: invocation}
			return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: ref}, nil, nil
		}
	}
	return c.InvocationOutcome{}, nil, nil
}

func (s *SessionAPI) invoke(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call) (c.InvocationOutcome, *apiReceipt, error) {
	out, existing, err := s.prepareInvocation(ctx, st, cat, call)
	if err != nil || existing != nil || out.Kind != "" {
		return out, existing, err
	}
	t := find(st.catalogue, call.Name)
	native := session.ToolCall{ID: call.ID, Name: call.Name, Args: append([]byte(nil), call.Arguments...)}
	receipt := &apiReceipt{digest: callDigest(call), done: make(chan struct{})}
	st.receipts[call.ID] = receipt
	// Cancellation changes only what the caller observes. Dispatch ownership and
	// the bounded receipt outlive the caller; no retry ever executes a second time.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	s.workers.Add(1)
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: string(st.record.Ref), Revision: string(cat)}, nofs.New(), memledger.New(), nil)
	go func() {
		defer s.workers.Done()
		defer stop()
		defer cancel()
		out := c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
		defer func() {
			if recover() != nil {
				out = c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
			}
			receipt.outcome = out
			close(receipt.done)
		}()
		result, err := t.Execute(runCtx, native, env)
		encoded, _ := json.Marshal(result)
		if err == nil && result.CallID == native.ID && len(encoded) <= 256*1024 {
			out, _ = c.NewInvocationOutcome(c.InvocationCompleted, &result, "", c.FailureUnspecified)
			if !out.Valid() {
				out = c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
			}
		}
	}()
	return c.InvocationOutcome{}, receipt, nil
}
func waitReceipt(ctx context.Context, out c.InvocationOutcome, r *apiReceipt, err error) (c.InvocationOutcome, error) {
	if err != nil || r == nil {
		return out, err
	}
	select {
	case <-r.done:
		return c.NewInvocationOutcome(r.outcome.Kind, r.outcome.Result, r.outcome.Authorization, r.outcome.Reason)
	case <-ctx.Done():
		return c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}, nil
	}
}
func (s *SessionAPI) CheckAuthorization(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call *c.Call, auth c.AuthorizationRef) (c.AuthorizationCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return c.AuthorizationCheck{}, err
	}
	st, err := s.state(ctx, ref)
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	if (call == nil) == (auth == "") {
		return c.AuthorizationCheck{}, c.ErrStateUnavailable
	}
	if call == nil {
		p := st.parked[auth]
		if p == nil || p.terminal != nil {
			return c.AuthorizationCheck{Reason: c.FailureInterrupted}, nil
		}
		if cat != st.record.Catalogue || cat != st.catalogue.Ref() {
			return c.AuthorizationCheck{Reason: c.FailureCatalogueChanged}, nil
		}
		if !s.now().Before(p.native.ExpiresAt) {
			return c.AuthorizationCheck{Reason: c.FailureExpired}, nil
		}
		if p.connection != st.record.Connection || !st.record.Connected {
			return c.AuthorizationCheck{Reason: c.FailureAuthorityWithdrawn}, nil
		}
		t := find(st.catalogue, p.call.Name)
		invocation, err := st.invocationDescriptor(t, p.call)
		if err != nil || invocation != p.descriptor {
			return c.AuthorizationCheck{Reason: c.FailureCatalogueChanged}, nil
		}
		status, err := st.attachment.AuthorizationStatus(ctx, p.native)
		if err != nil {
			return c.AuthorizationCheck{}, err
		}
		if status == session.AuthorizationGranted {
			return c.AuthorizationCheck{Ready: true}, nil
		}
		if status != session.AuthorizationPending {
			terminal := flow(status)
			p.terminal = &terminal
			p.call.Arguments = nil
			return c.AuthorizationCheck{Reason: c.FailureAuthorizationFailed}, nil
		}
		return c.AuthorizationCheck{Authorization: auth, ExpiresAt: p.native.ExpiresAt}, nil
	}
	out, receipt, err := s.prepareInvocation(ctx, st, cat, *call)
	if receipt != nil {
		return c.AuthorizationCheck{Reason: c.FailureInterrupted}, nil
	}
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	if out.Kind == c.InvocationAuthorizationRequired {
		return c.AuthorizationCheck{Authorization: out.Authorization, ExpiresAt: st.parked[out.Authorization].native.ExpiresAt}, nil
	}
	if out.Kind == c.InvocationNotDispatched {
		return c.AuthorizationCheck{Reason: out.Reason}, nil
	}
	return c.AuthorizationCheck{Ready: true}, nil
}

func (s *SessionAPI) InvokeTool(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call c.Call) (c.InvocationOutcome, error) {
	s.mu.Lock()
	st, err := s.state(ctx, ref)
	if err != nil {
		s.mu.Unlock()
		return c.InvocationOutcome{}, err
	}
	out, r, err := s.invoke(ctx, st, cat, call)
	s.mu.Unlock()
	return waitReceipt(ctx, out, r, err)
}
func (s *SessionAPI) BeginAuthorization(ctx context.Context, ref c.SessionRef, a c.AuthorizationRef) (c.BrowserPrompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	p := st.parked[a]
	if p == nil || p.terminal != nil {
		return c.BrowserPrompt{}, c.ErrAuthorizationNotFound
	}
	u, err := st.attachment.PresentAuthorization(ctx, p.native)
	return c.BrowserPrompt{URL: u, ExpiresAt: p.native.ExpiresAt}, err
}
func (s *SessionAPI) ObserveAuthorization(ctx context.Context, ref c.SessionRef, a c.AuthorizationRef) (c.FlowStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return c.FlowStatus{}, err
	}
	p := st.parked[a]
	if p == nil {
		return c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureInterrupted}, nil
	}
	if p.terminal != nil {
		return *p.terminal, nil
	}
	if p.connection != st.record.Connection {
		return c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureAuthorityWithdrawn}, nil
	}
	if p.completed != nil {
		return c.FlowStatus{Kind: c.FlowCompleted, Catalogue: p.completed}, nil
	}
	status, err := st.attachment.AuthorizationStatus(ctx, p.native)
	if err != nil {
		return c.FlowStatus{}, err
	}
	if status != session.AuthorizationGranted {
		status := flow(status)
		if status.Kind != c.FlowPending {
			p.terminal = &status
			p.call.Arguments = nil
		}
		return status, nil
	}
	var tools []tool.Tool
	var native c.WorkspaceEnrollmentRef
	if s.process.custody != nil {
		native = c.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID(apiRef()), RequiredServices: uint32(len(s.process.construction.backends)), ExpiresAt: p.native.ExpiresAt}
		frozen, _, e := st.attachment.freezeAuthenticatedCatalogue(ctx, native, s.process, &brokerTokenSource{runtime: st.attachment.runtime, logical: st.attachment.logical, ctx: ctx}, s.process.reservedToolNames, true, true)
		if e != nil {
			return c.FlowStatus{}, e
		}
		tools = frozen.Tools()
		st.attachment.mu.RLock()
		tsid := st.attachment.verifiedTSID
		st.attachment.mu.RUnlock()
		account, e := s.process.nativeAccount(ctx, tsid)
		if e != nil || p.account == ([32]byte{}) || account != p.account {
			terminal := c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureAuthorityWithdrawn}
			p.terminal = &terminal
			// Keep the original call refused even after its parked tombstone is evicted.
			if len(st.receipts) < 64 {
				receipt := &apiReceipt{digest: callDigest(p.call), done: make(chan struct{}), outcome: noDispatch(c.FailureAuthorityWithdrawn)}
				close(receipt.done)
				st.receipts[p.call.ID] = receipt
			}
			p.call.Arguments = nil
			return terminal, nil
		}
	} else {
		tools, err = st.attachment.RefreshGrantedAuthorizationCatalogue(ctx, p.native)
		if err != nil {
			return c.FlowStatus{}, err
		}
		st.attachment.mu.RLock()
		if st.attachment.catalogue.frozen != nil {
			native = st.attachment.catalogue.frozen.Ref()
		}
		st.attachment.mu.RUnlock()
	}
	if err = s.publish(ctx, st, tools, native); err != nil {
		return c.FlowStatus{}, err
	}
	p.completed = st.catalogue
	return c.FlowStatus{Kind: c.FlowCompleted, Catalogue: st.catalogue}, nil
}
func (s *SessionAPI) CancelAuthorization(ctx context.Context, ref c.SessionRef, a c.AuthorizationRef) (c.CancelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return 0, err
	}
	p := st.parked[a]
	if p == nil || p.terminal != nil {
		return c.AlreadyResolved, nil
	}
	r, err := st.attachment.CancelAuthorization(ctx, p.native)
	if err != nil {
		return 0, err
	}
	if r == c.CancelCancelled {
		status := c.FlowStatus{Kind: c.FlowCancelled}
		p.terminal = &status
		p.call.Arguments = nil
		return c.Cancelled, nil
	}
	return c.AlreadyResolved, nil
}
func (s *SessionAPI) ResumeTool(ctx context.Context, ref c.SessionRef, a c.AuthorizationRef, cat c.CatalogueRef) (c.InvocationOutcome, error) {
	s.mu.Lock()
	st, err := s.state(ctx, ref)
	if err != nil {
		s.mu.Unlock()
		return c.InvocationOutcome{}, err
	}
	p := st.parked[a]
	if p == nil || p.terminal != nil {
		s.mu.Unlock()
		return noDispatch(c.FailureInterrupted), nil
	}
	if s.process.custody != nil && (p.completed == nil || p.account == ([32]byte{}) || p.account != st.record.Account) {
		s.mu.Unlock()
		return noDispatch(c.FailureAuthorityWithdrawn), nil
	}
	if cat != st.catalogue.Ref() {
		s.mu.Unlock()
		return noDispatch(c.FailureCatalogueChanged), nil
	}
	if !s.now().Before(p.native.ExpiresAt) {
		s.mu.Unlock()
		return noDispatch(c.FailureExpired), nil
	}
	if p.connection != st.record.Connection || !st.record.Connected {
		s.mu.Unlock()
		return noDispatch(c.FailureAuthorityWithdrawn), nil
	}
	t := find(st.catalogue, p.call.Name)
	invocation, err := st.invocationDescriptor(t, p.call)
	if err != nil || invocation != p.descriptor {
		s.mu.Unlock()
		return noDispatch(c.FailureCatalogueChanged), nil
	}
	status, err := st.attachment.AuthorizationStatus(ctx, p.native)
	if err != nil || status != session.AuthorizationGranted {
		s.mu.Unlock()
		return noDispatch(c.FailureAuthorizationFailed), err
	}
	out, r, err := s.invoke(ctx, st, cat, p.call)
	s.mu.Unlock()
	return waitReceipt(ctx, out, r, err)
}
func (s *SessionAPI) disconnect(ctx context.Context, st *apiState) error {
	// Persist withdrawal before touching native grants. Even uncertain cleanup
	// cannot make public calls reach the old catalogue again.
	oldCustody := st.record.Custody
	st.record.Connected = false
	st.record.Withdrawing = true
	st.record.Connection = ""
	st.record.Account = [32]byte{}
	st.record.Anonymous = nil
	st.record.Catalogue = c.CatalogueRef(apiRef())
	if err := s.save(ctx, st); err != nil {
		return err
	}
	_ = s.empty(st)
	if st.enrollment != nil && st.enrollment.status.Kind == c.FlowPending {
		if _, err := st.attachment.CancelWorkspaceEnrollment(ctx, st.enrollment.native); err != nil {
			return err
		}
	}
	if oldCustody != nil {
		if err := s.process.TombstoneCredentialCustody(ctx, s.assertion(st)); err != nil && s.now().Before(oldCustody.ExpiresAt) {
			return err
		}
	}
	if _, err := s.process.DeleteSession(ctx, session.SessionID(st.record.Ref)); err != nil {
		return err
	}
	if st.attachment != nil {
		if _, err := st.attachment.Close(context.Background()); err != nil {
			return err
		}
	}
	st.attachment = nil
	st.record.Binding = ""
	st.record.Custody = nil
	st.record.Withdrawing = false
	st.enrollment = nil
	st.parked = make(map[c.AuthorizationRef]*apiParked)
	return s.save(ctx, st)
}
func (s *SessionAPI) DisconnectTools(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef) (c.DisconnectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		return 0, err
	}
	if !st.record.Connected && st.attachment == nil && st.enrollment == nil {
		return c.AlreadyDisconnected, nil
	}
	if cat != st.catalogue.Ref() {
		return c.CatalogueChanged, nil
	}
	if err = s.disconnect(ctx, st); err != nil {
		return 0, err
	}
	return c.Disconnected, nil
}
func (s *SessionAPI) DeleteSession(ctx context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(ctx, ref)
	if err != nil {
		if validAPIRef(string(ref)) {
			o, w, e := s.partitions(ctx)
			_ = o
			_ = w
			if e == nil && errors.Is(s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Err(), redis.Nil) {
				return c.AlreadyAbsent, nil
			}
		}
		return 0, err
	}
	if err = s.disconnect(ctx, st); err != nil {
		return 0, err
	}
	if err = s.redis.Del(ctx, sessionAPIPrefix+string(ref)).Err(); err != nil {
		return 0, err
	}
	delete(s.states, ref)
	return c.Deleted, nil
}
