package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// fakeSkillSource is an in-memory tool.SkillSource: one skill per key, a body and named assets.
type fakeSkillSource struct {
	skills map[string]fakeSkill
}

type fakeSkill struct {
	body   string
	assets map[string][]byte
}

func (fakeSkillSource) ListSkills(context.Context) ([]tool.SkillMeta, error) { return nil, nil }

func (f fakeSkillSource) SkillBody(_ context.Context, name string) (string, error) {
	skill, ok := f.skills[name]
	if !ok {
		return "", tool.ErrSkillNotFound
	}
	return skill.body, nil
}

func (f fakeSkillSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	skill, ok := f.skills[name]
	if !ok {
		return nil, tool.ErrSkillNotFound
	}
	out := make([]tool.SkillAsset, 0, len(skill.assets))
	for asset, data := range skill.assets { // map order is random on purpose: the server must sort
		out = append(out, tool.SkillAsset{Name: asset, Size: int64(len(data))})
	}
	return out, nil
}

func (f fakeSkillSource) ReadSkillAsset(_ context.Context, name, asset string) ([]byte, error) {
	skill, ok := f.skills[name]
	if !ok {
		return nil, tool.ErrSkillAssetNotFound
	}
	data, ok := skill.assets[asset]
	if !ok {
		return nil, tool.ErrSkillAssetNotFound
	}
	return data, nil
}

func skillFilesService(t *testing.T, source tool.SkillSource) *server.Service {
	t.Helper()
	return skillFilesServiceWith(t, source, nil)
}

func skillFilesServiceWith(t *testing.T, source tool.SkillSource, tweak func(*server.Config)) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	cfg := server.Config{Engine: engine, Store: memstore.New(), Now: func() time.Time { return time.Unix(0, 0) }}
	if source != nil {
		cfg.SkillSourceFor = func(context.Context) tool.SkillSource { return source }
	}
	if tweak != nil {
		tweak(&cfg)
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func deploySkill() fakeSkillSource {
	return fakeSkillSource{skills: map[string]fakeSkill{
		"deploy": {
			body: "# Deploy\nRoll out safely.\n",
			assets: map[string][]byte{
				"references/api.md": []byte("API notes"),
				"examples/b.txt":    []byte("b"),
				"scripts/run.sh":    []byte("#!/bin/sh\n"),
				"data/blob.bin":     {0xff, 0xfe, 0x00},
				"big/huge.txt":      []byte(strings.Repeat("x", 25_001)),
				"edge/exact.txt":    []byte(strings.Repeat("y", 25_000)),
				"data/nul.dat":      []byte("ok\x00ok"), // valid UTF-8, but binary
			},
		},
		"bigbody":   {body: strings.Repeat("b", 25_001)},
		"nulbody":   {body: "ok\x00ok"},
		"emptybody": {body: ""},
	}}
}

// SPEC: ListSkillFiles returns SKILL.md first, then assets sorted by logical name, each with its size.
func TestGRPCListSkillFiles(t *testing.T) {
	client, cleanup := dialGRPC(t, skillFilesService(t, deploySkill()))
	defer cleanup()

	resp, err := client.ListSkillFiles(context.Background(), &mecatlv1.ListSkillFilesRequest{Name: "deploy"})
	if err != nil {
		t.Fatalf("ListSkillFiles: %v", err)
	}
	var got []string
	for _, file := range resp.GetFiles() {
		got = append(got, fmt.Sprintf("%s:%d:%t", file.GetName(), file.GetSize(), file.GetInstructions()))
	}
	// Only the instruction body carries the instructions marker.
	want := []string{
		"SKILL.md:26:true",
		"big/huge.txt:25001:false",
		"data/blob.bin:3:false",
		"data/nul.dat:5:false",
		"edge/exact.txt:25000:false",
		"examples/b.txt:1:false",
		"references/api.md:9:false",
		"scripts/run.sh:10:false",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

// SPEC: a skill outside the caller's view, or skills disabled, is NotFound with a stable code.
func TestGRPCSkillFilesUnknownSkill(t *testing.T) {
	for name, source := range map[string]tool.SkillSource{"unknown skill": deploySkill(), "skills disabled": nil} {
		t.Run(name, func(t *testing.T) {
			client, cleanup := dialGRPC(t, skillFilesService(t, source))
			defer cleanup()
			_, err := client.ListSkillFiles(context.Background(), &mecatlv1.ListSkillFilesRequest{Name: "nope"})
			if status.Code(err) != codes.NotFound {
				t.Fatalf("code = %v, want NotFound (err %v)", status.Code(err), err)
			}
			_, err = client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: "nope", File: "SKILL.md"})
			if status.Code(err) != codes.NotFound {
				t.Fatalf("read code = %v, want NotFound (err %v)", status.Code(err), err)
			}
		})
	}
}

// SPEC: ReadSkillFile returns the body for SKILL.md and an asset's text for its logical name.
func TestGRPCReadSkillFile(t *testing.T) {
	client, cleanup := dialGRPC(t, skillFilesService(t, deploySkill()))
	defer cleanup()
	for file, want := range map[string]string{
		"SKILL.md":          "# Deploy\nRoll out safely.\n",
		"references/api.md": "API notes",
	} {
		resp, err := client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: "deploy", File: file})
		if err != nil {
			t.Fatalf("ReadSkillFile(%q): %v", file, err)
		}
		if resp.GetContent() != want {
			t.Fatalf("content(%q) = %q, want %q", file, resp.GetContent(), want)
		}
	}
}

