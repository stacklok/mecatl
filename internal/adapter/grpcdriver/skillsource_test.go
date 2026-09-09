package grpcdriver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// hostileSkillServer is a RAW driverv1 server (NOT the harness wrapper) so the
// client's defensive normalization can be fed wire data a well-behaved Go
// source could never produce: blank names, duplicates, unsorted order,
// oversized descriptions, unknown origin labels, unsorted assets.
type hostileSkillServer struct {
	driverv1.UnimplementedSkillSourceServiceServer
	skills []*driverv1.SkillMeta
	// skillAssets maps skill name → wire assets (returned verbatim, unsorted,
	// with a defensive NO-OP for unknown skills so the sentinel test stays
	// intact). A nil value means the skill is known but asset-less.
	skillAssets         map[string][]*driverv1.SkillAsset
	readSkillAssetCalls int
	readSkillAssetData  []byte
}

func (s *hostileSkillServer) ListSkills(context.Context, *driverv1.ListSkillsRequest) (*driverv1.ListSkillsResponse, error) {
	return &driverv1.ListSkillsResponse{Skills: s.skills}, nil
}

func (s *hostileSkillServer) ListSkillAssets(_ context.Context, req *driverv1.ListSkillAssetsRequest) (*driverv1.ListSkillAssetsResponse, error) {
	assets, ok := s.skillAssets[req.GetName()]
	if !ok {
		return nil, status.Error(codes.NotFound, "skill not found")
	}
	return &driverv1.ListSkillAssetsResponse{Assets: assets}, nil
}

func (s *hostileSkillServer) ReadSkillAsset(_ context.Context, _ *driverv1.ReadSkillAssetRequest) (*driverv1.ReadSkillAssetResponse, error) {
	s.readSkillAssetCalls++
	return &driverv1.ReadSkillAssetResponse{Data: s.readSkillAssetData}, nil
}

func newHostileSkillClient(t *testing.T, metas []*driverv1.SkillMeta) *SkillSource {
	t.Helper()
	return newHostileSkillClientWithAssets(t, metas, nil)
}

// newHostileSkillClientWithAssets returns a SkillSource over a hostile server
// that also hosts assets (unsorted, for the ListSkillAssets regression test).
func newHostileSkillClientWithAssets(t *testing.T, metas []*driverv1.SkillMeta, assets map[string][]*driverv1.SkillAsset) *SkillSource {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, &hostileSkillServer{skills: metas, skillAssets: assets})
	})
	return NewSkillSource(conn)
}

// TestSkillSourceListDefensiveDiscipline pins the §H client discipline: drop
// invalid names, de-dup first-wins, sort by name, force descriptions
// single-line then truncate to the always-in-context cap, and stamp Origin
// SkillOriginDriver UNCONDITIONALLY (a driver must not claim project/user
// tier labels).
func TestSkillSourceListDefensiveDiscipline(t *testing.T) {
	long := strings.Repeat("d", skills.MaxDescriptionBytes*2)
	src := newHostileSkillClient(t, []*driverv1.SkillMeta{
		{Name: "zeta", Description: "claims a trusted tier", Origin: "user"},
		{Name: "", Description: "blank name must be dropped"},
		{Name: "   ", Description: "whitespace name must be dropped"},
		{Name: "line\nbreak", Description: "control name must be dropped"},
		{Name: "line\u2028break", Description: "line-separator name must be dropped"},
		{Name: "BadName", Description: "grammar-violating name must be dropped"},
		{Name: "dup", Description: "first wins", Origin: "registry-of-doom"},
		{Name: "dup", Description: "second loses"},
		{Name: "alpha", Description: long, Origin: ""},
		{Name: "multiline", Description: "line one\nline two\r\n\tline three\x7f!", Origin: "project"},
	})
	got, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListSkills kept %d skills, want 4 (invalid names dropped, dup de-duped): %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "dup" || got[2].Name != "multiline" || got[3].Name != "zeta" {
		t.Errorf("not name-sorted: %q, %q, %q, %q", got[0].Name, got[1].Name, got[2].Name, got[3].Name)
	}
	if len(got[0].Description) > skills.MaxDescriptionBytes {
		t.Errorf("description not truncated to the cap: %d > %d", len(got[0].Description), skills.MaxDescriptionBytes)
	}
	if got[1].Description != "first wins" {
		t.Errorf("de-dup kept %q, want the FIRST wire entry", got[1].Description)
	}
	// Single-line promise: every control char (newline/CR/tab/DEL) becomes a
	// space BEFORE the byte-cap truncation, so no line structure survives into
	// the always-in-context tool description.
	if strings.ContainsAny(got[2].Description, "\n\r\t\x7f") {
		t.Errorf("multi-line description leaked control characters: %q", got[2].Description)
	}
	if want := "line one line two   line three !"; got[2].Description != want {
		t.Errorf("single-line normalization = %q, want %q", got[2].Description, want)
	}
	// Origin is stamped driver UNCONDITIONALLY — even "user"/"project" claims.
	for _, m := range got {
		if m.Origin != tool.SkillOriginDriver {
			t.Errorf("skill %q Origin = %q, want driver (a driver-listed skill is ALWAYS driver tier)", m.Name, m.Origin)
		}
	}
}

