package grpcdriver

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// valid is the driver-protocol UTF-8 backstop, the peer of internal/adapter/
// server's mapper backstop (issue #402). Skill/soul/command/agent-def content
// is os.ReadFile→string off the workspace with NO decoder to launder it, so a
// Latin-1 SKILL.md or SOUL.md would fail proto.Marshal and turn the RPC into
// codes.Internal. Applied to every DISK-SOURCED string that crosses into a
// proto message; harness-authored tokens (ids, origins, enums) are skipped, and
// AgentMCPServer.Headers is deliberately EXEMPT — it is secret-shaped, and
// rewriting a credential to repair it would corrupt the very thing it carries.
func valid(s string) string { return session.ToValidUTF8(s) }

// validAll is valid over a slice; proto3 validates every element of a repeated
// string field, so a single bad entry fails the whole message.
func validAll(ss []string) []string {
	if len(ss) == 0 {
		return ss
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = valid(s)
	}
	return out
}

// validMap is valid over a map; proto3 validates map KEYS as well as values.
func validMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[valid(k)] = valid(v)
	}
	return out
}

// Server wrappers: mount an IN-PROCESS store as the generated driver server
// interfaces, so a Go driver process (or a bufconn test fixture) is the
// in-process store plus this file plus a grpc.Server. The session wrapper
// runs sessnap SERVER-side too, so a conformance run over wrapper+memstore
// exercises the full encode→wire→decode→state-machine→encode→wire→decode
// path. Exported from this internal adapter for the conformance fixtures;
// PROMOTING them to an importable location for external Go driver authors is
// a deliberate decision for a future DRIVERS.md, not implied here.
//
// CAPACITY: a driver mounting these wrappers MUST construct its grpc.Server
// with grpc.MaxRecvMsgSize(MaxSnapshotBytes) (the protocol's required minimum
// snapshot capacity; gRPC's default 4 MiB receive cap rejects a legitimate
// media-carrying snapshot). The harness client's send/receive limits are
// already raised to the same value by Dial; the bufconn conformance fixture
// does the same server-side.

// sessionStoreServer adapts a port.SessionStore to SessionStoreServiceServer.
type sessionStoreServer struct {
	driverv1.UnimplementedSessionStoreServiceServer
	store port.SessionStore
}

// NewSessionStoreServer wraps st as a SessionStoreService driver server.
func NewSessionStoreServer(st port.SessionStore) driverv1.SessionStoreServiceServer {
	return &sessionStoreServer{store: st}
}

// Save decodes the envelope (rejecting a non-SnapshotFormat payload, a
// malformed payload, or a top-level session_id that disagrees with the id
// inside the payload — all INVALID_ARGUMENT) and persists the restored
// session in the wrapped store.
func (s *sessionStoreServer) Save(ctx context.Context, req *driverv1.SaveRequest) (*driverv1.SaveResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	snap := req.GetSnapshot()
	if snap == nil {
		return nil, status.Error(codes.InvalidArgument, "snapshot is required")
	}
	if got := snap.GetFormat(); got != SnapshotFormat {
		return nil, status.Errorf(codes.InvalidArgument, "unknown snapshot format %q (this server speaks %q)", got, SnapshotFormat)
	}
	if len(snap.GetPayload()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "snapshot payload is empty")
	}
	sess, err := sessnap.Unmarshal(snap.GetPayload())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode snapshot: %v", err)
	}
	// Keying guard: the TOP-LEVEL session_id is the storage key (the proto
	// contract — a driver may key without decoding), so a payload carrying a
	// different id would store one session under another's key. Reject it.
	if string(sess.ID) != req.GetSessionId() {
		return nil, status.Errorf(codes.InvalidArgument,
			"session_id %q does not match the snapshot payload's session id %q (the top-level session_id is the storage key; the two must agree)",
			req.GetSessionId(), sess.ID)
	}
	if err := s.store.Save(ctx, sess); err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.SaveResponse{}, nil
}

