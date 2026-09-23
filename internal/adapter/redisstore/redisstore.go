// Package redisstore implements the Redis-backed port.SessionStore, port.EventLog,
// port.PrunableStore, and port.ToolCallRecorder for the cloud-native posture
// (ADR 0048, mecak8s). The agent pods are storage-free: session snapshots and
// the durable event log live in Redis as a managed service, and this adapter is
// the single store the server relay binds when the operator selects a Redis
// backend (a transport alternative to the local jsonlstore).
//
// It REUSES the existing storage formats — it is a TRANSPORT, not a format:
//   - snapshots are encoded with engine/adapter/sessnap (sessnap-json/1), shared
//     with jsonlstore and the gRPC driver; and
//   - the event-log record is the same {"v":<tag>,"ev":<json>} envelope shape as
//     jsonlstore, with its own format tag (redisstore-eventlog/1) so a
//     forward-incompatible log fails loud on Read (an unknown tag is an error);
//     a gap marker is a sibling tag on that same shape, never an event.
//
// No client-side mutex is needed: Redis serializes commands single-threaded and
// HSET/HGET/XADD are atomic, so the in-process sync.Mutex that jsonlstore
// carries is absent here. The adapter is validated by the SAME conformance
// suites as jsonlstore (storeconformance + eventlogconformance, including the
// cursor table), exercised offline against an in-process miniredis so
// `task test` needs no live broker.
//
// The event log is a STREAM, not a LIST (ADR 0250): XADD IDs are opaque,
// monotonic and durable, so they serve as cursors directly, and XREAD BLOCK is a
// cross-process blocking follow a LIST cannot express. Older Redis namespaces
// are not inspected or migrated.
//
// DURABILITY CAVEAT: Append/Save call XADD/HSET synchronously and return only
// once Redis acknowledges the command, but Redis's own persistence config
// (RDB snapshotting vs AOF fsync policy) determines durability-on-crash. An
// operator selecting this backend must configure Redis persistence to match
// their durability requirement; the adapter makes no durability claim beyond
// "Redis accepted the write".
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive-core/redisconn"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Key layout. The session id is the raw string after each entity prefix
// (Redis keys are arbitrary strings, so no sanitization is needed — unlike
// jsonlstore's filename-safe token).
const (
	sessionKeyPrefix = "mecatl:session:"
	eventsKeyPrefix  = "mecatl:events:"
	toolsKeyPrefix   = "mecatl:tools:"

	// hash fields on the session key.
	fieldBlob          = "blob"
	fieldMtime         = "mtime"
	fieldMetadataEntry = "metadata_entry"
	fieldMetadataOwner = "metadata_owner"
)

func keyPrefixExists(ctx context.Context, client redis.UniversalClient, prefix string) (bool, error) {
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+"*", 1).Result()
		if err != nil {
			return false, err
		}
		if len(keys) != 0 {
			return true, nil
		}
		if next == 0 {
			return false, nil
		}
		cursor = next
	}
}

// ErrNotFound is returned by Load when no snapshot exists for the id. It wraps
// port.ErrSessionNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is.
var ErrNotFound = fmt.Errorf("redisstore: session not found: %w", port.ErrSessionNotFound)

// EventLogFormat is the per-record format tag written on every event-log
// record. It versions the on-disk encoding so Read can reject an unknown tag as
// an infra error (a forward-incompatible log must fail loud, not silently skip).
const EventLogFormat = "redisstore-eventlog/1"

// eventLogRecord is one event-log entry: a format tag plus the verbatim
// session.Event JSON. The event is stored as already-redacted JSON (the relay
// is the redaction boundary); the tag lets Read validate the encoding version.
// It mirrors the jsonlstore envelope shape so the two stores are codec-siblings.
type eventLogRecord struct {
	V  string          `json:"v"`
	Ev json.RawMessage `json:"ev,omitempty"`
	// R is a gap marker's reason, set only when V is EventLogGapFormat. A gap is
	// an envelope variant rather than an event (ADR 0250 decision 5), so it
	// shares this record shape instead of becoming a session.Event. Omitted on
	// an ordinary event so the encoding of an event record is byte-unchanged
	// from before cursors existed.
	R string `json:"r,omitempty"`
}