// newFixtureSkillClient wires the client to the REAL server wrapper over the
// canonical fixture, for the sentinel/ctx tests.
func newFixtureSkillClient(t *testing.T) *SkillSource {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, NewSkillSourceServer(sourceconformance.NewFixtureSource()))
	})
	return NewSkillSource(conn)
}

// TestSkillSourceSentinelMapping pins the §H NOT_FOUND rows: unknown skill →
// ErrSkillNotFound (body/assets, name in message), unknown skill or asset →
// ErrSkillAssetNotFound (read).
func TestSkillSourceSentinelMapping(t *testing.T) {
	src := newFixtureSkillClient(t)
	ctx := context.Background()

	_, err := src.SkillBody(ctx, "ghost")
	if !errors.Is(err, tool.ErrSkillNotFound) {
		t.Errorf("SkillBody(unknown) = %v, want ErrSkillNotFound", err)
	}
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("SkillBody(unknown) error must carry the name, got %v", err)
	}
	if _, err := src.ListSkillAssets(ctx, "ghost"); !errors.Is(err, tool.ErrSkillNotFound) {
		t.Errorf("ListSkillAssets(unknown) = %v, want ErrSkillNotFound", err)
	}
	if _, err := src.ReadSkillAsset(ctx, "ghost", "x.md"); !errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Errorf("ReadSkillAsset(unknown skill) = %v, want ErrSkillAssetNotFound", err)
	}
	if _, err := src.ReadSkillAsset(ctx, "review", "no-such.md"); !errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Errorf("ReadSkillAsset(unknown asset) = %v, want ErrSkillAssetNotFound", err)
	}
	// Invalid logical names are rejected locally before an RPC; a separate raw
	// hostile-server test proves a nonconforming driver cannot supply content.
	data, err := src.ReadSkillAsset(ctx, "review", "../escape")
	if err == nil || len(data) != 0 {
		t.Errorf("ReadSkillAsset(invalid name) = %q, %v; want an error and no content", data, err)
	}
	if errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Errorf("invalid name mapped to the not-found sentinel %v; want a distinct validation error", err)
	}
}

// TestSkillSourceReadAssetRejectsInvalidNameBeforeRPC proves the client keeps
// the SkillSource invalid-name contract even when a raw nonconforming driver
// would return bytes for the request.
func TestSkillSourceReadAssetRejectsInvalidNameBeforeRPC(t *testing.T) {
	server := &hostileSkillServer{readSkillAssetData: []byte("hostile driver content")}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, server)
	})
	src := NewSkillSource(conn)

	data, err := src.ReadSkillAsset(context.Background(), "bundle", "references/forged\nasset.md")
	if err == nil || len(data) != 0 {
		t.Fatalf("ReadSkillAsset(invalid) = %q, %v; want local error and no content", data, err)
	}
	if server.readSkillAssetCalls != 0 {
		t.Errorf("invalid asset reached hostile driver %d time(s), want no RPC", server.readSkillAssetCalls)
	}
}

// TestSkillSourceCtxRewrap pins the §H context row: a caller-cancelled ctx
// surfaces so errors.Is(err, context.Canceled) holds harness-side.
func TestSkillSourceCtxRewrap(t *testing.T) {
	src := newFixtureSkillClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.ListSkills(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListSkills(cancelled ctx) = %v, want errors.Is(_, context.Canceled)", err)
	}
	if _, err := src.SkillBody(ctx, "review"); !errors.Is(err, context.Canceled) {
		t.Errorf("SkillBody(cancelled ctx) = %v, want errors.Is(_, context.Canceled)", err)
	}
}

