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
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const sessionAPIPrefix = "mecatl:poc:broker-session:v1:"

// SessionAPI owns the process-local coordination around durable session metadata.
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
	metadata *redis.Client
}

type apiRecord struct {
	WriteToken      string
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
	record         apiRecord
	attachment     *Attachment
	catalogue      c.Catalogue
	enrollment     *apiEnrollment
	parked         map[c.AuthorizationRef]*apiParked
	running        *apiReceipt
	pendingWrite   *apiRecord
	recovering     bool
	recoveryRecord *apiRecord

	// Mutable session data above is owned by ioGate. Registry snapshots and
	// cancellation below are protected by SessionAPI.mu.
	ioGate        chan struct{}
	generation    uint64
	invalidated   bool
	opCancel      context.CancelFunc
	snapshot      apiRecord
	loaded        bool
	deleted       bool
	users         int
	retained      bool
	enrollmentRef c.EnrollmentRef
}

type apiEnrollment struct {
	ref    c.EnrollmentRef
	native c.WorkspaceEnrollmentRef
	prompt c.BrowserPrompt
	status c.FlowStatus
}

type apiParked struct {
	attempt        c.BrokerAttempt
	call           c.Call
	native         session.ExternalAuthorization
	connection     string
	descriptor     [32]byte
	account        [32]byte
	completed      c.Catalogue
	terminal       *c.FlowStatus
	cleanupPending bool
}

type apiReceipt struct {
	done    chan struct{}
	outcome c.InvocationOutcome
}

var _ c.SessionService = (*SessionAPI)(nil)

// NewSessionAPI requires verified owner context and an independently verified
// workload resolver and single-node Redis options (including through wrappers).
func NewSessionAPI(p *Process, r redis.UniversalClient, workload func(context.Context) *session.Principal) (*SessionAPI, error) {
	if p == nil || r == nil || workload == nil {
		return nil, c.ErrStateUnavailable
	}
	if len(p.construction.protectedBackends) > 0 && p.custody == nil {
		return nil, c.ErrContinuityUnavailable
	}
	ctx, cancel := context.WithCancel(p.ctx)
	options, ok := r.(interface{ Options() *redis.Options })
	if !ok {
		cancel()
		return nil, c.ErrStateUnavailable
	}
	// Metadata writes must not retry: readback cannot fence a write still queued
	// on another Redis connection.
	var metadata *redis.Client
	if options.Options().MaxRetries != 0 {
		config := *options.Options()
		config.MaxRetries = -1
		metadata = redis.NewClient(&config)
	}
	return &SessionAPI{metadata: metadata, process: p, redis: r, workload: workload, owner: func(ctx context.Context) ([32]byte, error) {
		return c.ContinuityPrincipalPartition(c.ContinuityPartitionOwner, session.PrincipalFromContext(ctx))
	}, states: make(map[c.SessionRef]*apiState), ctx: ctx, cancel: cancel, now: time.Now}, nil
}

// NewProcessSessionAPI borrows native protected Redis when present, otherwise the
// caller-owned metadata client. Neither path changes native credential ownership.
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

func (s *SessionAPI) Ready(ctx context.Context) error {
	return s.redis.Ping(ctx).Err()
}