// compile-time assertions that Store satisfies all four ports it meets.
var (
	_ port.SessionStore         = (*Store)(nil)
	_ port.SessionCreator       = (*Store)(nil)
	_ port.ToolCallRecorder     = (*Store)(nil)
	_ port.PrunableStore        = (*Store)(nil)
	_ port.SessionMetadataPager = (*Store)(nil)
	_ port.EventLog             = (*Store)(nil)
	_ port.SessionLineageReader = (*Store)(nil)
)

// Store is the Redis-backed SessionStore + EventLog + PrunableStore +
// ToolCallRecorder. Every operation leases one replaceable client generation;
// the manager lock is held only for acquisition/publication, never Redis I/O.
type Store struct {
	clients     *clientGenerations
	followers   *followerRegistry
	reload      *reloadLifecycle
	diagnostics port.Diagnostics
	closeGrace  time.Duration
	closeOnce   sync.Once

	metadataWorkObserver func(metadataWorkKind)
}

// Config configures a Redis connection using Kubernetes Secret-mounted files.
// Address-only configuration is intentionally supported for the disposable local
// and Kind path, and requires an explicit AllowPlaintext opt-in. Any credential
// requires VERIFIED TLS — either the host's system trust store (TLS) or an
// explicit PEM CA bundle (CAFile). Certificate verification is never disabled.
//
// Client-certificate (mTLS) authentication is NOT supported. The shared
// toolhive-core Redis layer this adapter delegates to exposes no
// client-certificate field; see ADR 0233 for the decision and the upstream
// tracking issue.
type Config struct {
	Addr         string
	UsernameFile string
	PasswordFile string
	// CAFile is a PEM CA bundle path. It REPLACES the system trust store, so a
	// managed service with a private CA needs it and one with a publicly-rooted
	// certificate does not.
	CAFile string
	// TLS enables verified TLS against the host's system trust store. CAFile
	// takes precedence when both are set.
	TLS            bool
	AllowPlaintext bool
	// FollowPoolSize is the hard connection bound for the isolated client used
	// only by ReadAfter calls with Follow set. Zero selects the default of 32.
	FollowPoolSize int
	// MaxFollowers is the process-local number of concurrently admitted follow
	// iterators. Zero selects the default of 32.
	MaxFollowers int
	Diagnostics  port.Diagnostics
}

// New connects to a plaintext, unauthenticated Redis broker. It is retained for
// local and Kind fixtures; production callers should use NewWithConfig with
// Secret-mounted credential files and verified TLS.
func New(addr string) (*Store, error) {
	return NewWithConfig(Config{Addr: addr, AllowPlaintext: true})
}

// NewWithConfig connects to Redis and pings it to fail fast. Secret values are
// read only from their mounted files and are never included in returned errors.
//
// Client construction, TLS assembly, dial/read/write timeout defaults, and the
// connectivity Ping are delegated to the shared toolhive-core Redis layer (ADR
// 0233). What stays here is the half that layer deliberately leaves to its
// callers: reading credentials from mounted files, and the policy that a
// credential implies verified TLS.
func NewWithConfig(cfg Config) (*Store, error) {
	return newWithConfig(cfg, defaultStoreDependencies())
}