// Load fetches the session from the wrapped store and re-encodes it into the
// envelope. A store not-found (port.ErrSessionNotFound) maps to NOT_FOUND.
func (s *sessionStoreServer) Load(ctx context.Context, req *driverv1.LoadRequest) (*driverv1.LoadResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := s.store.Load(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, storeStatus(err)
	}
	line, err := sessnap.Marshal(sess)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode snapshot: %v", err)
	}
	return &driverv1.LoadResponse{
		Snapshot: &driverv1.SessionSnapshot{Format: SnapshotFormat, Payload: line},
	}, nil
}

func (s *sessionStoreServer) Capabilities(context.Context, *driverv1.SessionStoreCapabilitiesRequest) (*driverv1.SessionStoreCapabilitiesResponse, error) {
	_, prunable := s.store.(port.PrunableStore)
	_, pager := s.store.(port.SessionMetadataPager)
	deleteSupported := prunable
	if support, ok := s.store.(port.SessionDeleteSupport); ok {
		deleteSupported = support.SupportsSessionDelete()
	}
	return &driverv1.SessionStoreCapabilitiesResponse{
		List: prunable, MetadataPaging: pager, Delete: deleteSupported,
	}, nil
}

// List serves the retention seam by type-asserting the wrapped backend for
// port.PrunableStore. A backend that is a plain Save/Load store answers
// UNIMPLEMENTED — the protocol's documented "cannot enumerate" posture; the
// harness-side sweeper then degrades to never sweeping that store.
func (s *sessionStoreServer) List(ctx context.Context, _ *driverv1.ListSessionsRequest) (*driverv1.ListSessionsResponse, error) {
	p, ok := s.store.(port.PrunableStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "the wrapped session store does not support enumeration (port.PrunableStore)")
	}
	entries, err := p.List(ctx)
	if err != nil {
		return nil, storeStatus(err)
	}
	out := make([]*driverv1.StoredSessionEntry, 0, len(entries))
	for _, e := range entries {
		pe := &driverv1.StoredSessionEntry{SessionId: string(e.ID)}
		if !e.ModifiedAt.IsZero() {
			pe.ModifiedAt = timestamppb.New(e.ModifiedAt)
		}
		out = append(out, pe)
	}
	return &driverv1.ListSessionsResponse{Sessions: out}, nil
}

// PageMetadata serves the optional bounded metadata pager. Ownership criteria
// are forwarded into the backend query so foreign rows never enter a page or
// its total count.
func (s *sessionStoreServer) PageMetadata(ctx context.Context, req *driverv1.PageSessionMetadataRequest) (*driverv1.PageSessionMetadataResponse, error) {
	pager, ok := s.store.(port.SessionMetadataPager)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "the wrapped session store does not support metadata paging (port.SessionMetadataPager)")
	}
	if req.GetLimit() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must be positive")
	}
	request := port.SessionMetadataPageRequest{
		Limit: int(req.GetLimit()), OwnershipEnforced: req.GetOwnershipEnforced(),
	}
	if cursor := req.GetCursor(); cursor != nil {
		if cursor.GetModifiedAt() == nil || cursor.GetSessionId() == "" || cursor.GetModifiedAt().CheckValid() != nil ||
			!legacyOrBoundCursorFields(cursor.GetGeneration(), cursor.GetScope(), cursor.GetContinuation()) {
			return nil, status.Error(codes.InvalidArgument, "cursor requires a valid key, and either all of generation/scope/continuation or none")
		}
		request.Cursor = &port.SessionMetadataCursor{
			ModifiedAt: cursor.GetModifiedAt().AsTime(), ID: session.SessionID(cursor.GetSessionId()),
			Generation: cursor.GetGeneration(), Scope: cursor.GetScope(), Continuation: cursor.GetContinuation(),
		}
	}
	if req.GetOwnerIssuer() != "" || req.GetOwnerSubject() != "" {
		request.Owner = &session.Principal{Issuer: req.GetOwnerIssuer(), Subject: req.GetOwnerSubject()}
	}
	page, err := pager.PageSessionMetadata(ctx, request)
	if err != nil {
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			return nil, status.Error(codes.Unimplemented, err.Error())
		}
		if errors.Is(err, port.ErrSessionMetadataCursorRestart) {
			return nil, status.Error(codes.Aborted, err.Error())
		}
		return nil, storeStatus(err)
	}
	if page.TotalCount > 1<<31-1 {
		return nil, status.Error(codes.Internal, "session metadata total count exceeds protocol range")
	}
	resp := &driverv1.PageSessionMetadataResponse{TotalCount: int32(page.TotalCount)} // #nosec G115 -- checked above
	resp.Sessions = make([]*driverv1.SessionMetadataEntry, 0, len(page.Sessions))
	for _, meta := range page.Sessions {
		entry, err := metadataToProto(meta)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Sessions = append(resp.Sessions, entry)
	}
	if page.NextCursor != nil {
		resp.NextCursor = &driverv1.SessionMetadataCursor{
			ModifiedAt: timestamppb.New(page.NextCursor.ModifiedAt), SessionId: string(page.NextCursor.ID),
			Generation: page.NextCursor.Generation, Scope: page.NextCursor.Scope, Continuation: page.NextCursor.Continuation,
		}
	}
	return resp, nil
}

