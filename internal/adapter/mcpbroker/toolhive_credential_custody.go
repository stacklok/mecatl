package mcpbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	custodySchemaVersion = 1
	custodyPrefix        = toolHiveAuthStoragePrefix + "custody:"
	maxCustodyTSID       = 4 << 10
	maxCustodyPlaintext  = 64 << 10
	custodyRetries       = 8
)

var (
	errCustodyUnavailable = errors.New("mcpbroker: credential custody unavailable")
	errCustodyConflict    = errors.New("mcpbroker: credential custody conflict")
	errCustodyNotFound    = errors.New("mcpbroker: credential custody record absent")
)

type recoveryID string

type custodyGuard struct {
	SessionID         session.SessionID
	Incarnation       session.IncarnationID
	OwnerPartition    [32]byte
	WorkloadPartition [32]byte
	ProfileDigest     [32]byte
	Providers         []string
}

// custodyRequest is produced by the trusted adapter boundary. The core neither
// authenticates callers nor attempts to derive this assertion from persistence.
type custodyRequest struct {
	Guard           custodyGuard
	AttemptDeadline time.Time
}

type custodyAssertion struct {
	custodyRequest
	Recovery recoveryID
}

type custodyRetention struct{ ExpiresAt time.Time }

type custodyState string

const (
	custodyStaged     custodyState = "staged"
	custodyCurrent    custodyState = "current"
	custodyTombstoned custodyState = "tombstoned"
)

type custodyRecord struct {
	Version   uint32
	ID        recoveryID
	Guard     custodyGuard
	TSID      string
	ExpiresAt time.Time
	Revision  uint64
	State     custodyState
}

type upstreamTokenRowReader interface {
	GetUpstreamTokens(context.Context, string, string) (*storage.UpstreamTokens, error)
}

type stagedCustody struct {
	Recovery  recoveryID
	ExpiresAt time.Time
}

type credentialCustody struct {
	client redis.UniversalClient
	keys   *credentialKeyRing
	rows   upstreamTokenRowReader
	tokens *upstreamtoken.InProcessService
	clock  port.Clock
}

func newCredentialCustody(client redis.UniversalClient, keys *credentialKeyRing, tokens *upstreamtoken.InProcessService, clock port.Clock, rows upstreamTokenRowReader) (*credentialCustody, error) {
	if client == nil || keys == nil || tokens == nil || clock == nil || rows == nil {
		return nil, errCustodyUnavailable
	}
	return &credentialCustody{client: client, keys: keys, rows: rows, tokens: tokens, clock: clock}, nil
}