func newWithConfig(cfg Config, deps storeDependencies) (*Store, error) {
	if err := validateAddr(cfg.Addr); err != nil {
		return nil, err
	}
	followPoolSize, maxFollowers, err := effectiveFollowLimits(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := connectionConfigWithReader(cfg, deps.readFile)
	if err != nil {
		return nil, err
	}
	pair, err := initialClientPair(context.Background(), &conn, followPoolSize, deps)
	if err != nil {
		// cfg.Addr is safe to name here: validateAddr has already rejected every
		// URL-shaped value that could carry a credential in its userinfo.
		return nil, fmt.Errorf("redisstore: connect %q: %w", cfg.Addr, err)
	}
	diagnostics := cfg.Diagnostics
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	st := &Store{
		clients: newClientGenerationsPair(pair), followers: newFollowerRegistry(maxFollowers), diagnostics: diagnostics,
		closeGrace: deps.closeGrace,
	}
	initClient, release, err := st.clients.acquire()
	if err != nil {
		closeClientPair(pair)
		return nil, err
	}
	if err := initializeMetadataIndex(context.Background(), initClient); err != nil {
		release()
		st.clients.close(deps.closeGrace)
		return nil, err
	}
	if err := initializeLineageIndex(context.Background(), initClient); err != nil {
		release()
		st.clients.close(deps.closeGrace)
		return nil, err
	}
	release()
	if cfg.reloadEnabled() {
		lifecycle, err := startReloadLifecycle(st, cfg, deps)
		if err != nil {
			st.clients.close(deps.closeGrace)
			return nil, err
		}
		st.reload = lifecycle
	}
	return st, nil
}

// NewClient builds a standalone redis.UniversalClient using the SAME
// connection policy (address, credential files, TLS) as NewWithConfig, minus
// the session-store scaffolding (metadata index, reload lifecycle, client
// generations). It is for a caller that needs its OWN Redis connection to the
// SAME managed instance — e.g. the bundled MCP-broker's embedded OAuth
// authorization server, which stores under a distinct key prefix — without
// reaching into this Store's internal, rotation-managed client. The caller
// owns the returned client's lifecycle (Close it when done).
//
// Credentials/CA material are read ONCE, here, at construction: unlike the
// main session-store client (NewWithConfig with cfg.reloadEnabled()), this
// client does NOT watch its credential/CA files and does NOT hot-reload after
// an ACL or CA rotation — go-redis reconnects using the SAME static
// Username/Password/TLS baked into redisconn.Config, which exposes no
// credentials-provider or reload hook. After a rotation, this client's
// reconnects fail with the stale material while /readyz (driven by the main
// store's reload-aware client) stays green, masking the failure as broker-only
// OAuth breakage. A rotation therefore requires restarting the process for
// this specific client. Document this limitation at every call site's own
// operator-facing docs rather than implying parity with NewWithConfig.
func NewClient(cfg Config) (redis.UniversalClient, error) {
	if err := validateAddr(cfg.Addr); err != nil {
		return nil, err
	}
	conn, err := connectionConfigWithReader(cfg, defaultStoreDependencies().readFile)
	if err != nil {
		return nil, err
	}
	client, err := redisconn.NewClient(context.Background(), &conn)
	if err != nil {
		return nil, fmt.Errorf("redisstore: connect %q: %w", cfg.Addr, err)
	}
	return client, nil
}

// validateAddr enforces host:port on EVERY path, secure and plaintext alike.
// Its errors never echo addr: an operator who passes a redis:// URL can embed a
// password in the userinfo, and this error reaches the diagnostics log.
func validateAddr(addr string) error {
	if addr == "" {
		return errors.New("redisstore: empty redis address")
	}
	if strings.ContainsAny(addr, "@/") {
		return errors.New("redisstore: redis address must be host:port, not a URL (no scheme, no embedded credentials)")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portStr == "" {
		return errors.New("redisstore: redis address must be host:port")
	}
	return nil
}

// connectionConfigWithReader folds this adapter's file-and-policy layer into the shared
// toolhive-core connection config. The reader seam keeps a descriptor read block testable.
func connectionConfigWithReader(cfg Config, readFile credentialFileReader) (redisconn.Config, error) {
	verifiedTLS := cfg.TLS || cfg.CAFile != ""
	hasCredentials := cfg.UsernameFile != "" || cfg.PasswordFile != ""
	if !verifiedTLS && !hasCredentials {
		if !cfg.AllowPlaintext {
			return redisconn.Config{}, errors.New("redisstore: plaintext Redis requires explicit opt-in")
		}
		return redisconn.Config{Addr: cfg.Addr}, nil
	}
	if !verifiedTLS {
		return redisconn.Config{}, errors.New("redisstore: Redis credentials require verified TLS: enable system-trust TLS or supply a PEM CA bundle")
	}
	if cfg.UsernameFile != "" && cfg.PasswordFile == "" {
		return redisconn.Config{}, errors.New("redisstore: a Redis username requires a password")
	}
	files, err := readConnectionFiles(cfg, readFile)
	if err != nil {
		return redisconn.Config{}, err
	}
	username, password, err := credentialsFromFiles(cfg, files)
	if err != nil {
		return redisconn.Config{}, err
	}
	// A non-nil TLSConfig with a nil CACert means "verify against the system
	// trust store". An unparsable bundle is reported by the shared layer's
	// BuildTLSConfig, before any network I/O.
	tlsCfg := &redisconn.TLSConfig{}
	if cfg.CAFile != "" {
		tlsCfg.CACert = files.ca
	}
	return redisconn.Config{Addr: cfg.Addr, Username: username, Password: password, TLS: tlsCfg}, nil
}

const defaultFollowCapacity = 32

func effectiveFollowLimits(cfg Config) (poolSize, maxFollowers int, err error) {
	poolSize = cfg.FollowPoolSize
	if poolSize == 0 {
		poolSize = defaultFollowCapacity
	}
	maxFollowers = cfg.MaxFollowers
	if maxFollowers == 0 {
		maxFollowers = defaultFollowCapacity
	}
	if poolSize < 1 {
		return 0, 0, errors.New("redisstore: follow pool size must be positive")
	}
	if maxFollowers < 1 || maxFollowers > poolSize {
		return 0, 0, errors.New("redisstore: max followers must be positive and no greater than follow pool size")
	}
	return poolSize, maxFollowers, nil
}

const maxCredentialFileSize int64 = 1 << 20

type credentialSnapshot struct {
	ca       []byte
	username []byte
	password []byte
}

type credentialFile struct {
	path   string
	before os.FileInfo
	body   []byte
}

func readConnectionFiles(cfg Config, readFile credentialFileReader) (credentialSnapshot, error) {
	paths := cfg.reloadPaths()
	files := make([]credentialFile, len(paths))
	for i, path := range paths {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return credentialSnapshot{}, errors.New("redisstore: open configured Redis file")
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return credentialSnapshot{}, errors.New("redisstore: inspect configured Redis file")
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return credentialSnapshot{}, errors.New("redisstore: configured Redis file is not regular")
		}
		if info.Size() > maxCredentialFileSize {
			_ = file.Close()
			return credentialSnapshot{}, errors.New("redisstore: configured Redis file is too large")
		}
		body, readErr := readFile(file, maxCredentialFileSize)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return credentialSnapshot{}, errors.New("redisstore: read configured Redis file")
		}
		if int64(len(body)) > maxCredentialFileSize {
			return credentialSnapshot{}, errors.New("redisstore: configured Redis file is too large")
		}
		files[i] = credentialFile{path: path, before: info, body: body}
	}
	for _, file := range files {
		after, err := os.Stat(file.path)
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(file.before, after) {
			return credentialSnapshot{}, errors.New("redisstore: configured Redis files changed while being read")
		}
	}
	var snapshot credentialSnapshot
	for _, file := range files {
		if file.path == cfg.CAFile {
			snapshot.ca = file.body
		}
		if file.path == cfg.UsernameFile {
			snapshot.username = file.body
		}
		if file.path == cfg.PasswordFile {
			snapshot.password = file.body
		}
	}
	return snapshot, nil
}