// TestSkillSourceOptionalFrontmatterRoundTrip pins the advisory frontmatter
// wire fields (issue #419): license/compatibility/metadata project from the
// fixture source through the server wrapper onto the wire and back into
// tool.SkillMeta.
func TestSkillSourceOptionalFrontmatterRoundTrip(t *testing.T) {
	src := newFixtureSkillClient(t)
	got, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	var review *tool.SkillMeta
	for i, m := range got {
		if m.Name == "review" {
			review = &got[i]
			break
		}
	}
	if review == nil {
		t.Fatalf("review skill missing from ListSkills: %+v", got)
	}
	if review.License != "MIT" {
		t.Errorf("review License = %q, want %q", review.License, "MIT")
	}
	if review.Compatibility != "mecatl >= 0.1" {
		t.Errorf("review Compatibility = %q, want %q", review.Compatibility, "mecatl >= 0.1")
	}
	wantMeta := map[string]string{"author": "stacklok", "version": "1"}
	if !reflect.DeepEqual(review.Metadata, wantMeta) {
		t.Errorf("review Metadata = %v, want %v", review.Metadata, wantMeta)
	}
	wantTools := []string{"Read", "Grep", "Shell"}
	if !reflect.DeepEqual(review.AllowedTools, wantTools) {
		t.Errorf("review AllowedTools = %v, want %v", review.AllowedTools, wantTools)
	}
	// The other fixtures omit the optional fields and must round-trip zero.
	for _, m := range got {
		if m.Name == "review" {
			continue
		}
		if m.License != "" || m.Compatibility != "" || m.Metadata != nil || m.AllowedTools != nil {
			t.Errorf("skill %q should have zero advisory fields, got License=%q Compatibility=%q Metadata=%v AllowedTools=%v",
				m.Name, m.License, m.Compatibility, m.Metadata, m.AllowedTools)
		}
	}
}

// TestSkillSourceHostileMetadataDefensiveClamp pins the client's defensive
// clamp on hostile wire metadata: oversized license/compatibility are
// truncated to the advisory cap, an over-count metadata map drops to nil, and
// an over-sized value drops the whole map — no panic.
func TestSkillSourceHostileMetadataDefensiveClamp(t *testing.T) {
	longLicense := strings.Repeat("L", skills.MaxLicenseBytes*2)
	longCompat := strings.Repeat("C", skills.MaxCompatibilityBytes*2)
	// Entry-count overflow.
	hugeMeta := make(map[string]string, skills.MaxMetadataEntries+1)
	for i := 0; i < skills.MaxMetadataEntries+1; i++ {
		hugeMeta[fmt.Sprintf("k%d", i)] = "v"
	}
	src := newHostileSkillClient(t, []*driverv1.SkillMeta{
		{Name: "big-license", Description: "x", License: longLicense, Compatibility: longCompat},
		{Name: "too-many", Description: "x", Metadata: hugeMeta},
		{Name: "one-huge-val", Description: "x", Metadata: map[string]string{"k": strings.Repeat("V", skills.MaxMetadataValueBytes+1)}},
	})
	got, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	byName := map[string]tool.SkillMeta{}
	for _, m := range got {
		byName[m.Name] = m
	}
	if b, ok := byName["big-license"]; !ok {
		t.Fatalf("big-license missing: %+v", got)
	} else {
		if len(b.License) > skills.MaxLicenseBytes {
			t.Errorf("License not clamped: %d > %d", len(b.License), skills.MaxLicenseBytes)
		}
		if len(b.Compatibility) > skills.MaxCompatibilityBytes {
			t.Errorf("Compatibility not clamped: %d > %d", len(b.Compatibility), skills.MaxCompatibilityBytes)
		}
	}
	if t2, ok := byName["too-many"]; !ok {
		t.Fatalf("too-many missing: %+v", got)
	} else if t2.Metadata != nil {
		t.Errorf("too-many Metadata should drop to nil on entry overflow, got %v", t2.Metadata)
	}
	if t3, ok := byName["one-huge-val"]; !ok {
		t.Fatalf("one-huge-val missing: %+v", got)
	} else if t3.Metadata != nil {
		t.Errorf("one-huge-val Metadata should drop to nil on value overflow, got %v", t3.Metadata)
	}
}