//nolint:gocyclo // Stage is one fail-closed native-expiry transaction.
func (c *credentialCustody) Stage(ctx context.Context, request custodyRequest, verifiedTSID string) (stagedCustody, error) {
	if ctx.Err() != nil {
		return stagedCustody{}, ctx.Err()
	}
	if c.validRequest(request) != nil || len(verifiedTSID) == 0 || len(verifiedTSID) > maxCustodyTSID || !utf8.ValidString(verifiedTSID) {
		return stagedCustody{}, errCustodyUnavailable
	}
	retention := custodyRetention{}
	rows := make(map[string]*storage.UpstreamTokens, len(request.Guard.Providers))
	for _, provider := range request.Guard.Providers {
		row, err := c.rows.GetUpstreamTokens(ctx, verifiedTSID, provider)
		if err != nil && !errors.Is(err, storage.ErrExpired) {
			return stagedCustody{}, custodyError(ctx, err)
		}
		if row == nil || row.ProviderID != provider || !validFuture(row.SessionExpiresAt, c.clock.Now()) {
			return stagedCustody{}, errCustodyUnavailable
		}
		rows[provider] = row
		if retention.ExpiresAt.IsZero() || row.SessionExpiresAt.Before(retention.ExpiresAt) {
			retention.ExpiresAt = row.SessionExpiresAt
		}
	}
	for _, provider := range request.Guard.Providers {
		credential, err := c.tokens.GetValidTokens(ctx, verifiedTSID, provider)
		if err != nil || credential == nil {
			return stagedCustody{}, custodyError(ctx, err)
		}
	}
	for _, provider := range request.Guard.Providers {
		row, err := c.rows.GetUpstreamTokens(ctx, verifiedTSID, provider)
		if (err != nil && !errors.Is(err, storage.ErrExpired)) || row == nil || row.ProviderID != provider || !validFuture(row.SessionExpiresAt, c.clock.Now()) {
			return stagedCustody{}, errCustodyUnavailable
		}
		if !row.SessionExpiresAt.Equal(rows[provider].SessionExpiresAt) {
			return stagedCustody{}, errCustodyUnavailable
		}
	}
	if !validFuture(retention.ExpiresAt, c.clock.Now()) || request.AttemptDeadline.After(retention.ExpiresAt) {
		return stagedCustody{}, errCustodyUnavailable
	}
	ref, err := newRecoveryID()
	if err != nil {
		return stagedCustody{}, errCustodyUnavailable
	}
	record := custodyRecord{Version: custodySchemaVersion, ID: ref, Guard: cloneGuard(request.Guard), TSID: verifiedTSID, ExpiresAt: retention.ExpiresAt.UTC(), Revision: 1, State: custodyStaged}
	sealed, err := c.sealRecord(record)
	if err != nil {
		return stagedCustody{}, errCustodyUnavailable
	}
	if c.validRequest(request) != nil || !validFuture(retention.ExpiresAt, c.clock.Now()) {
		return stagedCustody{}, errCustodyUnavailable
	}
	result, err := custodyCreateScript.Run(ctx, c.client, []string{c.key(ref)}, sealed, record.ExpiresAt.UnixMilli(), request.AttemptDeadline.UnixMilli()).Int()
	if err != nil {
		return stagedCustody{}, custodyError(ctx, err)
	}
	if result == -1 {
		return stagedCustody{}, errCustodyUnavailable
	}
	if result != 1 {
		return stagedCustody{}, errCustodyConflict
	}
	return stagedCustody{Recovery: ref, ExpiresAt: record.ExpiresAt}, nil
}

func (c *credentialCustody) Commit(ctx context.Context, assertion custodyAssertion) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.validAssertion(assertion) != nil {
		return errCustodyUnavailable
	}
	for attempts := 0; attempts < custodyRetries; attempts++ {
		record, raw, err := c.readRecord(ctx, assertion.Recovery)
		if err != nil {
			return custodyError(ctx, err)
		}
		if !c.matches(assertion, record) {
			return errCustodyUnavailable
		}
		switch record.State {
		case custodyCurrent:
			return nil
		case custodyTombstoned:
			return errCustodyConflict
		case custodyStaged:
		default:
			return errCustodyUnavailable
		}
		if record.Revision == ^uint64(0) {
			return errCustodyUnavailable
		}
		record.State, record.Revision = custodyCurrent, record.Revision+1
		next, err := c.sealRecord(record)
		if err != nil {
			return errCustodyUnavailable
		}
		ok, err := c.replace(ctx, assertion.Recovery, raw, next, record.ExpiresAt, assertion.AttemptDeadline)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errCustodyConflict
}

func (c *credentialCustody) Load(ctx context.Context, assertion custodyAssertion) (custodyRecord, error) {
	if c.validAssertion(assertion) != nil {
		return custodyRecord{}, errCustodyUnavailable
	}
	record, _, err := c.readRecord(ctx, assertion.Recovery)
	if err != nil {
		return custodyRecord{}, custodyError(ctx, err)
	}
	if record.State != custodyCurrent || !c.matches(assertion, record) {
		return custodyRecord{}, errCustodyUnavailable
	}
	return record, nil
}

func (c *credentialCustody) LoadCurrent(ctx context.Context, ref recoveryID, guard custodyGuard) (custodyRecord, error) {
	if validateGuard(guard) != nil || !validRecoveryID(ref) {
		return custodyRecord{}, errCustodyUnavailable
	}
	record, _, err := c.readRecord(ctx, ref)
	if err != nil {
		return custodyRecord{}, custodyError(ctx, err)
	}
	if record.State != custodyCurrent || !sameGuard(record.Guard, guard) || !record.ExpiresAt.After(c.clock.Now()) {
		return custodyRecord{}, errCustodyUnavailable
	}
	return record, nil
}