func metadataToProto(meta port.SessionDiscoveryMeta) (*driverv1.SessionMetadataEntry, error) {
	if meta.Turns < -1<<31 || meta.Turns > 1<<31-1 {
		return nil, errors.New("session metadata turns exceeds protocol range")
	}
	entry := &driverv1.SessionMetadataEntry{
		SessionId: string(meta.ID), State: string(meta.State), Turns: int32(meta.Turns),
		ModelId: meta.ModelID, Title: meta.Title, TitleProvenance: string(meta.TitleProvenance),
		Workspace: meta.Workspace, Kind: string(meta.Kind), EstimatedBytes: meta.EstimatedBytes,
		ParentSessionId: string(meta.Relationship.ParentSessionID), CallId: string(meta.Relationship.CallID),
		ScheduleName: meta.Relationship.ScheduleName, OriginSessionId: string(meta.Relationship.OriginSessionID),
		TeamId: meta.Relationship.TeamID, MemberName: meta.Relationship.MemberName,
		DebugTargetSessionId: valid(string(meta.Relationship.DebugTargetID)),
	}
	if !meta.ModifiedAt.IsZero() {
		entry.ModifiedAt = timestamppb.New(meta.ModifiedAt)
	}
	if !meta.CreatedAt.IsZero() {
		entry.CreatedAt = timestamppb.New(meta.CreatedAt)
	}
	if meta.Relationship.BranchIndex != nil {
		if *meta.Relationship.BranchIndex < -1<<31 || *meta.Relationship.BranchIndex > 1<<31-1 {
			return nil, errors.New("session metadata branch index exceeds protocol range")
		}
		index := int32(*meta.Relationship.BranchIndex)
		entry.BranchIndex = &index
	}
	if meta.Owner != nil {
		entry.HasOwner = true
		entry.OwnerIssuer = meta.Owner.Issuer
		entry.OwnerSubject = meta.Owner.Subject
		entry.OwnerGrantType = string(meta.Owner.GrantType)
		entry.OwnerName = meta.Owner.Name
	}
	return entry, nil
}

// Delete serves the retention seam's idempotent delete (same PrunableStore
// type-assertion posture as List; UNIMPLEMENTED for a plain backend).
func (s *sessionStoreServer) Delete(ctx context.Context, req *driverv1.DeleteSessionRequest) (*driverv1.DeleteSessionResponse, error) {
	p, ok := s.store.(port.PrunableStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "the wrapped session store does not support deletion (port.PrunableStore)")
	}
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := p.Delete(ctx, session.SessionID(req.GetSessionId())); err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.DeleteSessionResponse{}, nil
}

// memoryStoreServer adapts a tool.MemoryStore to MemoryStoreServiceServer.
type memoryStoreServer struct {
	driverv1.UnimplementedMemoryStoreServiceServer
	store tool.MemoryStore
}

// NewMemoryStoreServer wraps st as a MemoryStoreService driver server.
func NewMemoryStoreServer(st tool.MemoryStore) driverv1.MemoryStoreServiceServer {
	return &memoryStoreServer{store: st}
}

