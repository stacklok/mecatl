package grpcdriver

import (
	"context"
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

// compile-time assertion for the mandatory seam.
var _ tool.MemoryStore = (*MemoryStore)(nil)

const (
	memoryCapabilityTimeout  = 5 * time.Second
	memoryStoreContract      = "memory-store/1"
	maxMemoryRecordRevisions = 64
)

func newMemoryStore(conn grpc.ClientConnInterface) *MemoryStore {
	return &MemoryStore{client: driverv1.NewMemoryStoreServiceClient(conn)}
}

// NegotiateMemoryStore requires the remote driver's current mandatory memory
// capabilities. Older/base-only drivers are rejected rather than silently
// weakening lifecycle or CAS semantics.
func NegotiateMemoryStore(ctx context.Context, conn grpc.ClientConnInterface) (tool.MemoryStore, error) {
	store := newMemoryStore(conn)
	probeCtx, cancel := context.WithTimeout(ctx, memoryCapabilityTimeout)
	defer cancel()
	caps, err := store.client.Capabilities(probeCtx, &driverv1.MemoryStoreCapabilitiesRequest{})
	if err != nil {
		return nil, rpcErr(probeCtx, "negotiate memory capabilities", err)
	}
	if caps == nil || caps.GetContract() != memoryStoreContract {
		return nil, fmt.Errorf("remote memory driver contract is %q, want %q", caps.GetContract(), memoryStoreContract)
	}
	return store, nil
}

// Recall returns the entry for the exact key. A driver miss (found=false) is
// (zero, false, nil) — never an error.
func (st *MemoryStore) Recall(ctx context.Context, key string) (tool.MemoryEntry, bool, error) {
	resp, err := st.client.Recall(ctx, &driverv1.RecallRequest{Key: key})
	if err != nil {
		return tool.MemoryEntry{}, false, rpcErr(ctx, "recall", err)
	}
	if resp == nil {
		return tool.MemoryEntry{}, false, fmt.Errorf("grpcdriver: recall: empty response")
	}
	if !resp.GetFound() {
		if resp.GetEntry() != nil {
			return tool.MemoryEntry{}, false, fmt.Errorf("grpcdriver: recall: missing key returned an entry")
		}
		return tool.MemoryEntry{}, false, nil
	}
	entry, err := memoryEntryFromResponse(resp.GetEntry(), key, false)
	return entry, err == nil, err
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

// Remember performs the canonical presence-and-version CAS operation.
func (st *MemoryStore) Remember(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	if err := tool.ValidateMemoryEntryWrite(entry, attr); err != nil {
		return tool.MemoryRecord{}, err
	}
	resp, err := st.client.Remember(ctx, &driverv1.RememberRequest{
		Entry: toProtoEntry(entry), Current: &driverv1.MemoryCurrent{Exists: expected.Exists, Version: string(expected.Version)}, Attribution: toProtoAttribution(attr),
	})
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, entry.Key, expected.Version)
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "remember", err)
	}
	return memoryRecordFromResponse(resp.GetRecord(), entry.Key)
}

// Inspect returns lifecycle data from the mandatory current driver contract.
func (st *MemoryStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	resp, err := st.client.Inspect(ctx, &driverv1.InspectRequest{Key: key})
	if err != nil {
		return tool.MemoryRecord{}, false, rpcErr(ctx, "inspect memory", err)
	}
	if !resp.GetFound() {
		if resp.GetRecord() != nil {
			return tool.MemoryRecord{}, false, fmt.Errorf("grpcdriver: inspect memory: missing key returned a record")
		}
		return tool.MemoryRecord{}, false, nil
	}
	record, err := memoryRecordFromResponse(resp.GetRecord(), key)
	return record, err == nil, err
}

// Forget appends a tombstone under exact-version CAS.
func (st *MemoryStore) Forget(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	resp, err := st.client.Forget(ctx, &driverv1.ForgetRequest{Key: key, ExpectedVersion: string(expected), Attribution: toProtoAttribution(attr)})
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, key, expected)
	}
	if status.Code(err) == codes.NotFound {
		return tool.MemoryRecord{}, fmt.Errorf("%w: %s", tool.ErrMemoryNotFound, status.Convert(err).Message())
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "forget", err)
	}
	return memoryRecordFromResponse(resp.GetRecord(), key)
}