// Close drains process-local work without deleting durable session identity or custody.
// Close this facade before closing its borrowed Process and Redis client.
func (s *SessionAPI) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, st := range s.states {
		s.invalidateLocked(st)
	}
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
	s.mu.Lock()
	states := s.states
	s.states = make(map[c.SessionRef]*apiState)
	s.mu.Unlock()
	var err error
	for _, st := range states {
		if st.attachment != nil {
			_, e := st.attachment.Close(context.Background())
			err = errors.Join(err, e)
		}
		settleParkedAuthorizations(st, c.FailureAuthorityWithdrawn)
	}
	if s.metadata != nil {
		err = errors.Join(err, s.metadata.Close())
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
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
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
	if !s.validOperation(ctx) {
		return c.ErrStateUnavailable
	}
	if st.pendingWrite != nil {
		return c.ErrStateUnavailable
	}
	b, err := json.Marshal(st.record)
	if err != nil {
		return err
	}
	ttl := st.record.ExpiresAt.Sub(s.now())
	if ttl <= 0 {
		return c.ErrStateUnavailable
	}
	key := sessionAPIPrefix + string(st.record.Ref)
	if !s.validOperation(ctx) {
		return c.ErrStateUnavailable
	}
	writer := s.redis
	if s.metadata != nil {
		writer = s.metadata
	}
	if err = writer.Set(ctx, key, b, ttl).Err(); err == nil {
		return nil
	}
	// A lost write acknowledgement is not proof that adoption failed.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stored, readErr := s.redis.Get(checkCtx, key).Bytes()
	if readErr == nil && bytes.Equal(stored, b) {
		return nil
	}
	st.pendingWrite = &st.record
	return err
}

func (st *apiState) completeDisconnect() {
	st.attachment = nil
	st.recovering = false
	st.recoveryRecord = nil
	st.enrollment = nil
	st.parked = make(map[c.AuthorizationRef]*apiParked)
}

func (s *SessionAPI) emptyCatalogue(st *apiState) error {
	cat, err := c.NewCatalogue(st.record.Catalogue, c.ConnectionRef(st.record.Connection), nil)
	if err != nil {
		return err
	}
	st.catalogue = cat
	return nil
}

func (s *SessionAPI) OpenSession(ctx context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	if saved != nil {
		ctx, release, err := s.operation(ctx, *saved, apiControl{})
		if err != nil {
			return c.SessionSnapshot{}, err
		}
		defer release()
		st, err := s.metadataState(ctx, *saved)
		if err != nil {
			return c.SessionSnapshot{}, err
		}
		if st.record.Connected || st.record.Withdrawing {
			return c.SessionSnapshot{}, c.ErrStateUnavailable
		}
		if st.catalogue == nil {
			if err := s.emptyCatalogue(st); err != nil {
				return c.SessionSnapshot{}, c.ErrStateUnavailable
			}
		}
		return c.SessionSnapshot{Ref: st.record.Ref, ExpiresAt: st.record.ExpiresAt, Catalogue: st.catalogue}, nil
	}

	owner, workload, err := s.partitions(ctx)
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	ref := c.SessionRef(apiRef())
	record := apiRecord{
		Ref: ref, Owner: owner, Workload: workload, Profile: s.profile(),
		Incarnation: session.NewIncarnationID(), Catalogue: c.CatalogueRef(apiRef()),
		ExpiresAt: s.now().Add(30 * 24 * time.Hour),
	}
	st := newAPIState(record)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return c.SessionSnapshot{}, c.ErrStateUnavailable
	}
	if len(s.states) >= 32 {
		s.mu.Unlock()
		return c.SessionSnapshot{}, c.ErrCapacity
	}
	if s.states[ref] != nil {
		s.mu.Unlock()
		return c.SessionSnapshot{}, c.ErrStateUnavailable
	}
	s.states[ref] = st
	s.mu.Unlock()

	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	defer release()
	if err = s.emptyCatalogue(st); err == nil {
		err = s.saveRecord(ctx, st, st.record)
	}
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	s.mu.Lock()
	st.loaded = true
	s.mu.Unlock()
	return c.SessionSnapshot{Ref: st.record.Ref, ExpiresAt: st.record.ExpiresAt, Catalogue: st.catalogue}, nil
}