func (s *memoryStoreServer) Capabilities(context.Context, *driverv1.MemoryStoreCapabilitiesRequest) (*driverv1.MemoryStoreCapabilitiesResponse, error) {
	_, lifecycle := s.store.(tool.MemoryLifecycleStore)
	_, convergence := s.store.(tool.MemoryConvergenceStore)
	return &driverv1.MemoryStoreCapabilitiesResponse{Lifecycle: lifecycle, Convergence: convergence}, nil
}

// RememberEntry stores the entry; a blank/whitespace-only key is rejected
// with INVALID_ARGUMENT before the store is consulted (the wrapped store's
// own rejection remains conformance-tested in-process).
func (s *memoryStoreServer) RememberEntry(ctx context.Context, req *driverv1.RememberEntryRequest) (*driverv1.RememberEntryResponse, error) {
	e := req.GetEntry()
	if e == nil {
		return nil, status.Error(codes.InvalidArgument, "entry is required")
	}
	if strings.TrimSpace(e.GetKey()) == "" {
		return nil, status.Error(codes.InvalidArgument, "entry key must not be blank")
	}
	if err := s.store.RememberEntry(ctx, fromProtoEntry(e)); err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.RememberEntryResponse{}, nil
}

// Recall looks up the exact key; a miss is found=false, never NOT_FOUND.
func (s *memoryStoreServer) Recall(ctx context.Context, req *driverv1.RecallRequest) (*driverv1.RecallResponse, error) {
	e, found, err := s.store.Recall(ctx, req.GetKey())
	if err != nil {
		return nil, storeStatus(err)
	}
	resp := &driverv1.RecallResponse{Found: found}
	if found {
		resp.Entry = toProtoEntry(e)
	}
	return resp, nil
}

// List returns the prefix-filtered, key-sorted entries with values present.
func (s *memoryStoreServer) List(ctx context.Context, req *driverv1.ListRequest) (*driverv1.ListResponse, error) {
	entries, err := s.store.List(ctx, req.GetPrefix())
	if err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.ListResponse{Entries: toProtoServerEntries(entries)}, nil
}

// Forget deletes the key; missing keys succeed (idempotent).
func (s *memoryStoreServer) Forget(ctx context.Context, req *driverv1.ForgetRequest) (*driverv1.ForgetResponse, error) {
	if err := s.store.Forget(ctx, req.GetKey()); err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.ForgetResponse{}, nil
}

// Index returns the tier-0 routing table (values omitted by the store).
func (s *memoryStoreServer) Index(ctx context.Context, _ *driverv1.IndexRequest) (*driverv1.IndexResponse, error) {
	entries, err := s.store.Index(ctx)
	if err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.IndexResponse{Entries: toProtoServerEntries(entries)}, nil
}

// Search returns the store's best-first matches (values omitted by the store).
func (s *memoryStoreServer) Search(ctx context.Context, req *driverv1.SearchRequest) (*driverv1.SearchResponse, error) {
	entries, err := s.store.Search(ctx, req.GetQuery(), int(req.GetK()))
	if err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.SearchResponse{Entries: toProtoServerEntries(entries)}, nil
}

func (s *memoryStoreServer) lifecycle() (tool.MemoryLifecycleStore, error) {
	lifecycle, ok := s.store.(tool.MemoryLifecycleStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "memory store does not support lifecycle operations")
	}
	return lifecycle, nil
}

// RememberVersioned delegates only when the wrapped store advertises lifecycle.
func (s *memoryStoreServer) RememberVersioned(ctx context.Context, req *driverv1.RememberVersionedRequest) (*driverv1.MemoryRecordResponse, error) {
	lifecycle, err := s.lifecycle()
	if err != nil {
		return nil, err
	}
	if req.GetEntry() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry is required")
	}
	ctx = withProtoAttribution(ctx, req.GetAttribution())
	record, callErr := lifecycle.RememberVersioned(ctx, fromProtoEntry(req.GetEntry()), tool.MemoryVersion(req.GetExpectedVersion()))
	if callErr != nil {
		return nil, storeStatus(callErr)
	}
	return &driverv1.MemoryRecordResponse{Record: toProtoRecord(record)}, nil
}