// SPEC: an invalid logical name is InvalidArgument and never content; an unknown file is NotFound;
// an oversize file and a non-text file are refused, not truncated or repaired.
func TestGRPCReadSkillFileRefusals(t *testing.T) {
	client, cleanup := dialGRPC(t, skillFilesService(t, deploySkill()))
	defer cleanup()
	cases := []struct {
		file string
		want codes.Code
	}{
		{"../etc/passwd", codes.InvalidArgument},
		{"/etc/passwd", codes.InvalidArgument},
		{"references/../api.md", codes.InvalidArgument},
		{"references\\api.md", codes.InvalidArgument},
		{"references/missing.md", codes.NotFound},
		{"big/huge.txt", codes.FailedPrecondition},
		{"data/blob.bin", codes.FailedPrecondition},
		{"data/nul.dat", codes.FailedPrecondition}, // valid UTF-8 with a NUL byte is still binary
	}
	for _, tc := range cases {
		resp, err := client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: "deploy", File: tc.file})
		if status.Code(err) != tc.want {
			t.Errorf("ReadSkillFile(%q) code = %v, want %v (err %v)", tc.file, status.Code(err), tc.want, err)
		}
		if resp.GetContent() != "" {
			t.Errorf("ReadSkillFile(%q) returned content on a refusal", tc.file)
		}
	}
}

// SPEC: the cap is inclusive: a file of exactly the Skill tool's 25,000-byte cap reads in full, and
// one byte more is refused.
func TestGRPCReadSkillFileCapBoundary(t *testing.T) {
	client, cleanup := dialGRPC(t, skillFilesService(t, deploySkill()))
	defer cleanup()
	resp, err := client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: "deploy", File: "edge/exact.txt"})
	if err != nil || len(resp.GetContent()) != 25_000 {
		t.Fatalf("read at the cap: len %d, err %v", len(resp.GetContent()), err)
	}
	if _, err := client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: "deploy", File: "big/huge.txt"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("read one byte over the cap: %v", err)
	}
}