func (s *SessionAPI) DeleteSession(ctx context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	ctx, release, err := s.operation(ctx, ref, apiControl{delete: true})
	if err != nil {
		return 0, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		st = ctx.Value(apiOperationKey{}).(*apiOperation).state
		if st.pendingWrite != nil || !errors.Is(s.redis.Get(ctx, sessionAPIPrefix+string(ref)).Err(), redis.Nil) {
			return 0, err
		}
		if st.loaded {
			if _, err = s.process.DeleteSession(ctx, session.SessionID(ref)); err != nil {
				return 0, err
			}
			if st.attachment != nil {
				if _, err = st.attachment.Close(ctx); err != nil {
					return 0, err
				}
			}
		}
		settleParkedAuthorizations(st, c.FailureAuthorityWithdrawn)
		s.mu.Lock()
		st.deleted = true
		s.mu.Unlock()
		return c.AlreadyAbsent, nil
	}
	if err = s.controlGeneration(ctx, st); err != nil {
		return 0, err
	}
	if _, err = s.process.DeleteSession(ctx, session.SessionID(ref)); err != nil {
		return 0, err
	}
	if st.attachment != nil {
		if _, err = st.attachment.Close(ctx); err != nil {
			return 0, err
		}
	}
	settleParkedAuthorizations(st, c.FailureAuthorityWithdrawn)
	if !s.validOperation(ctx) || st.pendingWrite != nil {
		return 0, c.ErrStateUnavailable
	}
	if err = s.redis.Del(ctx, sessionAPIPrefix+string(ref)).Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	st.deleted = true
	s.mu.Unlock()
	return c.Deleted, nil
}

func noDispatch(reason c.FailureReason) c.InvocationOutcome {
	return c.InvocationOutcome{Kind: c.InvocationNotDispatched, Reason: reason}
}

func find(cat c.Catalogue, name string) tool.Tool {
	for _, candidate := range cat.Tools() {
		if candidate.Spec().Name == name {
			return candidate
		}
	}
	return nil
}

func callDigest(call c.Call) [32]byte {
	return session.BrokerCallDigest(session.ToolCall{ID: call.ID, Name: call.Name, Args: call.Arguments})
}

func sameCall(left, right c.Call) bool {
	return left.ID == right.ID && left.Name == right.Name && bytes.Equal(left.Arguments, right.Arguments)
}

func apiToolDigest(candidate tool.Tool) [32]byte {
	if candidate == nil {
		return [32]byte{}
	}
	spec := candidate.Spec()
	if advertised, ok := candidate.(tool.Disclosable); ok {
		spec = advertised.Advertised()
	}
	_, serial := candidate.(tool.DispatchSerial)
	_, authorization := candidate.(tool.AuthorizationRequester)
	encoded, _ := json.Marshal(struct {
		Spec           tool.ToolSpec
		ReadOnly       bool
		DispatchSerial bool
		Authorization  bool
	}{Spec: spec, ReadOnly: candidate.ReadOnly(), DispatchSerial: serial, Authorization: authorization})
	return sha256.Sum256(encoded)
}

func validCall(call c.Call) bool {
	return len(call.ID) > 0 && len(call.ID) <= 256 && utf8.ValidString(string(call.ID)) && len(call.Name) > 0 && len(call.Name) <= 256 && utf8.ValidString(call.Name) && len(call.Arguments) > 0 && len(call.Arguments) <= 256*1024 && json.Valid(call.Arguments)
}

