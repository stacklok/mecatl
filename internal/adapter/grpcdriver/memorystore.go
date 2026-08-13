package grpcdriver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// MemoryStore is a tool.MemoryStore over a remote MemoryStoreService driver.
// It is PURE TRANSLATION: every behavioral guarantee (key validation,
// sorting, value omission on Index/Search, determinism) is the DRIVER's, and
// the memconformance suite run over this client is what pins it. Sanitization
// of model-written values stays harness-side in the memory tools.
type MemoryStore struct {
	client driverv1.MemoryStoreServiceClient
}

// MemoryLifecycleStore is the negotiated lifecycle-capable view of MemoryStore.
// It is returned only after the remote driver successfully answers a lifecycle
// probe, so base-only drivers never accidentally advertise optional tools.
type MemoryLifecycleStore struct{ *MemoryStore }

// compile-time assertions for the distinct base and optional seams.
var (
	_ tool.MemoryStore          = (*MemoryStore)(nil)
	_ tool.MemoryLifecycleStore = (*MemoryLifecycleStore)(nil)
)

// ErrMemoryLifecycleUnsupported reports an old/base-only remote driver. Base
// memory operations remain usable; irreversible lifecycle operations never
// silently downgrade.
var ErrMemoryLifecycleUnsupported = errors.New("remote memory driver does not support lifecycle operations")

const memoryCapabilityTimeout = 5 * time.Second

// NewMemoryStore wraps an established driver connection (see Dial) as a
// tool.MemoryStore.
func NewMemoryStore(conn grpc.ClientConnInterface) *MemoryStore {
	return &MemoryStore{client: driverv1.NewMemoryStoreServiceClient(conn)}
}

// NegotiateMemoryStore reads the explicit optional-capability signal once and
// returns either the base client or a distinct lifecycle-capable wrapper.
// Unimplemented means an older valid driver; every other negotiation failure is
// returned rather than silently changing the catalog.
func NegotiateMemoryStore(ctx context.Context, conn grpc.ClientConnInterface) (tool.MemoryStore, error) {
	base := NewMemoryStore(conn)
	probeCtx, cancel := context.WithTimeout(ctx, memoryCapabilityTimeout)
	defer cancel()
	caps, err := base.client.Capabilities(probeCtx, &driverv1.MemoryStoreCapabilitiesRequest{})
	if status.Code(err) == codes.Unimplemented {
		return base, nil
	}
	if err != nil {
		return nil, rpcErr(probeCtx, "negotiate memory capabilities", err)
	}
	if !caps.GetLifecycle() {
		return base, nil
	}
	return &MemoryLifecycleStore{MemoryStore: base}, nil
}

// RememberEntry stores e on the driver, overwriting any existing entry under
// e.Key. The driver stamps UpdatedAt on write (the input value is advisory);
// a blank/whitespace-only key surfaces the driver's INVALID_ARGUMENT.
func (st *MemoryStore) RememberEntry(ctx context.Context, e tool.MemoryEntry) error {
	if _, err := st.client.RememberEntry(ctx, &driverv1.RememberEntryRequest{Entry: toProtoEntry(e)}); err != nil {
		return rpcErr(ctx, "remember entry", err)
	}
	return nil
}

// Recall returns the entry for the exact key. A driver miss (found=false) is
// (zero, false, nil) — never an error.
func (st *MemoryStore) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	resp, err := st.client.Recall(ctx, &driverv1.RecallRequest{Key: key})
	if err != nil {
		return tool.MemoryEntry{}, false, rpcErr(ctx, "recall", err)
	}
	if !resp.GetFound() {
		return tool.MemoryEntry{}, false, nil
	}
	return fromProtoEntry(resp.GetEntry()), true, nil
}

// List returns all entries whose key has the given prefix (empty = all),
// key-sorted by the driver, values present.
func (st *MemoryStore) List(ctx context.Context, prefix string) ([]tool.MemoryEntry, error) {
	resp, err := st.client.List(ctx, &driverv1.ListRequest{Prefix: prefix})
	if err != nil {
		return nil, rpcErr(ctx, "list", err)
	}
	return fromProtoEntries(resp.GetEntries()), nil
}