// TestSkillSourceHostileAllowedToolsDefensiveClamp pins the client's defensive
// clamp on hostile wire `allowed-tools`: an over-count list is truncated to the
// advisory cap, and an over-long name is truncated — no panic, and ADVISORY
// ONLY (never a permission grant).
func TestSkillSourceHostileAllowedToolsDefensiveClamp(t *testing.T) {
	// Entry-count overflow.
	huge := make([]string, skills.MaxAllowedTools+1)
	for i := range huge {
		huge[i] = "tool"
	}
	longName := strings.Repeat("T", skills.MaxAllowedToolNameBytes*2)
	src := newHostileSkillClient(t, []*driverv1.SkillMeta{
		{Name: "too-many-tools", Description: "x", AllowedTools: huge},
		{Name: "huge-name", Description: "x", AllowedTools: []string{longName}},
	})
	got, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	byName := map[string]tool.SkillMeta{}
	for _, m := range got {
		byName[m.Name] = m
	}
	if b, ok := byName["too-many-tools"]; !ok {
		t.Fatalf("too-many-tools missing: %+v", got)
	} else if len(b.AllowedTools) > skills.MaxAllowedTools {
		t.Errorf("AllowedTools not clamped to the count cap: %d > %d", len(b.AllowedTools), skills.MaxAllowedTools)
	}
	if b, ok := byName["huge-name"]; !ok {
		t.Fatalf("huge-name missing: %+v", got)
	} else if len(b.AllowedTools) != 1 || len(b.AllowedTools[0]) > skills.MaxAllowedToolNameBytes {
		t.Errorf("AllowedTools name not clamped: %d > %d", len(b.AllowedTools[0]), skills.MaxAllowedToolNameBytes)
	}
}

// TestSkillSourceListAssetsSortsHostileOrder is the regression test for the
// ListSkillAssets latent bug (MUST-FIX 1): the driver client must sort assets by
// name so the "Bundled files:" rendering is deterministic — the FS source's
// listAssets already sorts, and the conformance suite's sortAssets masked the
// driver client's omission by sorting both sides before comparing. Feed the
// hostile server assets in REVERSE (descending) order and assert the client
// returns them name-sorted. This test FAILS without the client-side sort.
func TestSkillSourceListAssetsSortsHostileOrder(t *testing.T) {
	src := newHostileSkillClientWithAssets(t, []*driverv1.SkillMeta{
		{Name: "bundle", Description: "x", HasAssets: true},
	}, map[string][]*driverv1.SkillAsset{
		"bundle": {
			{Name: "scripts/run.sh", Size: 14, Executable: true},
			{Name: "references/api.md", Size: 9},
		},
	})
	got, err := src.ListSkillAssets(context.Background(), "bundle")
	if err != nil {
		t.Fatalf("ListSkillAssets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 assets, got %d: %+v", len(got), got)
	}
	if got[0].Name != "references/api.md" || got[1].Name != "scripts/run.sh" {
		t.Errorf("ListSkillAssets not sorted: %q, %q (want references/api.md then scripts/run.sh)", got[0].Name, got[1].Name)
	}
}