func (c *credentialCustody) Tombstone(ctx context.Context, request custodyRequest, ref recoveryID) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.validRequest(request) != nil || !validRecoveryID(ref) {
		return errCustodyUnavailable
	}
	for attempts := 0; attempts < custodyRetries; attempts++ {
		record, raw, err := c.readRecord(ctx, ref)
		if errors.Is(err, errCustodyNotFound) {
			return nil
		}
		if err != nil {
			return custodyError(ctx, err)
		}
		if !sameGuard(record.Guard, request.Guard) || !record.ExpiresAt.After(c.clock.Now()) || request.AttemptDeadline.After(record.ExpiresAt) {
			return errCustodyUnavailable
		}
		if record.State == custodyTombstoned {
			return nil
		}
		if record.Revision == ^uint64(0) {
			return errCustodyUnavailable
		}
		record.State, record.Revision, record.TSID = custodyTombstoned, record.Revision+1, ""
		next, err := c.sealRecord(record)
		if err != nil {
			return errCustodyUnavailable
		}
		ok, err := c.replace(ctx, ref, raw, next, record.ExpiresAt, request.AttemptDeadline)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errCustodyConflict
}

func (c *credentialCustody) Resolve(ctx context.Context, assertion custodyAssertion, provider string) (*upstreamtoken.UpstreamCredential, error) {
	if c.validAssertion(assertion) != nil || !containsProvider(assertion.Guard.Providers, provider) {
		return nil, errCustodyUnavailable
	}
	record, err := c.Load(ctx, assertion)
	if err != nil {
		return nil, err
	}
	credential, err := c.tokens.GetValidTokens(ctx, record.TSID, provider)
	if err != nil || credential == nil {
		return nil, custodyError(ctx, err)
	}
	final, _, err := c.readRecord(ctx, assertion.Recovery)
	if c.validAssertion(assertion) != nil || err != nil || final.State != custodyCurrent || !c.matches(assertion, final) || final.TSID != record.TSID {
		return nil, errCustodyUnavailable
	}
	return credential, nil
}

func (c *credentialCustody) validRequest(request custodyRequest) error {
	if validateGuard(request.Guard) != nil || !contract.ValidContinuityAttemptDeadline(c.clock.Now(), request.AttemptDeadline) {
		return errCustodyUnavailable
	}
	return nil
}
func (c *credentialCustody) validAssertion(assertion custodyAssertion) error {
	if c.validRequest(assertion.custodyRequest) != nil || !validRecoveryID(assertion.Recovery) {
		return errCustodyUnavailable
	}
	return nil
}
func (c *credentialCustody) matches(assertion custodyAssertion, record custodyRecord) bool {
	return assertion.AttemptDeadline.After(c.clock.Now()) && record.ID == assertion.Recovery && sameGuard(record.Guard, assertion.Guard) && record.ExpiresAt.After(c.clock.Now()) && !assertion.AttemptDeadline.After(record.ExpiresAt)
}

func validFuture(value, now time.Time) bool          { return !value.IsZero() && value.After(now) }
func (*credentialCustody) key(ref recoveryID) string { return custodyPrefix + string(ref) }