func (s *memoryStoreServer) InspectMemory(ctx context.Context, req *driverv1.InspectMemoryRequest) (*driverv1.InspectMemoryResponse, error) {
	lifecycle, err := s.lifecycle()
	if err != nil {
		return nil, err
	}
	record, found, callErr := lifecycle.Inspect(ctx, req.GetKey())
	if callErr != nil {
		return nil, storeStatus(callErr)
	}
	resp := &driverv1.InspectMemoryResponse{Found: found}
	if found {
		resp.Record = toProtoRecord(record)
	}
	return resp, nil
}

func (s *memoryStoreServer) ForgetVersioned(ctx context.Context, req *driverv1.ForgetVersionedRequest) (*driverv1.MemoryRecordResponse, error) {
	lifecycle, err := s.lifecycle()
	if err != nil {
		return nil, err
	}
	ctx = withProtoAttribution(ctx, req.GetAttribution())
	record, callErr := lifecycle.ForgetVersioned(ctx, req.GetKey(), tool.MemoryVersion(req.GetExpectedVersion()))
	if callErr != nil {
		return nil, storeStatus(callErr)
	}
	return &driverv1.MemoryRecordResponse{Record: toProtoRecord(record)}, nil
}

func (s *memoryStoreServer) UndoLatest(ctx context.Context, req *driverv1.UndoLatestRequest) (*driverv1.MemoryRecordResponse, error) {
	lifecycle, err := s.lifecycle()
	if err != nil {
		return nil, err
	}
	ctx = withProtoAttribution(ctx, req.GetAttribution())
	record, callErr := lifecycle.UndoLatest(ctx, req.GetKey(), tool.MemoryVersion(req.GetExpectedVersion()))
	if callErr != nil {
		return nil, storeStatus(callErr)
	}
	return &driverv1.MemoryRecordResponse{Record: toProtoRecord(record)}, nil
}

func (s *memoryStoreServer) RememberIfCurrent(ctx context.Context, req *driverv1.RememberIfCurrentRequest) (*driverv1.MemoryRecordResponse, error) {
	store, ok := s.store.(tool.MemoryConvergenceStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "memory convergence unsupported")
	}
	if req.GetEntry() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry is required")
	}
	ctx = withProtoAttribution(ctx, req.GetAttribution())
	record, err := store.RememberIfCurrent(ctx, fromProtoEntry(req.GetEntry()), tool.MemoryCurrent{Exists: req.GetExpectedExists(), Version: tool.MemoryVersion(req.GetExpectedVersion())})
	if err != nil {
		return nil, storeStatus(err)
	}
	return &driverv1.MemoryRecordResponse{Record: toProtoRecord(record)}, nil
}

func withProtoAttribution(ctx context.Context, a *driverv1.MemoryAttribution) context.Context {
	if a == nil {
		return ctx
	}
	s := a.GetSource()
	return tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriter(a.GetWriter()), Origin: tool.MemoryOrigin(a.GetOrigin()), Source: tool.MemorySource{SessionID: s.GetSessionId(), ProposalID: s.GetProposalId()}})
}

func toProtoRecord(record tool.MemoryRecord) *driverv1.MemoryRecord {
	out := &driverv1.MemoryRecord{Current: toProtoRevision(record.Current), Revisions: make([]*driverv1.MemoryRevision, len(record.Revisions))}
	for i, revision := range record.Revisions {
		out.Revisions[i] = toProtoRevision(revision)
	}
	return out
}

func toProtoRevision(rev tool.MemoryRevision) *driverv1.MemoryRevision {
	out := &driverv1.MemoryRevision{Key: valid(rev.Key), Value: valid(rev.Value), Description: valid(rev.Description), Version: valid(string(rev.Version)), Status: valid(string(rev.Status)), Writer: valid(string(rev.Writer)), Origin: valid(string(rev.Origin)), Source: &driverv1.MemorySource{SessionId: valid(rev.Source.SessionID), ProposalId: valid(rev.Source.ProposalID)}}
	if !rev.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(rev.UpdatedAt)
	}
	return out
}