func credentialsFromFiles(cfg Config, files credentialSnapshot) (string, string, error) {
	username, password := "", ""
	if cfg.UsernameFile != "" {
		username = secretFileValue(files.username)
		if username == "" {
			return "", "", errors.New("redisstore: Redis username file is empty")
		}
	}
	if cfg.PasswordFile != "" {
		password = secretFileValue(files.password)
		if password == "" {
			return "", "", errors.New("redisstore: Redis password file is empty")
		}
	}
	return username, password, nil
}

func secretFileValue(body []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(body), "\r\n"), "\n")
}

// Save stores a sessnap-encoded snapshot of s under the session key, stamping
// the current time as the mtime field so List can report the SAVE time (not a
// fresh time.Now() at list time — the stable-mtime conformance invariant).
// HSET overwrites the blob field, so a second Save replaces the first (the
// overwrite contract).
func (st *Store) Save(ctx context.Context, s *session.Session) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return err
	}
	defer release()
	if s == nil {
		return sessnap.ErrNilSession
	}
	blob, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	modifiedAt := time.Now().UTC()
	if err := saveSnapshotAndMetadata(ctx, client, s, blob, modifiedAt); err != nil {
		return fmt.Errorf("redisstore: save %q: %w", s.ID, err)
	}
	return nil
}