func (s *SessionAPI) nativePreflight(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, *apiReceipt, error) {
	if err := ctx.Err(); err != nil {
		return c.InvocationOutcome{}, nil, err
	}
	if cat != st.catalogue.Ref() || cat != st.record.Catalogue || find(st.catalogue, call.Name) == nil {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	requester, ok := find(st.catalogue, call.Name).(tool.AuthorizationRequester)
	if !ok {
		return c.InvocationOutcome{}, nil, nil
	}
	native := session.ToolCall{ID: call.ID, Name: call.Name, Args: append([]byte(nil), call.Arguments...)}
	authorization, required, err := requester.RequestAuthorization(ctx, native)
	if err != nil {
		return noDispatch(c.FailureAuthorizationFailed), nil, nil
	}
	if !required {
		if authorization != (session.ExternalAuthorization{}) {
			if err := requester.AbortAuthorization(ctx, authorization); err != nil {
				return c.InvocationOutcome{}, nil, err
			}
		}
		return c.InvocationOutcome{}, nil, nil
	}
	if !validAPIRef(authorization.ID) || authorization.Binding == "" || len(authorization.Binding) > 256 || !utf8.ValidString(string(authorization.Binding)) || (authorization.DisplayName != "" && (!utf8.ValidString(authorization.DisplayName) || len(authorization.DisplayName) > 256)) || authorization.ExpiresAt.IsZero() || !authorization.ExpiresAt.After(s.now()) || !s.validOperation(ctx) {
		if abortErr := requester.AbortAuthorization(ctx, authorization); abortErr != nil {
			return c.InvocationOutcome{}, nil, abortErr
		}
		return noDispatch(c.FailureAuthorizationFailed), nil, nil
	}
	ref := c.AuthorizationRef(apiRef())
	target := find(st.catalogue, call.Name)
	st.parked[ref] = &apiParked{
		attempt: attempt, call: c.Call{ID: call.ID, Name: call.Name, Arguments: append([]byte(nil), call.Arguments...)},
		native: authorization, connection: st.record.Connection, descriptor: apiToolDigest(target), account: st.record.Account,
	}
	return c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: ref}, nil, nil
}

func (s *SessionAPI) dispatchInvocation(ctx context.Context, st *apiState, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, *apiReceipt, error) {
	if err := ctx.Err(); err != nil {
		return c.InvocationOutcome{}, nil, err
	}
	if !receiptFinished(st.running) {
		return noDispatch(c.FailureCapacity), nil, nil
	}
	if cat != st.record.Catalogue || !st.record.Connected || st.catalogue == nil || cat != st.catalogue.Ref() {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	t := find(st.catalogue, call.Name)
	if t == nil || st.attachment == nil {
		return noDispatch(c.FailureCatalogueChanged), nil, nil
	}
	native := session.ToolCall{ID: call.ID, Name: call.Name, Args: append([]byte(nil), call.Arguments...)}
	durableCall := native
	filter := ""
	if call.Name == "CallMcpWithQuery" {
		var err error
		durableCall, filter, err = (&attachmentQueryTool{}).target(native)
		if err != nil {
			return noDispatch(c.FailureCatalogueChanged), nil, nil
		}
	}
	if route, ok := st.attachment.lookupRoute(durableCall.Name); s.process.custody != nil && ok && route.broker {
		st.attachment.mu.RLock()
		tsid := st.attachment.verifiedTSID
		st.attachment.mu.RUnlock()
		ready, readinessErr := s.process.nativeGrantReady(ctx, tsid)
		account, accountErr := s.process.nativeAccount(ctx, tsid)
		if readinessErr != nil || accountErr != nil || !ready || account != st.record.Account || account == ([32]byte{}) {
			return noDispatch(c.FailureAuthorityWithdrawn), nil, nil
		}
	}
	ctx = context.WithValue(ctx, durableNativeKey{}, &durableNativeCall{attachment: st.attachment, hash: callHash(durableCall), filter: filter})
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	s.mu.Lock()
	if s.closed || !s.workerGeneration(ctx, st) {
		s.mu.Unlock()
		cancel()
		stop()
		return c.InvocationOutcome{}, nil, c.ErrStateUnavailable
	}
	s.workers.Add(1)
	st.users++
	s.mu.Unlock()
	receipt := &apiReceipt{done: make(chan struct{})}
	st.running = receipt
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: string(st.record.Ref), Revision: string(cat)}, nofs.New(), memledger.New(), nil)
	go func() {
		defer s.finishOwnership(st)
		defer stop()
		defer cancel()
		out := c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
		defer func() {
			if recover() != nil {
				out = c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
			}
			s.mu.Lock()
			if s.closed || !s.workerGeneration(runCtx, st) {
				out = c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
			}
			receipt.outcome = out
			close(receipt.done)
			s.mu.Unlock()
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

func (s *SessionAPI) InvokeTool(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, error) {
	caller := ctx
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.InvocationOutcome{}, err
	}
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		release()
		return c.InvocationOutcome{}, err
	}
	out, r, err := s.prepareInvocation(ctx, st, cat, call, attempt)
	if err == nil && r == nil && out.Kind == "" {
		out, r, err = s.dispatchInvocation(ctx, st, cat, call, attempt)
	}
	release()
	return waitReceipt(caller, out, r, err)
}