// Forget deletes the entry for key on the driver; a missing key is not an
// error (idempotent).
func (st *MemoryStore) Forget(ctx context.Context, key string) error {
	if _, err := st.client.Forget(ctx, &driverv1.ForgetRequest{Key: key}); err != nil {
		return rpcErr(ctx, "forget", err)
	}
	return nil
}

// Index returns the driver's tier-0 routing table: key-sorted entries with
// values omitted and descriptions filled.
func (st *MemoryStore) Index(ctx context.Context) ([]tool.MemoryEntry, error) {
	resp, err := st.client.Index(ctx, &driverv1.IndexRequest{})
	if err != nil {
		return nil, rpcErr(ctx, "index", err)
	}
	return fromProtoEntries(resp.GetEntries()), nil
}

// Search returns up to k entries relevant to query, best-first per the
// driver's (deterministic) ranking, values omitted. k <= 0 selects the
// driver's default page size; a blank query yields an empty result.
func (st *MemoryStore) Search(ctx context.Context, query string, k int) ([]tool.MemoryEntry, error) {
	resp, err := st.client.Search(ctx, &driverv1.SearchRequest{Query: query, K: int32(k)}) //nolint:gosec // k is a small page size
	if err != nil {
		return nil, rpcErr(ctx, "search", err)
	}
	return fromProtoEntries(resp.GetEntries()), nil
}

// RememberVersioned uses the lifecycle RPC selected by negotiation. Once a
// driver advertises lifecycle support, any RPC failure is a protocol/operation
// error; the capable client never downgrades to legacy unconditional writes.
func (st *MemoryLifecycleStore) RememberVersioned(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryEntryWrite(entry, attr); err != nil {
		return tool.MemoryRecord{}, err
	}
	resp, err := st.client.RememberVersioned(ctx, &driverv1.RememberVersionedRequest{Entry: toProtoEntry(entry), ExpectedVersion: string(expected), Attribution: toProtoAttribution(attr)})
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, entry.Key, expected)
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "remember versioned", err)
	}
	return fromProtoRecord(resp.GetRecord()), nil
}

// Inspect returns lifecycle data from the positively negotiated driver. A
// missing or failing lifecycle RPC is not reinterpreted as a legacy Recall.
func (st *MemoryLifecycleStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	resp, err := st.client.InspectMemory(ctx, &driverv1.InspectMemoryRequest{Key: key})
	if err != nil {
		return tool.MemoryRecord{}, false, rpcErr(ctx, "inspect memory", err)
	}
	return fromProtoRecord(resp.GetRecord()), resp.GetFound(), nil
}

// ForgetVersioned never downgrades to legacy Forget because doing so would lose
// compare-version protection and history.
func (st *MemoryLifecycleStore) ForgetVersioned(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	resp, err := st.client.ForgetVersioned(ctx, &driverv1.ForgetVersionedRequest{Key: key, ExpectedVersion: string(expected), Attribution: toProtoAttribution(attr)})
	if status.Code(err) == codes.Unimplemented {
		return tool.MemoryRecord{}, fmt.Errorf("%w: forget", ErrMemoryLifecycleUnsupported)
	}
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, key, expected)
	}
	if status.Code(err) == codes.NotFound {
		return tool.MemoryRecord{}, fmt.Errorf("%w: %s", tool.ErrMemoryNotFound, status.Convert(err).Message())
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "forget versioned", err)
	}
	return fromProtoRecord(resp.GetRecord()), nil
}

// UndoLatest requires lifecycle support and never silently degrades.
func (st *MemoryLifecycleStore) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	resp, err := st.client.UndoLatest(ctx, &driverv1.UndoLatestRequest{Key: key, ExpectedVersion: string(expected), Attribution: toProtoAttribution(attr)})
	if status.Code(err) == codes.Unimplemented {
		return tool.MemoryRecord{}, fmt.Errorf("%w: undo", ErrMemoryLifecycleUnsupported)
	}
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, key, expected)
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "undo latest", err)
	}
	return fromProtoRecord(resp.GetRecord()), nil
}

