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
			Name:        m.Name,
			Description: m.Description,
			Origin:      string(m.Origin),
			HasAssets:   m.HasAssets,
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
	return &driverv1.GetSkillBodyResponse{Body: body}, nil
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
	out := make([]*driverv1.SkillAsset, len(assets))
	for i, a := range assets {
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
	return &driverv1.LoadSoulResponse{Body: body}, nil
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
			Name:            d.Name,
			Description:     d.Description,
			Tools:           d.Tools,
			DisallowedTools: d.DisallowedTools,
			Model:           d.Model,
			Provider:        d.Provider,
			PermissionMode:  d.PermissionMode,
			MaxTurns:        int32(d.MaxTurns),     //nolint:gosec // bounded operator config, never overflows
			MaxToolCalls:    int32(d.MaxToolCalls), //nolint:gosec // bounded operator config, never overflows
			Color:           d.Color,
			Skills:          d.Skills,
			McpServers:      toProtoMCPServers(d.MCPServers),
			Hooks:           d.Hooks,
			Body:            d.Body,
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
		out[i] = &driverv1.AgentMCPServer{Name: srv.Name, Url: srv.URL, Headers: srv.Headers}
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
		out[i] = &driverv1.CommandMeta{Name: c.Name, Description: c.Description}
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
	return &driverv1.GetCommandBodyResponse{Body: body}, nil
}

// storeStatus maps a wrapped store's error onto the driver protocol's status
// vocabulary (the §C table): the not-found sentinel → NOT_FOUND, context
// errors → CANCELLED / DEADLINE_EXCEEDED, everything else → INTERNAL.
func storeStatus(err error) error {
	switch {
	case errors.Is(err, port.ErrSessionNotFound):
		return status.Error(codes.NotFound, err.Error())
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