func TestSkillSourceAssetRPCReceiveCaps(t *testing.T) {
	t.Run("advertised small actual oversize payload", func(t *testing.T) {
		server := &hostileSkillServer{
			skillAssets:        map[string][]*driverv1.SkillAsset{"bundle": {{Name: "references/data.txt", Size: 1}}},
			readSkillAssetData: []byte(strings.Repeat("x", maxSkillAssetRPCResponseBytes*2)),
		}
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSkillSourceServiceServer(gs, server)
		})
		src := NewSkillSource(conn)
		assets, err := src.ListSkillAssets(context.Background(), "bundle")
		if err != nil || len(assets) != 1 || assets[0].Size != 1 {
			t.Fatalf("advertised inventory = %+v, %v", assets, err)
		}
		data, err := src.ReadSkillAsset(context.Background(), "bundle", "references/data.txt")
		if err == nil || len(data) != 0 || status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("oversize ReadSkillAsset = %d bytes, %v; want transport ResourceExhausted", len(data), err)
		}
	})

	t.Run("aggregate inventory name bytes", func(t *testing.T) {
		assets := make([]*driverv1.SkillAsset, 200)
		for i := range assets {
			assets[i] = &driverv1.SkillAsset{Name: fmt.Sprintf("references/%03d-%s.txt", i, strings.Repeat("n", 160)), Size: 1}
		}
		server := &hostileSkillServer{skillAssets: map[string][]*driverv1.SkillAsset{"bundle": assets}}
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSkillSourceServiceServer(gs, server)
		})
		got, err := NewSkillSource(conn).ListSkillAssets(context.Background(), "bundle")
		if err == nil || got != nil || status.Code(err) == codes.ResourceExhausted || !strings.Contains(err.Error(), "inventory names exceed") {
			t.Fatalf("aggregate-name ListSkillAssets = (%d assets, %v), want client-side whole-inventory rejection", len(got), err)
		}
	})

	t.Run("oversize inventory", func(t *testing.T) {
		assets := make([]*driverv1.SkillAsset, maxSkillInventoryEntries)
		for i := range assets {
			assets[i] = &driverv1.SkillAsset{Name: fmt.Sprintf("references/%04d-%s.txt", i, strings.Repeat("x", 80)), Size: 1}
		}
		server := &hostileSkillServer{skillAssets: map[string][]*driverv1.SkillAsset{"bundle": assets}}
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSkillSourceServiceServer(gs, server)
		})
		got, err := NewSkillSource(conn).ListSkillAssets(context.Background(), "bundle")
		if err == nil || got != nil || status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("oversize ListSkillAssets = %d assets, %v; want transport ResourceExhausted", len(got), err)
		}
	})
}

type oversizedAssetSource struct {
	tool.SkillSource
	assets []tool.SkillAsset
	data   []byte
}

func (s oversizedAssetSource) ListSkillAssets(context.Context, string) ([]tool.SkillAsset, error) {
	return s.assets, nil
}

func (s oversizedAssetSource) ReadSkillAsset(context.Context, string, string) ([]byte, error) {
	return s.data, nil
}

func TestSkillSourceServerWrapperBoundsAssets(t *testing.T) {
	base := sourceconformance.NewFixtureSource()
	t.Run("inventory count", func(t *testing.T) {
		assets := make([]tool.SkillAsset, maxSkillInventoryEntries+1)
		for i := range assets {
			assets[i] = tool.SkillAsset{Name: fmt.Sprintf("references/%d.txt", i), Size: 1}
		}
		srv := NewSkillSourceServer(oversizedAssetSource{SkillSource: base, assets: assets})
		_, err := srv.ListSkillAssets(context.Background(), &driverv1.ListSkillAssetsRequest{Name: "review"})
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("ListSkillAssets error = %v, want ResourceExhausted", err)
		}
	})
	t.Run("inventory name bytes", func(t *testing.T) {
		assets := make([]tool.SkillAsset, 200)
		for i := range assets {
			assets[i] = tool.SkillAsset{Name: fmt.Sprintf("references/%03d-%s.txt", i, strings.Repeat("n", 160)), Size: 1}
		}
		srv := NewSkillSourceServer(oversizedAssetSource{SkillSource: base, assets: assets})
		resp, err := srv.ListSkillAssets(context.Background(), &driverv1.ListSkillAssetsRequest{Name: "review"})
		if status.Code(err) != codes.ResourceExhausted || resp != nil {
			t.Fatalf("ListSkillAssets = (%v, %v), want nil ResourceExhausted on aggregate name bytes", resp, err)
		}
	})
	t.Run("payload size", func(t *testing.T) {
		srv := NewSkillSourceServer(oversizedAssetSource{SkillSource: base, data: make([]byte, maxSkillAssetDataBytes+1)})
		_, err := srv.ReadSkillAsset(context.Background(), &driverv1.ReadSkillAssetRequest{Skill: "review", Asset: "references/checklist.md"})
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("ReadSkillAsset error = %v, want ResourceExhausted", err)
		}
	})
}

func TestSkillSourceListAssetsRejectsControlName(t *testing.T) {
	src := newHostileSkillClientWithAssets(t, []*driverv1.SkillMeta{{Name: "bundle", Description: "x", HasAssets: true}}, map[string][]*driverv1.SkillAsset{
		"bundle": {&driverv1.SkillAsset{Name: "references/good\nforged.md"}},
	})
	assets, err := src.ListSkillAssets(context.Background(), "bundle")
	if err == nil || assets != nil {
		t.Fatalf("ListSkillAssets(control name) = (%+v, %v), want nil assets and error", assets, err)
	}
}