func (st *MemoryLifecycleStore) versionConflict(ctx context.Context, key string, expected tool.MemoryVersion) error {
	record, found, err := st.Inspect(ctx, key)
	if err != nil {
		return err
	}
	var actual tool.MemoryVersion
	if found {
		actual = record.Current.Version
	}
	return &tool.MemoryVersionConflictError{Key: key, Expected: expected, Actual: actual}
}

func toProtoAttribution(a tool.MemoryAttribution) *driverv1.MemoryAttribution {
	return &driverv1.MemoryAttribution{Writer: session.ToValidUTF8(string(a.Writer)), Origin: session.ToValidUTF8(string(a.Origin)), Source: toProtoSource(a.Source)}
}

func toProtoSource(s tool.MemorySource) *driverv1.MemorySource {
	return &driverv1.MemorySource{SessionId: session.ToValidUTF8(s.SessionID)}
}

func fromProtoRecord(record *driverv1.MemoryRecord) tool.MemoryRecord {
	if record == nil {
		return tool.MemoryRecord{}
	}
	out := tool.MemoryRecord{Current: fromProtoRevision(record.GetCurrent()), Revisions: make([]tool.MemoryRevision, len(record.GetRevisions()))}
	for i, rev := range record.GetRevisions() {
		out.Revisions[i] = fromProtoRevision(rev)
	}
	return out
}

func fromProtoRevision(rev *driverv1.MemoryRevision) tool.MemoryRevision {
	if rev == nil {
		return tool.MemoryRevision{}
	}
	var updated time.Time
	if ts := rev.GetUpdatedAt(); ts != nil && ts.IsValid() {
		updated = ts.AsTime()
	}
	source := rev.GetSource()
	return tool.MemoryRevision{Key: rev.GetKey(), Value: rev.GetValue(), Description: rev.GetDescription(), Version: tool.MemoryVersion(rev.GetVersion()), Status: tool.MemoryStatus(rev.GetStatus()), Writer: tool.MemoryWriter(rev.GetWriter()), Origin: tool.MemoryOrigin(rev.GetOrigin()), Source: tool.MemorySource{SessionID: source.GetSessionId()}, UpdatedAt: updated}
}

// toProtoEntry projects a tool.MemoryEntry onto the wire form. A zero
// UpdatedAt stays nil (unset) rather than the epoch — the driver stamps the
// write time anyway (the field is advisory on input).
func toProtoEntry(e tool.MemoryEntry) *driverv1.MemoryEntry {
	pe := &driverv1.MemoryEntry{Key: session.ToValidUTF8(e.Key), Value: session.ToValidUTF8(e.Value), Description: session.ToValidUTF8(e.Description)}
	if !e.UpdatedAt.IsZero() {
		pe.UpdatedAt = timestamppb.New(e.UpdatedAt)
	}
	return pe
}

// fromProtoEntry projects a wire entry back onto tool.MemoryEntry. A nil/
// unset timestamp maps to the zero time.Time, never the Unix epoch.
func fromProtoEntry(pe *driverv1.MemoryEntry) tool.MemoryEntry {
	if pe == nil {
		return tool.MemoryEntry{}
	}
	var updated time.Time
	if ts := pe.GetUpdatedAt(); ts != nil && ts.IsValid() {
		updated = ts.AsTime()
	}
	return tool.MemoryEntry{Key: session.ToValidUTF8(pe.GetKey()), Value: session.ToValidUTF8(pe.GetValue()), Description: session.ToValidUTF8(pe.GetDescription()), UpdatedAt: updated}
}

func fromProtoEntries(pes []*driverv1.MemoryEntry) []tool.MemoryEntry {
	if pes == nil {
		return nil
	}
	out := make([]tool.MemoryEntry, len(pes))
	for i, pe := range pes {
		out[i] = fromProtoEntry(pe)
	}
	return out
}