// skillSourceServer adapts a tool.SkillSource to SkillSourceServiceServer.
type skillSourceServer struct {
	driverv1.UnimplementedSkillSourceServiceServer
	src tool.SkillSource
}

// NewSkillSourceServer wraps src as a SkillSourceService driver server.
func NewSkillSourceServer(src tool.SkillSource) driverv1.SkillSourceServiceServer {
	return &skillSourceServer{src: src}
}

// ListSkills projects the source's metadata snapshot onto the wire.
func (s *skillSourceServer) ListSkills(ctx context.Context, _ *driverv1.ListSkillsRequest) (*driverv1.ListSkillsResponse, error) {
	metas, err := s.src.ListSkills(ctx)
	if err != nil {
		return nil, sourceStatus(err)
	}
	out := make([]*driverv1.SkillMeta, len(metas))
	for i, m := range metas {
		out[i] = &driverv1.SkillMeta{
			Name:          valid(m.Name),
			Description:   valid(m.Description),
			Origin:        string(m.Origin),
			HasAssets:     m.HasAssets,
			License:       valid(m.License),
			Compatibility: valid(m.Compatibility),
			Metadata:      validMap(m.Metadata),
			AllowedTools:  validAll(m.AllowedTools),
		}
	}
	return &driverv1.ListSkillsResponse{Skills: out}, nil
}

// GetSkillBody returns the named skill's body; a blank name is
// INVALID_ARGUMENT before the source is consulted, an unknown skill NOT_FOUND.
func (s *skillSourceServer) GetSkillBody(ctx context.Context, req *driverv1.GetSkillBodyRequest) (*driverv1.GetSkillBodyResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	body, err := s.src.SkillBody(ctx, req.GetName())
	if err != nil {
		return nil, sourceStatus(err)
	}
	return &driverv1.GetSkillBodyResponse{Body: valid(body)}, nil
}

// ListSkillAssets returns the named skill's payload descriptors; a blank name
// is INVALID_ARGUMENT, an unknown skill NOT_FOUND.
func (s *skillSourceServer) ListSkillAssets(ctx context.Context, req *driverv1.ListSkillAssetsRequest) (*driverv1.ListSkillAssetsResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	assets, err := s.src.ListSkillAssets(ctx, req.GetName())
	if err != nil {
		return nil, sourceStatus(err)
	}
	if len(assets) > maxSkillInventoryEntries {
		return nil, status.Errorf(codes.ResourceExhausted, "skill asset inventory has %d entries, limit %d", len(assets), maxSkillInventoryEntries)
	}
	out := make([]*driverv1.SkillAsset, len(assets))
	nameBytes := 0
	for i, a := range assets {
		nameBytes += len(a.Name)
		if nameBytes > maxSkillInventoryNameBytes {
			return nil, status.Errorf(codes.ResourceExhausted, "skill asset inventory names exceed %d bytes", maxSkillInventoryNameBytes)
		}
		if !tool.ValidSkillAssetName(a.Name) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid logical asset name %q", a.Name)
		}
		out[i] = &driverv1.SkillAsset{Name: a.Name, Size: a.Size, Executable: a.Executable}
	}
	return &driverv1.ListSkillAssetsResponse{Assets: out}, nil
}

// ReadSkillAsset returns one payload's bytes. The logical asset name is
// PRE-VALIDATED here via tool.ValidSkillAssetName (the single shared
// validator), so an invalid name is INVALID_ARGUMENT before the source is
// consulted — never content; blank skill/asset are INVALID_ARGUMENT; an
// unknown skill or asset is NOT_FOUND.
func (s *skillSourceServer) ReadSkillAsset(ctx context.Context, req *driverv1.ReadSkillAssetRequest) (*driverv1.ReadSkillAssetResponse, error) {
	if req.GetSkill() == "" {
		return nil, status.Error(codes.InvalidArgument, "skill is required")
	}
	if req.GetAsset() == "" {
		return nil, status.Error(codes.InvalidArgument, "asset is required")
	}
	if !tool.ValidSkillAssetName(req.GetAsset()) {
		return nil, status.Errorf(codes.InvalidArgument,
			"invalid logical asset name %q (slash-separated, relative, no \".\"/\"..\" segments, no backslash)", req.GetAsset())
	}
	data, err := s.src.ReadSkillAsset(ctx, req.GetSkill(), req.GetAsset())
	if err != nil {
		return nil, sourceStatus(err)
	}
	if len(data) > maxSkillAssetDataBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "skill asset payload is %d bytes, limit %d", len(data), maxSkillAssetDataBytes)
	}
	return &driverv1.ReadSkillAssetResponse{Data: data}, nil
}