// Create atomically publishes a snapshot and its derivative metadata only when
// no authoritative Redis session key exists for s.ID.
func (st *Store) Create(ctx context.Context, s *session.Session) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return err
	}
	defer release()
	if s == nil {
		return sessnap.ErrNilSession
	}
	blob, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	created, err := createSnapshotAndMetadata(ctx, client, s, blob, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("redisstore: create %q: %w", s.ID, err)
	}
	if !created {
		return fmt.Errorf("redisstore: create %q: %w", s.ID, port.ErrSessionAlreadyExists)
	}
	return nil
}

// Load reads the snapshot blob for id and restores it. A missing key (redis.Nil
// on HGET) wraps port.ErrSessionNotFound with the id in the message.
func (st *Store) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureStore, err)
	}
	defer release()
	st.observeMetadataWork(metadataWorkLoad)
	blob, err := client.HGet(ctx, sessionKey(id), fieldBlob).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureStore, fmt.Errorf("redisstore: load %q: %w", id, err))
	}
	sess, err := sessnap.Unmarshal(blob)
	if err != nil {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, err)
	}
	if sess.ID != id {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot,
			fmt.Errorf("redisstore: load %q: snapshot id mismatch: stored %q", id, sess.ID))
	}
	return sess, nil
}

// List returns every stored session's id and SAVE-time mtime. It SCANs the
// keyspace for current session keys, then HGETs the mtime
// field for each. The mtime is the value written at Save time, so two Lists
// with no intervening Save agree exactly (the stable-across-reads invariant).
// SCAN is cursor-based and non-blocking; a corrupt mtime field (absent or
// unparseable) is skipped best-effort rather than failing the whole inventory.
func (st *Store) List(ctx context.Context) ([]port.StoredSession, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	var out []port.StoredSession
	scan := client.Scan(ctx, 0, sessionKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		key := scan.Val()
		id := strings.TrimPrefix(key, sessionKeyPrefix)
		if id == "" {
			continue
		}
		mtimeRaw, err := client.HGet(ctx, key, fieldMtime).Result()
		if err != nil {
			// A key without an mtime field is a corrupt/partial entry; skip it
			// best-effort (Load of that id would fail the same way) rather than
			// fail the whole inventory.
			continue
		}
		ns, err := strconv.ParseInt(mtimeRaw, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, port.StoredSession{ID: session.SessionID(id), ModifiedAt: time.Unix(0, ns)})
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("redisstore: list: %w", err)
	}
	return out, nil
}

// SupportsSessionActivityProjection reports that the Redis metadata index
// atomically reflects the latest snapshot's activity.
func (*Store) SupportsSessionActivityProjection() bool { return true }

// PageSessionMetadata reads one owner-filtered keyset page from the derivative
// Redis metadata index. It never reads a snapshot blob or traverses rows before
// the cursor; legacy stores without a complete index report unsupported.
func (st *Store) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	defer release()
	return st.pageSessionMetadata(ctx, client, request)
}

// DeleteSessionIfUnchanged atomically compares the indexed durable row and
// removes the snapshot plus sidecars in one Redis script.
func (st *Store) DeleteSessionIfUnchanged(ctx context.Context, expected port.SessionDiscoveryMeta) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, err
	}
	defer release()
	deleted, err := deleteSessionIfMetadataUnchanged(ctx, client, expected)
	if err != nil {
		return false, fmt.Errorf("redisstore: conditional delete: %w", err)
	}
	return deleted, nil
}

