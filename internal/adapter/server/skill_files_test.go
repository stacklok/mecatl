package server_test

import (
	"context"
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
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/agent"
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
			},
		},
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
		got = append(got, fmt.Sprintf("%s:%d", file.GetName(), file.GetSize()))
	}
	want := []string{
		"SKILL.md:26",
		"big/huge.txt:25001",
		"data/blob.bin:3",
		"examples/b.txt:1",
		"references/api.md:9",
		"scripts/run.sh:10",
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
		{"big/huge.txt", codes.ResourceExhausted},
		{"data/blob.bin", codes.FailedPrecondition},
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

// The server adapter does not import the skills adapter, so it carries its own copy of the Skill
// tool's output cap. This keeps the two equal.
func TestSkillFileCapMatchesSkillToolCap(t *testing.T) {
	if skillfs.MaxOutputBytes != 25_000 {
		t.Fatalf("skillfs.MaxOutputBytes = %d; update maxSkillFileBytes in skill_files.go to match", skillfs.MaxOutputBytes)
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
	if len(list.GetFiles()) != 6 || list.GetFiles()[0].GetName() != "SKILL.md" {
		t.Fatalf("files = %v", list.GetFiles())
	}

	var read mecatlv1.ReadSkillFileResponse
	path := "/v1/skills/files/content?name=deploy&file=" + url.QueryEscape("references/api.md")
	if code := httpGet(t, srv, path, &read); code != 200 || read.GetContent() != "API notes" {
		t.Fatalf("read status/content = %d %q", code, read.GetContent())
	}

	for path, want := range map[string]int{
		"/v1/skills/files":                                                  400, // name missing
		"/v1/skills/files?name=nope":                                        404,
		"/v1/skills/files/content?name=deploy":                              400, // file missing
		"/v1/skills/files/content?name=deploy&file=..%2Fetc%2Fpasswd":       400,
		"/v1/skills/files/content?name=deploy&file=references%2Fmissing.md": 404,
		"/v1/skills/files/content?name=deploy&file=big%2Fhuge.txt":          413,
		"/v1/skills/files/content?name=deploy&file=data%2Fblob.bin":         422,
	} {
		var ignored mecatlv1.ReadSkillFileResponse
		if code := httpGet(t, srv, path, &ignored); code != want {
			t.Errorf("GET %s status = %d, want %d", path, code, want)
		}
	}
}