// soulSourceServer adapts a prompt.SoulSource to SoulSourceServiceServer.
type soulSourceServer struct {
	driverv1.UnimplementedSoulSourceServiceServer
	src prompt.SoulSource
}

// NewSoulSourceServer wraps src as a SoulSourceService driver server. The
// wrapped Go source already upholds the fail-soft contract; the harness
// CLIENT re-validates the body regardless (it never trusts a driver to
// sanitize).
func NewSoulSourceServer(src prompt.SoulSource) driverv1.SoulSourceServiceServer {
	return &soulSourceServer{src: src}
}

// LoadSoul returns the source's body verbatim (empty = no soul, fail-soft).
func (s *soulSourceServer) LoadSoul(ctx context.Context, _ *driverv1.LoadSoulRequest) (*driverv1.LoadSoulResponse, error) {
	body, err := s.src.Load(ctx)
	if err != nil {
		return nil, sourceStatus(err)
	}
	return &driverv1.LoadSoulResponse{Body: valid(body)}, nil
}

// agentSourceServer adapts a tool.AgentDefSource to AgentSourceServiceServer.
type agentSourceServer struct {
	driverv1.UnimplementedAgentSourceServiceServer
	src tool.AgentDefSource
}

// NewAgentSourceServer wraps src as an AgentSourceService driver server.
func NewAgentSourceServer(src tool.AgentDefSource) driverv1.AgentSourceServiceServer {
	return &agentSourceServer{src: src}
}

// ListAgentDefs projects the source's definition snapshot onto the wire.
func (s *agentSourceServer) ListAgentDefs(ctx context.Context, _ *driverv1.ListAgentDefsRequest) (*driverv1.ListAgentDefsResponse, error) {
	defs, err := s.src.ListAgentDefs(ctx)
	if err != nil {
		return nil, sourceStatus(err)
	}
	out := make([]*driverv1.AgentDef, len(defs))
	for i, d := range defs {
		out[i] = &driverv1.AgentDef{
			Name:            valid(d.Name),
			Description:     valid(d.Description),
			Tools:           validAll(d.Tools),
			DisallowedTools: validAll(d.DisallowedTools),
			Model:           valid(d.Model),
			Provider:        valid(d.Provider),
			PermissionMode:  valid(d.PermissionMode),
			MaxTurns:        int32(d.MaxTurns),     //nolint:gosec // bounded operator config, never overflows
			MaxToolCalls:    int32(d.MaxToolCalls), //nolint:gosec // bounded operator config, never overflows
			Color:           valid(d.Color),
			Skills:          validAll(d.Skills),
			McpServers:      toProtoMCPServers(d.MCPServers),
			Hooks:           validMap(d.Hooks),
			Body:            valid(d.Body),
			Origin:          string(d.Origin),
		}
	}
	return &driverv1.ListAgentDefsResponse{AgentDefs: out}, nil
}

// toProtoMCPServers projects the port MCP-server entries onto the wire. The
// header VALUES are secret-shaped; they cross the driver wire only (which
// refuses non-local cleartext) and are never logged.
func toProtoMCPServers(in []tool.AgentMCPServer) []*driverv1.AgentMCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]*driverv1.AgentMCPServer, len(in))
	for i, srv := range in {
		// Headers is NOT repaired: see valid's doc — a secret must cross byte-exact.
		out[i] = &driverv1.AgentMCPServer{Name: valid(srv.Name), Url: valid(srv.URL), Headers: srv.Headers}
	}
	return out
}