// Delete removes the session snapshot, derivative metadata, event log, and
// tool-call sidecar. It is idempotent and completes in one atomic script.
func (st *Store) Delete(ctx context.Context, id session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return err
	}
	defer release()
	if err := deleteSessionAndMetadata(ctx, client, id); err != nil {
		return fmt.Errorf("redisstore: delete %q: %w", id, err)
	}
	return nil
}

// Append durably records ev under id as a format-tagged JSON record on the
// per-session event STREAM (XADD). It satisfies port.EventLog. The event is
// marshalled to its session.Event JSON verbatim (already redacted at the relay)
// and wrapped in the {"v":"redisstore-eventlog/1","ev":...} envelope so Read
// can validate the format. XADD preserves append order, so Read returns events
// in the exact order Append received them.
//
// It delegates to AppendEvent and drops the cursor. There is deliberately ONE
// write path: two would have to agree on the datatype, and the first append
// through the other one would meet a WRONGTYPE — the failure mode a "leave the
// old path alone" migration produces. A caller that wants the position calls
// AppendEvent; this signature exists for the shipped port.
func (st *Store) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	_, err := st.AppendEvent(ctx, id, ev)
	return err
}

// Read yields the session's recorded events in APPEND order. It satisfies
// port.EventLog. A MISS (no event key) yields an EMPTY sequence: absence is
// data, not an error. A genuine fault — an undecodable record, an unknown format
// tag, or a Redis error — is yielded as the error on a zero-value event and the
// consumer stops (the standard iter.Seq2 error idiom).
//
// It reads only the current STREAM representation. Unsupported old datatypes
// are rejected without mutation.
//
// GAP MARKERS ARE SKIPPED. This port's shipped contract is that it returns
// EVENTS, and a gap is a delivery envelope (ADR 0250 decision 5) that the
// event-sourced fold of ADR 0038 would choke on. Cursor readers see gaps via
// ReadAfter.
func (st *Store) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		client, release, err := st.clients.acquire()
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		defer release()

		raws, err := rawEventRecords(ctx, client, id)
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		for _, raw := range raws {
			rec, ok, err := decodeEventLogRecord(raw)
			if err != nil {
				yield(session.Event{}, err)
				return
			}
			if !ok || rec.Kind != port.LogRecordEvent {
				continue // a gap or a record carrying no event
			}
			if !yield(rec.Event, nil) {
				return
			}
		}
	}
}

// rawEventRecords returns the current STREAM's raw envelopes in append order.
func rawEventRecords(ctx context.Context, client redis.UniversalClient, id session.SessionID) ([][]byte, error) {
	key := eventsKey(id)
	kind, err := client.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: type of event key %q: %w", id, err)
	}
	if kind != "none" && kind != "stream" {
		return nil, fmt.Errorf("redisstore: unsupported old event log type %q for %q; data was left unchanged", kind, id)
	}
	// A missing key is not an error in Redis (XRANGE on a missing key returns an
	// empty slice), so any error here is a genuine fault.
	msgs, err := client.XRange(ctx, key, "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: read events %q: %w", id, err)
	}
	out := make([][]byte, 0, len(msgs))
	for _, msg := range msgs {
		raw, present := msg.Values[recordField]
		if !present {
			continue
		}
		text, isString := raw.(string)
		if !isString {
			return nil, fmt.Errorf("redisstore: event entry %s field %q has type %T, want string", msg.ID, recordField, raw)
		}
		out = append(out, []byte(text))
	}
	return out, nil
}

// toolCallRecord is the structured record written by ToolCall. It mirrors the
// jsonlstore tool-call audit record so the two stores produce the same audit
// shape for offline replay/analysis.
type toolCallRecord struct {
	Type         string             `json:"type"` // always "tool_call"
	Time         time.Time          `json:"time"`
	SessionID    session.SessionID  `json:"session_id"`
	CallID       session.ToolCallID `json:"call_id"`
	Tool         string             `json:"tool"`
	Args         json.RawMessage    `json:"args,omitempty"`
	Result       string             `json:"result"`
	IsError      bool               `json:"is_error"`
	QueuedMicros int64              `json:"queued_micros"`
	TookMicros   int64              `json:"took_micros"`
}

