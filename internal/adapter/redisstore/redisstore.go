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
//     forward-incompatible log fails loud on Read (an unknown tag is an error).
//
// No client-side mutex is needed: Redis serializes commands single-threaded and
// HSET/HGET/RPUSH are atomic, so the in-process sync.Mutex that jsonlstore
// carries is absent here. The adapter is validated by the SAME conformance
// suites as jsonlstore (storeconformance + eventlogconformance), exercised
// offline against an in-process miniredis so `task test` needs no live broker.
//
// DURABILITY CAVEAT: Append/Save call RPUSH/HSET synchronously and return only
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
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redis"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Key layout under a single Redis keyspace. The session id is the raw string
// after the sessionKeyPrefix (Redis keys are arbitrary strings, so no
// sanitization is needed — unlike jsonlstore's filename-safe safeName).
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
	Ev json.RawMessage `json:"ev"`
}

// compile-time assertions that Store satisfies all four ports it meets.
var (
	_ port.SessionStore         = (*Store)(nil)
	_ port.SessionCreator       = (*Store)(nil)
	_ port.ToolCallRecorder     = (*Store)(nil)
	_ port.PrunableStore        = (*Store)(nil)
	_ port.SessionMetadataPager = (*Store)(nil)
	_ port.EventLog             = (*Store)(nil)
)

// Store is the Redis-backed SessionStore + EventLog + PrunableStore +
// ToolCallRecorder. Every operation leases one replaceable client generation;
// the manager lock is held only for acquisition/publication, never Redis I/O.
type Store struct {
	clients *clientGenerations
	reload  *reloadLifecycle

	metadataWorkObserver        func(metadataWorkKind)
	migrationInspectionObserver func()
	migrationMutationObserver   func()
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
	Diagnostics    port.Diagnostics

	initialClientFactory func(context.Context, *tcredis.Config) (redis.UniversalClient, error)
	candidateFactory     func(context.Context, *tcredis.Config) (redis.UniversalClient, error)
	watcherFactory       watcherFactory
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
	if err := validateAddr(cfg.Addr); err != nil {
		return nil, err
	}
	conn, err := connectionConfig(cfg)
	if err != nil {
		return nil, err
	}
	factory := cfg.initialClientFactory
	if factory == nil {
		factory = func(ctx context.Context, conn *tcredis.Config) (redis.UniversalClient, error) {
			return tcredis.NewClient(ctx, conn)
		}
	}
	client, err := factory(context.Background(), &conn)
	if err != nil {
		// cfg.Addr is safe to name here: validateAddr has already rejected every
		// URL-shaped value that could carry a credential in its userinfo.
		return nil, fmt.Errorf("redisstore: connect %q: %w", cfg.Addr, err)
	}
	st := &Store{clients: newClientGenerations(client)}
	initCtx, release, err := st.pin(context.Background())
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := st.initializeMetadataIndex(initCtx); err != nil {
		release()
		_ = st.clients.close()
		return nil, err
	}
	release()
	if cfg.reloadEnabled() {
		lifecycle, err := startReloadLifecycle(st, cfg)
		if err != nil {
			_ = st.clients.close()
			return nil, err
		}
		st.reload = lifecycle
	}
	return st, nil
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

// connectionConfig folds this adapter's file-and-policy layer into the shared
// toolhive-core connection config.
func connectionConfig(cfg Config) (tcredis.Config, error) {
	verifiedTLS := cfg.TLS || cfg.CAFile != ""
	hasCredentials := cfg.UsernameFile != "" || cfg.PasswordFile != ""
	if !verifiedTLS && !hasCredentials {
		if !cfg.AllowPlaintext {
			return tcredis.Config{}, errors.New("redisstore: plaintext Redis requires explicit opt-in")
		}
		return tcredis.Config{Addr: cfg.Addr}, nil
	}
	if !verifiedTLS {
		return tcredis.Config{}, errors.New("redisstore: Redis credentials require verified TLS: enable system-trust TLS or supply a PEM CA bundle")
	}
	if cfg.UsernameFile != "" && cfg.PasswordFile == "" {
		return tcredis.Config{}, errors.New("redisstore: a Redis username requires a password")
	}
	files, err := readConnectionFiles(cfg)
	if err != nil {
		return tcredis.Config{}, err
	}
	username, password, err := credentialsFromFiles(cfg, files)
	if err != nil {
		return tcredis.Config{}, err
	}
	// A non-nil TLSConfig with a nil CACert means "verify against the system
	// trust store". An unparsable bundle is reported by the shared layer's
	// BuildTLSConfig, before any network I/O.
	tlsCfg := &tcredis.TLSConfig{}
	if cfg.CAFile != "" {
		tlsCfg.CACert = files[cfg.CAFile]
	}
	return tcredis.Config{Addr: cfg.Addr, Username: username, Password: password, TLS: tlsCfg}, nil
}

func readConnectionFiles(cfg Config) (map[string][]byte, error) {
	paths := cfg.reloadPaths()
	before := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, errors.New("redisstore: inspect configured Redis file")
		}
		before[path] = info
	}
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("redisstore: read configured Redis file")
		}
		files[path] = body
	}
	for _, path := range paths {
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before[path], after) {
			return nil, errors.New("redisstore: configured Redis files changed while being read")
		}
	}
	return files, nil
}