func (s *SessionAPI) parkedAuthorization(st *apiState, ref c.AuthorizationRef, attempt c.BrokerAttempt) (*apiParked, error) {
	if !validAPIRef(string(ref)) || !attempt.Valid() {
		return nil, c.ErrStateUnavailable
	}
	parked := st.parked[ref]
	if parked == nil || parked.attempt != attempt {
		return nil, c.ErrStateUnavailable
	}
	return parked, nil
}

func settleParkedAuthorizations(st *apiState, reason c.FailureReason) {
	for _, parked := range st.parked {
		flow := c.FlowStatus{Kind: c.FlowFailed, Reason: reason}
		parked.terminal = &flow
		parked.call.Arguments = nil
		parked.native = session.ExternalAuthorization{}
		parked.completed = nil
		parked.cleanupPending = false
	}
}

func (s *SessionAPI) parkedSnapshotValid(st *apiState, parked *apiParked) bool {
	return st.record.Connected && !st.record.Withdrawing && st.attachment != nil && st.catalogue != nil &&
		st.record.Connection == parked.connection && st.record.Catalogue == st.catalogue.Ref() &&
		apiToolDigest(find(st.catalogue, parked.call.Name)) == parked.descriptor && st.record.Account == parked.account
}

func (s *SessionAPI) parkedAuthority(ctx context.Context, st *apiState, parked *apiParked) c.FailureReason {
	if !s.parkedSnapshotValid(st, parked) {
		return c.FailureCatalogueChanged
	}
	targetCall := session.ToolCall{ID: parked.call.ID, Name: parked.call.Name, Args: append([]byte(nil), parked.call.Arguments...)}
	if targetCall.Name == "CallMcpWithQuery" {
		var err error
		targetCall, _, err = (&attachmentQueryTool{}).target(targetCall)
		if err != nil {
			return c.FailureCallChanged
		}
	}
	if route, ok := st.attachment.lookupRoute(targetCall.Name); s.process.custody != nil && ok && route.broker {
		st.attachment.mu.RLock()
		tsid := st.attachment.verifiedTSID
		st.attachment.mu.RUnlock()
		ready, readyErr := s.process.nativeGrantReady(ctx, tsid)
		account, accountErr := s.process.nativeAccount(ctx, tsid)
		if readyErr != nil || accountErr != nil || !ready || account == ([32]byte{}) || account != parked.account {
			return c.FailureAuthorityWithdrawn
		}
	}
	return c.FailureUnspecified
}