// SPEC: the instruction body obeys the same limits as an asset: an oversize or non-text body is
// refused, and an empty body (a skill with only frontmatter) reads as empty text, not an error.
func TestGRPCReadSkillFileBodyLimits(t *testing.T) {
	client, cleanup := dialGRPC(t, skillFilesService(t, deploySkill()))
	defer cleanup()
	for name, want := range map[string]codes.Code{"bigbody": codes.FailedPrecondition, "nulbody": codes.FailedPrecondition, "emptybody": codes.OK} {
		resp, err := client.ReadSkillFile(context.Background(), &mecatlv1.ReadSkillFileRequest{Name: name, File: "SKILL.md"})
		if status.Code(err) != want {
			t.Errorf("ReadSkillFile(%s, SKILL.md) code = %v, want %v (err %v)", name, status.Code(err), want, err)
		}
		if want != codes.OK && resp.GetContent() != "" {
			t.Errorf("%s: returned content on a refusal", name)
		}
	}
	list, err := client.ListSkillFiles(context.Background(), &mecatlv1.ListSkillFilesRequest{Name: "emptybody"})
	if err != nil || len(list.GetFiles()) != 1 || list.GetFiles()[0].GetSize() != 0 {
		t.Fatalf("empty-body listing = %v, err %v", list.GetFiles(), err)
	}
}

// SPEC: every read goes through the caller's publication preface, like ListSkills: it takes the
// caller partition's publication lock and publishes that caller's learned skills first, and releases
// the lock afterwards.
func TestSkillFileReadsGoThroughThePublicationPreface(t *testing.T) {
	var begun, published, released int
	svc := skillFilesServiceWith(t, deploySkill(), func(cfg *server.Config) {
		cfg.BeginSkillPublication = func(learning.SkillPartition) func() {
			begun++
			return func() { released++ }
		}
		cfg.PublishLearnedSkills = func(context.Context, learning.SkillPartition) error {
			published++
			return nil
		}
	})
	if _, err := svc.ListSkillFiles(context.Background(), "deploy"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadSkillFile(context.Background(), "deploy", "SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if begun != 2 || published != 2 || released != 2 {
		t.Fatalf("begun/published/released = %d/%d/%d, want 2/2/2", begun, published, released)
	}
}

// SPEC: the HTTP routes answer with the same files and content as gRPC, and map the same failures
// to HTTP statuses. File names carry slashes, so they travel as query parameters.
func TestHTTPSkillFiles(t *testing.T) {
	srv := httptest.NewServer(server.NewHTTPHandler(skillFilesService(t, deploySkill())))
	defer srv.Close()

	var list mecatlv1.ListSkillFilesResponse
	if code := httpGet(t, srv, "/v1/skills/files?name=deploy", &list); code != 200 {
		t.Fatalf("list status = %d", code)
	}
	if len(list.GetFiles()) != 8 || list.GetFiles()[0].GetName() != "SKILL.md" {
		t.Fatalf("files = %v", list.GetFiles())
	}

	var read mecatlv1.ReadSkillFileResponse
	path := "/v1/skills/files/read?name=deploy&file=" + url.QueryEscape("references/api.md")
	if code := httpGet(t, srv, path, &read); code != 200 || read.GetContent() != "API notes" {
		t.Fatalf("read status/content = %d %q", code, read.GetContent())
	}

	// Each refusal carries its status AND its stable error code; two refusals can share a status.
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/v1/skills/files":                                               {400, "invalid_argument"}, // name missing
		"/v1/skills/files?name=nope":                                     {404, "skill_not_found"},
		"/v1/skills/files/read?name=deploy":                              {400, "invalid_argument"}, // file missing
		"/v1/skills/files/read?name=deploy&file=..%2Fetc%2Fpasswd":       {400, "invalid_argument"},
		"/v1/skills/files/read?name=deploy&file=references%2Fmissing.md": {404, "skill_file_not_found"},
		"/v1/skills/files/read?name=deploy&file=big%2Fhuge.txt":          {422, "skill_file_too_large"},
		"/v1/skills/files/read?name=deploy&file=data%2Fblob.bin":         {422, "skill_file_not_text"},
		"/v1/skills/files/read?name=deploy&file=data%2Fnul.dat":          {422, "skill_file_not_text"},
	} {
		code, problem := httpGetProblem(t, srv, path)
		if code != want.status || problem != want.code {
			t.Errorf("GET %s = %d %q, want %d %q", path, code, problem, want.status, want.code)
		}
	}
}

// httpGetProblem returns the status and the stable `code` of an RFC 9457 error body.
func httpGetProblem(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Code
}