func credentialsFromFiles(cfg Config, files map[string][]byte) (string, string, error) {
	username, password := "", ""
	if cfg.UsernameFile != "" {
		username = secretFileValue(files[cfg.UsernameFile])
		if username == "" {
			return "", "", errors.New("redisstore: Redis username file is empty")
		}
	}
	if cfg.PasswordFile != "" {
		password = secretFileValue(files[cfg.PasswordFile])
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
	ctx, release, err := st.pin(ctx)
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
	if err := st.saveSnapshotAndMetadata(ctx, s, blob, modifiedAt); err != nil {
		return fmt.Errorf("redisstore: save %q: %w", s.ID, err)
	}
	return nil
}

// Create atomically publishes a snapshot and its derivative metadata only when
// no authoritative Redis session key exists for s.ID.
func (st *Store) Create(ctx context.Context, s *session.Session) error {
	ctx, release, err := st.pin(ctx)
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
	created, err := st.createSnapshotAndMetadata(ctx, s, blob, time.Now().UTC())
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
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	st.observeMetadataWork(metadataWorkLoad)
	blob, err := st.redis(ctx).HGet(ctx, sessionKey(id), fieldBlob).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, fmt.Errorf("redisstore: load %q: %w", id, err)
	}
	return sessnap.Unmarshal(blob)
}