// ToolCall appends a structured tool-call record to the per-session tool list
// (RPUSH). It satisfies port.ToolCallRecorder. Errors are intentionally
// swallowed (the port has no error return); the record is best-effort durable.
func (st *Store) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	rec := toolCallRecord{
		Type:         "tool_call",
		Time:         time.Now().UTC(),
		SessionID:    id,
		CallID:       call.ID,
		Tool:         call.Name,
		Args:         call.Args,
		Result:       result.Content,
		IsError:      result.IsError,
		QueuedMicros: queued.Microseconds(),
		TookMicros:   took.Microseconds(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// Bound the audit write so a stalled Redis cannot wedge every audit call
	// on the go-redis default timeout; the recorder has no error return, so a
	// bounded ctx is the only way to keep a slow broker from piling up.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, release, err := st.clients.acquire()
	if err != nil {
		return
	}
	defer release()
	_ = client.RPush(ctx, toolsKey(id), line).Err()
}

// Ping checks the Redis broker is reachable. It is the readyz health probe a
// storage-free deployment (mecak8s) consults on /readyz: if Redis is down the
// endpoint controller removes the pod. It uses a short timeout so a stalled
// broker fails the probe quickly rather than wedging readiness.
func (st *Store) Ping(ctx context.Context) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return err
	}
	defer release()
	return client.Ping(ctx).Err()
}

// Close stops credential reload, rejects new work, and gives pinned operations a
// fixed grace interval to finish. Followers are store-owned: they are cancelled
// and joined, then only isolated follow clients are force-closed when a read
// ignores cancellation. Durability clients retain generation-lease semantics.
func (st *Store) Close() error {
	st.closeOnce.Do(func() {
		deadline := time.Now().Add(st.closeGrace)
		var followersDone <-chan struct{}
		if st.followers != nil {
			followersDone = st.followers.beginClose()
		}
		st.clients.beginClose()
		if st.reload != nil {
			st.reload.Close()
		}
		if followersDone != nil {
			firstWait := time.Until(deadline) / 2
			if !waitForRedisShutdown(followersDone, firstWait) {
				st.clients.forceCloseFollow()
				_ = waitForRedisShutdown(followersDone, time.Until(deadline))
			}
		}
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		if pending := st.clients.close(remaining); pending > 0 {
			st.diagnostics.Log(context.Background(), port.LevelWarn, "redis store shutdown",
				"component", "redis", "outcome", "timed_out", "reason", "active_operations", "count", pending)
		}
	})
	return nil
}

func waitForRedisShutdown(done <-chan struct{}, duration time.Duration) bool {
	if duration <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// ScheduleStore returns a port.ScheduleStore backed by the SAME Redis client as
// the session store (a sibling struct sharing the connection). Composition
// discovers it via type-assertion on this accessor — NOT by asserting the
// *Store itself implements port.ScheduleStore (the schedule store is a separate
// concern; the accessor keeps session-store and schedule-store methods from
// bloating one struct, the jsonlstore.ScheduleStore precedent — and the way
// PrunableStore is discovered on the store itself but here the schedule store is
// a sibling struct, not the session store). A caller that does not need
// schedules never calls this; the byte-identical default is no schedules.
//
// The schedule store carries NO client-side mutex: Redis serializes commands
// single-threaded, and the Claim path is a Lua CAS (EVAL) that is the
// cross-replica at-most-once fence — the multi-host counterpart of the
// single-process mutex the jsonl schedule store carries. See schedulestore.go.
func (st *Store) ScheduleStore() port.ScheduleStore {
	return &scheduleStore{clients: st.clients}
}

func sessionKey(id session.SessionID) string { return sessionKeyPrefix + string(id) }
func eventsKey(id session.SessionID) string  { return eventsKeyPrefix + string(id) }
func toolsKey(id session.SessionID) string   { return toolsKeyPrefix + string(id) }