func (c *credentialCustody) sealRecord(record custodyRecord) (string, error) {
	if validateRecord(record) != nil {
		return "", errCustodyUnavailable
	}
	plain, err := json.Marshal(record)
	if err != nil || len(plain) > maxCustodyPlaintext {
		return "", errCustodyUnavailable
	}
	return c.keys.seal(credentialAAD(credentialAADNamespace, "custody", string(record.ID), "record"), string(plain))
}
func (c *credentialCustody) readRecord(ctx context.Context, ref recoveryID) (custodyRecord, string, error) {
	if !validRecoveryID(ref) {
		return custodyRecord{}, "", errCustodyUnavailable
	}
	raw, err := c.client.Get(ctx, c.key(ref)).Result()
	if errors.Is(err, redis.Nil) {
		return custodyRecord{}, "", errCustodyNotFound
	}
	if err != nil {
		return custodyRecord{}, "", custodyError(ctx, err)
	}
	if len(raw) > maxCredentialEnvelope {
		return custodyRecord{}, "", errCustodyUnavailable
	}
	plain, err := c.keys.open(credentialAAD(credentialAADNamespace, "custody", string(ref), "record"), raw)
	if err != nil || len(plain) > maxCustodyPlaintext {
		return custodyRecord{}, "", errCustodyUnavailable
	}
	var record custodyRecord
	if json.Unmarshal([]byte(plain), &record) != nil || validateRecord(record) != nil || record.ID != ref {
		return custodyRecord{}, "", errCustodyUnavailable
	}
	return record, raw, nil
}
func (c *credentialCustody) replace(ctx context.Context, ref recoveryID, old, next string, expiry, deadline time.Time) (bool, error) {
	result, err := custodyReplaceScript.Run(ctx, c.client, []string{c.key(ref)}, old, next, expiry.UnixMilli(), deadline.UnixMilli()).Int()
	if err != nil {
		return false, custodyError(ctx, err)
	}
	if result == -1 {
		return false, errCustodyUnavailable
	}
	return result == 1, nil
}

func validateRecord(record custodyRecord) error {
	if record.Version != custodySchemaVersion || !validRecoveryID(record.ID) || validateGuard(record.Guard) != nil || record.Revision == 0 || record.ExpiresAt.IsZero() || record.ExpiresAt.Location() != time.UTC {
		return errCustodyUnavailable
	}
	switch record.State {
	case custodyStaged, custodyCurrent:
		if record.TSID == "" || len(record.TSID) > maxCustodyTSID || !utf8.ValidString(record.TSID) {
			return errCustodyUnavailable
		}
	case custodyTombstoned:
		if record.TSID != "" {
			return errCustodyUnavailable
		}
	default:
		return errCustodyUnavailable
	}
	return nil
}
func validateGuard(guard custodyGuard) error {
	if !contract.ValidLogicalSessionID(guard.SessionID) || !guard.Incarnation.Valid() || guard.OwnerPartition == ([32]byte{}) || guard.WorkloadPartition == ([32]byte{}) || len(guard.Providers) == 0 || len(guard.Providers) > contract.MaxContinuityProviders {
		return errCustodyUnavailable
	}
	prior := ""
	for _, provider := range guard.Providers {
		if !contract.ValidContinuityProvider(provider) || provider <= prior {
			return errCustodyUnavailable
		}
		prior = provider
	}
	return nil
}
func cloneGuard(in custodyGuard) custodyGuard {
	out := in
	out.Providers = append([]string(nil), in.Providers...)
	return out
}
func sameGuard(a, b custodyGuard) bool {
	return a.SessionID == b.SessionID && a.Incarnation == b.Incarnation && a.OwnerPartition == b.OwnerPartition && a.WorkloadPartition == b.WorkloadPartition && a.ProfileDigest == b.ProfileDigest && slices.Equal(a.Providers, b.Providers)
}
func containsProvider(providers []string, provider string) bool {
	index := sort.SearchStrings(providers, provider)
	return index < len(providers) && providers[index] == provider
}
func newRecoveryID() (recoveryID, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return recoveryID(base64.RawURLEncoding.EncodeToString(raw[:])), nil
}
func validRecoveryID(ref recoveryID) bool {
	raw, err := base64.RawURLEncoding.DecodeString(string(ref))
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == string(ref)
}
func custodyError(ctx context.Context, _ error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errCustodyUnavailable
}

var custodyCreateScript = redis.NewScript(`
local server_time = redis.call('TIME')
local now_ms = tonumber(server_time[1]) * 1000 + math.floor(tonumber(server_time[2]) / 1000)
if now_ms >= tonumber(ARGV[3]) then return -1 end
if redis.call('EXISTS', KEYS[1]) ~= 0 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PXAT', ARGV[2])
return 1
`)
var custodyReplaceScript = redis.NewScript(`
local server_time = redis.call('TIME')
local now_ms = tonumber(server_time[1]) * 1000 + math.floor(tonumber(server_time[2]) / 1000)
if now_ms >= tonumber(ARGV[4]) then return -1 end
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PXAT', ARGV[3])
return 1
`)