// List returns every stored session's id and SAVE-time mtime. It SCANs the
// keyspace for session keys (MATCH mecatl:session:*), then HGETs the mtime
// field for each. The mtime is the value written at Save time, so two Lists
// with no intervening Save agree exactly (the stable-across-reads invariant).
// SCAN is cursor-based and non-blocking; a corrupt mtime field (absent or
// unparseable) is skipped best-effort rather than failing the whole inventory.
func (st *Store) List(ctx context.Context) ([]port.StoredSession, error) {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []port.StoredSession
	scan := st.redis(ctx).Scan(ctx, 0, sessionKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		key := scan.Val()
		id := strings.TrimPrefix(key, sessionKeyPrefix)
		if id == "" {
			continue
		}
		mtimeRaw, err := st.redis(ctx).HGet(ctx, key, fieldMtime).Result()
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

// PageSessionMetadata reads one owner-filtered keyset page from the derivative
// Redis metadata index. It never reads a snapshot blob or traverses rows before
// the cursor; legacy stores without a complete index report unsupported.
func (st *Store) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	defer release()
	return st.pageSessionMetadata(ctx, request)
}

// DeleteSessionIfUnchanged atomically compares the indexed durable row and
// removes the snapshot plus sidecars in one Redis script.
func (st *Store) DeleteSessionIfUnchanged(ctx context.Context, expected port.SessionDiscoveryMeta) (bool, error) {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	deleted, err := st.deleteSessionIfMetadataUnchanged(ctx, expected)
	if err != nil {
		return false, fmt.Errorf("redisstore: conditional delete: %w", err)
	}
	return deleted, nil
}

// Delete removes the session snapshot, derivative metadata, event log, and
// tool-call sidecar. It is idempotent and completes in one atomic script.
func (st *Store) Delete(ctx context.Context, id session.SessionID) error {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := st.deleteSessionAndMetadata(ctx, id); err != nil {
		return fmt.Errorf("redisstore: delete %q: %w", id, err)
	}
	return nil
}

// Append durably records ev under id as a format-tagged JSON record on the
// per-session event list (RPUSH). It satisfies port.EventLog. The event is
// marshalled to its session.Event JSON verbatim (already redacted at the relay)
// and wrapped in the {"v":"redisstore-eventlog/1","ev":...} envelope so Read
// can validate the format. RPUSH preserves append order, so Read returns
// events in the exact order Append received them.
func (st *Store) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return err
	}
	defer release()
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("redisstore: marshal event: %w", err)
	}
	rec, err := json.Marshal(eventLogRecord{V: EventLogFormat, Ev: evJSON})
	if err != nil {
		return fmt.Errorf("redisstore: marshal event record: %w", err)
	}
	if err := st.redis(ctx).RPush(ctx, eventsKey(id), rec).Err(); err != nil {
		return fmt.Errorf("redisstore: append event %q: %w", id, err)
	}
	return nil
}

// Read yields the session's recorded events in APPEND order (RPUSH order =
// LRANGE 0 -1 order). It satisfies port.EventLog. A MISS (no event key) yields
// an EMPTY sequence: absence is data, not an error. A genuine fault — an
// undecodable record, an unknown format tag, or a Redis error — is yielded as
// the error on a zero-value event and the consumer stops (the standard
// iter.Seq2 error idiom).
func (st *Store) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		ctx, release, err := st.pin(ctx)
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		defer release()
		records, err := st.redis(ctx).LRange(ctx, eventsKey(id), 0, -1).Result()
		if err != nil {
			// A missing event key is not an error in Redis (LRANGE on a missing
			// key returns an empty slice), so any error here is a genuine fault.
			yield(session.Event{}, fmt.Errorf("redisstore: read events %q: %w", id, err))
			return
		}
		for _, raw := range records {
			var rec eventLogRecord
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				yield(session.Event{}, fmt.Errorf("redisstore: decode event record: %w", err))
				return
			}
			if rec.V != EventLogFormat {
				yield(session.Event{}, fmt.Errorf("redisstore: unknown event-log format %q (want %q)", rec.V, EventLogFormat))
				return
			}
			var ev session.Event
			if err := json.Unmarshal(rec.Ev, &ev); err != nil {
				yield(session.Event{}, fmt.Errorf("redisstore: decode event: %w", err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
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
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return
	}
	defer release()
	_ = st.redis(ctx).RPush(ctx, toolsKey(id), line).Err()
}

// Ping checks the Redis broker is reachable. It is the readyz health probe a
// storage-free deployment (mecak8s) consults on /readyz: if Redis is down the
// endpoint controller removes the pod. It uses a short timeout so a stalled
// broker fails the probe quickly rather than wedging readiness.
func (st *Store) Ping(ctx context.Context) error {
	ctx, release, err := st.pin(ctx)
	if err != nil {
		return err
	}
	defer release()
	return st.redis(ctx).Ping(ctx).Err()
}

// Close stops credential reload before retiring the active Redis generation. It
// rejects new operations and swaps, waits for pinned operations and lock
// lifecycles to finish, and is safe to call repeatedly.
func (st *Store) Close() error {
	if st.reload != nil {
		st.reload.Close()
	}
	return st.clients.close()
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