// Undo appends a compensating revision under exact-version CAS.
func (st *MemoryStore) Undo(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	attr, _ := tool.MemoryAttributionFromContext(ctx)
	resp, err := st.client.Undo(ctx, &driverv1.UndoRequest{Key: key, ExpectedVersion: string(expected), Attribution: toProtoAttribution(attr)})
	if status.Code(err) == codes.FailedPrecondition {
		return tool.MemoryRecord{}, st.versionConflict(ctx, key, expected)
	}
	if err != nil {
		return tool.MemoryRecord{}, rpcErr(ctx, "undo", err)
	}
	return memoryRecordFromResponse(resp.GetRecord(), key)
}

func (st *MemoryStore) versionConflict(ctx context.Context, key string, expected tool.MemoryVersion) error {
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
	return &driverv1.MemorySource{SessionId: session.ToValidUTF8(s.SessionID), ProposalId: session.ToValidUTF8(s.ProposalID)}
}

func memoryRecordFromResponse(record *driverv1.MemoryRecord, key string) (tool.MemoryRecord, error) {
	if record == nil || record.GetCurrent() == nil || len(record.GetRevisions()) == 0 || len(record.GetRevisions()) > maxMemoryRecordRevisions {
		return tool.MemoryRecord{}, fmt.Errorf("grpcdriver: malformed memory record: current revision and 1..%d history entries are required", maxMemoryRecordRevisions)
	}
	out := tool.MemoryRecord{Current: fromProtoRevision(record.GetCurrent()), Revisions: make([]tool.MemoryRevision, len(record.GetRevisions()))}
	for i, rev := range record.GetRevisions() {
		if err := validateProtoRevision(rev, key); err != nil {
			return tool.MemoryRecord{}, fmt.Errorf("grpcdriver: malformed memory record revision %d: %w", i, err)
		}
		out.Revisions[i] = fromProtoRevision(rev)
	}
	if err := validateProtoRevision(record.GetCurrent(), key); err != nil {
		return tool.MemoryRecord{}, fmt.Errorf("grpcdriver: malformed current memory revision: %w", err)
	}
	last := out.Revisions[len(out.Revisions)-1]
	if !sameMemoryRevision(out.Current, last) {
		return tool.MemoryRecord{}, fmt.Errorf("grpcdriver: malformed memory record: current revision does not match history tail")
	}
	return out, nil
}

func validateProtoRevision(rev *driverv1.MemoryRevision, key string) error {
	if rev == nil || rev.GetKey() == "" || rev.GetKey() != key || !tool.ValidMemoryKey(rev.GetKey()) {
		return fmt.Errorf("invalid key")
	}
	if rev.GetVersion() == "" || len(rev.GetVersion()) > 128 {
		return fmt.Errorf("invalid version")
	}
	switch tool.MemoryStatus(rev.GetStatus()) {
	case tool.MemoryStatusActive, tool.MemoryStatusSuperseded, tool.MemoryStatusDeleted:
	default:
		return fmt.Errorf("invalid status %q", rev.GetStatus())
	}
	if rev.GetUpdatedAt() == nil || rev.GetUpdatedAt().CheckValid() != nil {
		return fmt.Errorf("invalid updated_at")
	}
	return nil
}

func sameMemoryRevision(a, b tool.MemoryRevision) bool {
	return a.Key == b.Key && a.Value == b.Value && a.Description == b.Description &&
		a.Version == b.Version && a.Status == b.Status && a.Writer == b.Writer &&
		a.Origin == b.Origin && a.Source == b.Source && a.UpdatedAt.Equal(b.UpdatedAt)
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
	return tool.MemoryRevision{Key: rev.GetKey(), Value: rev.GetValue(), Description: rev.GetDescription(), Version: tool.MemoryVersion(rev.GetVersion()), Status: tool.MemoryStatus(rev.GetStatus()), Writer: tool.MemoryWriter(rev.GetWriter()), Origin: tool.MemoryOrigin(rev.GetOrigin()), Source: tool.MemorySource{SessionID: source.GetSessionId(), ProposalID: source.GetProposalId()}, UpdatedAt: updated}
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

func memoryEntryFromResponse(pe *driverv1.MemoryEntry, key string, valueMustBeEmpty bool) (tool.MemoryEntry, error) {
	if pe == nil || !tool.ValidMemoryKey(pe.GetKey()) || key != "" && pe.GetKey() != key {
		return tool.MemoryEntry{}, fmt.Errorf("grpcdriver: malformed memory entry key")
	}
	if valueMustBeEmpty && pe.GetValue() != "" {
		return tool.MemoryEntry{}, fmt.Errorf("grpcdriver: malformed memory index entry contains a value")
	}
	if pe.GetUpdatedAt() == nil || pe.GetUpdatedAt().CheckValid() != nil {
		return tool.MemoryEntry{}, fmt.Errorf("grpcdriver: malformed memory entry timestamp")
	}
	return fromProtoEntry(pe), nil
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