func (s *SessionAPI) authorizationStatus(ctx context.Context, st *apiState, parked *apiParked) (c.FlowStatus, error) {
	if parked.terminal != nil {
		return *parked.terminal, nil
	}
	if !s.now().Before(parked.native.ExpiresAt) {
		if _, err := st.attachment.CancelAuthorization(ctx, parked.native); err != nil {
			parked.cleanupPending = true
			return c.FlowStatus{}, err
		}
		status := c.FlowStatus{Kind: c.FlowExpired}
		parked.terminal = &status
		parked.call.Arguments = nil
		parked.native = session.ExternalAuthorization{}
		return status, nil
	}
	status, err := st.attachment.AuthorizationStatus(ctx, parked.native)
	if err != nil {
		return c.FlowStatus{}, err
	}
	var flow c.FlowStatus
	switch status {
	case session.AuthorizationPending:
		flow.Kind = c.FlowPending
	case session.AuthorizationGranted:
		if reason := s.parkedAuthority(ctx, st, parked); reason != c.FailureUnspecified {
			flow = c.FlowStatus{Kind: c.FlowFailed, Reason: reason}
		} else {
			parked.completed = st.catalogue
			flow = c.FlowStatus{Kind: c.FlowCompleted, Catalogue: st.catalogue}
		}
	case session.AuthorizationCancelled:
		flow.Kind = c.FlowCancelled
	case session.AuthorizationExpired:
		flow.Kind = c.FlowExpired
	default:
		flow = c.FlowStatus{Kind: c.FlowFailed, Reason: c.FailureAuthorizationFailed}
	}
	if !flow.Valid() {
		return c.FlowStatus{}, c.ErrStateUnavailable
	}
	if flow.Kind != c.FlowPending {
		parked.terminal = &flow
		parked.native = session.ExternalAuthorization{}
		if flow.Kind != c.FlowCompleted {
			parked.call.Arguments = nil
		}
	}
	return flow, nil
}

func (s *SessionAPI) CheckAuthorization(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call *c.Call, authorization c.AuthorizationRef, attempt c.BrokerAttempt) (c.AuthorizationCheck, error) {
	if !attempt.Valid() || !validAPIRef(string(cat)) || (call == nil) == (authorization == "") {
		return c.AuthorizationCheck{}, c.ErrStateUnavailable
	}
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	if cat != st.record.Catalogue || st.catalogue == nil || cat != st.catalogue.Ref() {
		return c.AuthorizationCheck{Reason: c.FailureCatalogueChanged}, nil
	}
	if call != nil {
		out, _, err := s.prepareInvocation(ctx, st, cat, *call, attempt)
		if err != nil {
			return c.AuthorizationCheck{}, err
		}
		switch out.Kind {
		case "":
			return c.AuthorizationCheck{Ready: true}, nil
		case c.InvocationAuthorizationRequired:
			parked, err := s.parkedAuthorization(st, out.Authorization, attempt)
			if err != nil {
				return c.AuthorizationCheck{}, err
			}
			return c.AuthorizationCheck{Authorization: out.Authorization, ExpiresAt: parked.native.ExpiresAt}, nil
		case c.InvocationNotDispatched:
			return c.AuthorizationCheck{Reason: out.Reason}, nil
		default:
			return c.AuthorizationCheck{}, c.ErrStateUnavailable
		}
	}
	parked, err := s.parkedAuthorization(st, authorization, attempt)
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	flow, err := s.authorizationStatus(ctx, st, parked)
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	switch flow.Kind {
	case c.FlowPending:
		return c.AuthorizationCheck{Authorization: authorization, ExpiresAt: parked.native.ExpiresAt}, nil
	case c.FlowCompleted:
		return c.AuthorizationCheck{Ready: true}, nil
	case c.FlowExpired:
		return c.AuthorizationCheck{Reason: c.FailureExpired}, nil
	case c.FlowCancelled, c.FlowFailed:
		reason := flow.Reason
		if !reason.Valid() {
			reason = c.FailureAuthorizationFailed
		}
		return c.AuthorizationCheck{Reason: reason}, nil
	default:
		return c.AuthorizationCheck{}, c.ErrStateUnavailable
	}
}

func (s *SessionAPI) BeginAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef) (c.BrowserPrompt, error) {
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	parked := st.parked[authorization]
	if parked == nil || parked.terminal != nil || !s.parkedSnapshotValid(st, parked) {
		return c.BrowserPrompt{}, c.ErrStateUnavailable
	}
	url, err := st.attachment.PresentAuthorization(ctx, parked.native)
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	prompt := c.BrowserPrompt{URL: url, ExpiresAt: parked.native.ExpiresAt}
	if !prompt.Valid() || !prompt.ExpiresAt.After(s.now()) {
		return c.BrowserPrompt{}, c.ErrStateUnavailable
	}
	return prompt, nil
}