// commandSourceServer adapts a prompt.CommandSource to
// CommandSourceServiceServer.
type commandSourceServer struct {
	driverv1.UnimplementedCommandSourceServiceServer
	src prompt.CommandSource
}

// NewCommandSourceServer wraps src as a CommandSourceService driver server.
func NewCommandSourceServer(src prompt.CommandSource) driverv1.CommandSourceServiceServer {
	return &commandSourceServer{src: src}
}

// ListCommands projects the source's CURRENT command metadata onto the wire
// (live semantics — re-consulted per call).
func (s *commandSourceServer) ListCommands(ctx context.Context, _ *driverv1.ListCommandsRequest) (*driverv1.ListCommandsResponse, error) {
	cmds, err := s.src.ListCommands(ctx)
	if err != nil {
		return nil, sourceStatus(err)
	}
	out := make([]*driverv1.CommandMeta, len(cmds))
	for i, c := range cmds {
		out[i] = &driverv1.CommandMeta{Name: valid(c.Name), Description: valid(c.Description)}
	}
	return &driverv1.ListCommandsResponse{Commands: out}, nil
}

// GetCommandBody returns the named command's RAW template; a blank name is
// INVALID_ARGUMENT before the source is consulted, an unknown name NOT_FOUND
// (the harness client maps it to the normal pass-through).
func (s *commandSourceServer) GetCommandBody(ctx context.Context, req *driverv1.GetCommandBodyRequest) (*driverv1.GetCommandBodyResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	body, found, err := s.src.CommandBody(ctx, req.GetName())
	if err != nil {
		return nil, sourceStatus(err)
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "unknown command %q", req.GetName())
	}
	return &driverv1.GetCommandBodyResponse{Body: valid(body)}, nil
}

// storeStatus maps a wrapped store's error onto the driver protocol's status
// vocabulary (the §C table): the not-found sentinel → NOT_FOUND, context
// errors → CANCELLED / DEADLINE_EXCEEDED, everything else → INTERNAL.
func storeStatus(err error) error {
	var conflict *tool.MemoryVersionConflictError
	switch {
	case errors.Is(err, port.ErrSessionNotFound), errors.Is(err, tool.ErrMemoryNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, tool.ErrInvalidMemoryKey), errors.Is(err, tool.ErrSecretMemoryValue):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &conflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// sourceStatus maps a wrapped content source's (skill/soul/agent/command)
// error onto the driver protocol's status vocabulary (the §H table): the
// skill/asset not-found sentinels → NOT_FOUND, context errors → CANCELLED /
// DEADLINE_EXCEEDED, everything else → INTERNAL.
func sourceStatus(err error) error {
	switch {
	case errors.Is(err, tool.ErrSkillNotFound), errors.Is(err, tool.ErrSkillAssetNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// eventLogStatus maps a wrapped event log's error onto the driver protocol's
// status vocabulary. The EventLog port has NO not-found sentinel — a miss is an
// empty Read stream, never an error (absence is data) — so there is no
// NOT_FOUND row: context errors → CANCELLED / DEADLINE_EXCEEDED, everything
// else (an Append durability fault, a Read I/O fault) → INTERNAL.
func eventLogStatus(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// leaseStatus maps a wrapped session lease's error onto the driver protocol's
// status vocabulary: ErrLeaseHeld → FAILED_PRECONDITION (a live competitor holds
// it; consistent with ErrNoActiveRun's mapping in the server relay),
// ErrLeaseUnsupported → UNIMPLEMENTED (the sticky-disable signal, the
// ErrPruneUnsupported precedent), context errors → CANCELLED / DEADLINE_EXCEEDED,
// everything else → INTERNAL.
func leaseStatus(err error) error {
	switch {
	case errors.Is(err, port.ErrLeaseHeld):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, port.ErrLeaseUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// toProtoServerEntries projects store entries onto the wire form for
// responses, carrying the store's stamped UpdatedAt (toProtoEntry already
// guards the zero time → unset).
func toProtoServerEntries(entries []tool.MemoryEntry) []*driverv1.MemoryEntry {
	out := make([]*driverv1.MemoryEntry, len(entries))
	for i, e := range entries {
		out[i] = toProtoEntry(e)
	}
	return out
}
