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

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
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
		if st.record.Connected || st.record.Withdrawing || st.record.Connection != "" {
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