func (s *SessionAPI) ObserveAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef) (c.FlowStatus, error) {
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.FlowStatus{}, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.FlowStatus{}, err
	}
	parked := st.parked[authorization]
	if parked == nil {
		return c.FlowStatus{}, c.ErrStateUnavailable
	}
	return s.authorizationStatus(ctx, st, parked)
}

func (s *SessionAPI) CancelAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef, attempt c.BrokerAttempt) (c.CancelResult, error) {
	if !attempt.Valid() {
		return 0, c.ErrStateUnavailable
	}
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return 0, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return 0, err
	}
	parked, err := s.parkedAuthorization(st, authorization, attempt)
	if err != nil {
		return 0, err
	}
	flow, err := s.authorizationStatus(ctx, st, parked)
	if err != nil {
		return 0, err
	}
	if flow.Kind != c.FlowPending {
		return c.AlreadyResolved, nil
	}
	outcome, err := st.attachment.CancelAuthorization(ctx, parked.native)
	if err != nil {
		parked.cleanupPending = true
		return 0, err
	}
	parked.cleanupPending = false
	if outcome == contract.CancelCancelled {
		status := c.FlowStatus{Kind: c.FlowCancelled}
		parked.terminal = &status
		parked.call.Arguments = nil
		parked.native = session.ExternalAuthorization{}
		return c.Cancelled, nil
	}
	status, err := s.authorizationStatus(ctx, st, parked)
	if err != nil {
		return 0, err
	}
	if status.Kind == c.FlowPending {
		return 0, c.ErrStateUnavailable
	}
	return c.AlreadyResolved, nil
}

func (s *SessionAPI) ResumeTool(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef, adopted c.CatalogueRef, attempt c.BrokerAttempt) (c.InvocationOutcome, error) {
	caller := ctx
	if !attempt.Valid() || !validAPIRef(string(adopted)) {
		return c.InvocationOutcome{}, c.ErrStateUnavailable
	}
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.InvocationOutcome{}, err
	}
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		release()
		return c.InvocationOutcome{}, err
	}
	parked, err := s.parkedAuthorization(st, authorization, attempt)
	if err != nil {
		release()
		return c.InvocationOutcome{}, err
	}
	flow, err := s.authorizationStatus(ctx, st, parked)
	if err != nil {
		release()
		return c.InvocationOutcome{}, err
	}
	if flow.Kind != c.FlowCompleted {
		reason := c.FailureCapacity
		switch flow.Kind {
		case c.FlowExpired:
			reason = c.FailureExpired
		case c.FlowCancelled:
			reason = c.FailureAuthorizationFailed
		case c.FlowFailed:
			reason = flow.Reason
			if !reason.Valid() {
				reason = c.FailureAuthorizationFailed
			}
		}
		release()
		return noDispatch(reason), nil
	}
	if parked.completed == nil || adopted != st.catalogue.Ref() || adopted != parked.completed.Ref() {
		release()
		return noDispatch(c.FailureCatalogueChanged), nil
	}
	if reason := s.parkedAuthority(ctx, st, parked); reason != c.FailureUnspecified {
		release()
		return noDispatch(reason), nil
	}
	call := c.Call{ID: parked.call.ID, Name: parked.call.Name, Arguments: append([]byte(nil), parked.call.Arguments...)}
	delete(st.parked, authorization)
	out, receipt, err := s.dispatchInvocation(ctx, st, adopted, call, attempt)
	if err != nil || receipt == nil {
		release()
		return out, err
	}
	release()
	return waitReceipt(caller, out, receipt, nil)
}
