package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type classificationNamedTool string

func (n classificationNamedTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: string(n)} }
func (classificationNamedTool) ReadOnly() bool        { return true }
func (classificationNamedTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

func TestCallerSeparation_ClassificationKindsMatchRealRegistrationContext(t *testing.T) {
	var got map[string]server.ClassificationEntry
	built, err := Build(context.Background(), Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		NoSoul:              true,
		MockProvider:        mockllm.New(),
		EnableParallel:      true,
		EnableTeams:         true,
		Shell:               "/bin/sh",
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		catalogClassificationObserver: func(entries map[string]server.ClassificationEntry) {
			got = entries
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	want := map[string]server.AccessKind{
		"Subagent":        server.KindCallerOwned,
		"InspectSubagent": server.KindCallerOwned,
		"InspectMember":   server.KindCallerOwned,
		"ShellStatus":     server.KindDerived,
		"SubagentStatus":  server.KindDerived,
		"Parallel":        server.KindDerived,
		"Team":            server.KindDerived,
		"PresentPlan":     server.KindDerived,
	}
	for name, kind := range want {
		if entry, ok := got[name]; !ok || entry.Kind != kind {
			t.Errorf("%s classification = %+v, present=%v; want kind %v", name, entry, ok, kind)
		}
	}
}

func TestCallerSeparation_SkillClassificationIsContextual(t *testing.T) {
	cases := []struct {
		name    string
		tool    tool.Tool
		assets  catalogAssets
		session catalogSession
		want    server.AccessKind
	}{
		{name: "static skill", tool: classificationNamedTool("Skill"), want: server.KindExempt},
		{name: "caller-partitioned skill", tool: classificationNamedTool("Skill"), session: catalogSession{skillPartitions: []learning.SkillPartition{{}}}, want: server.KindCallerOwned},
		{name: "legacy draft", tool: classificationNamedTool("SkillDraft"), want: server.KindExempt},
		{name: "lifecycle draft", tool: classificationNamedTool("SkillDraft"), assets: catalogAssets{learnedSkills: memskill.New()}, want: server.KindCallerOwned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := skillToolClassification(tc.tool, tc.assets, tc.session)
			if !ok || entry.Kind != tc.want {
				t.Fatalf("classification = %+v, ok=%v; want kind %v", entry, ok, tc.want)
			}
		})
	}
}

func TestCallerSeparation_ClassificationFailureRunsOwnedCleanup(t *testing.T) {
	classified := newClassifiedCatalog()
	classified.catalog.MustRegister(&blockingSteerTool{started: make(chan struct{}), release: make(chan struct{})})
	closed := false
	defer func() {
		if recover() == nil {
			t.Fatal("classification failure did not panic")
		}
		if !closed {
			t.Fatal("classification failure did not release acquired catalog resources")
		}
	}()
	mustValidateClassifiedCatalog(classified, "cleanup fixture", func() error {
		closed = true
		return nil
	})
}

func TestCallerSeparation_RegisteredUnclassifiedToolFailsRealCatalogGuard(t *testing.T) {
	block := &blockingSteerTool{started: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Build did not reject an actually registered unclassified tool")
		}
		message := fmt.Sprint(recovered)
		if !strings.Contains(message, "unclassified access boundary") || !strings.Contains(message, "SteerBlock") {
			t.Fatalf("panic = %q, want classification failure naming SteerBlock", message)
		}
	}()

	_, _ = Build(context.Background(), Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		NoSoul:              true,
		MockProvider:        mockllm.New(),
		extraCoreTools:      []tool.Tool{block},
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
}

func TestCallerSeparation_RealCatalogClassifiesAllMemoryLifecycleTools(t *testing.T) {
	var got map[string]server.ClassificationEntry
	built, err := Build(context.Background(), Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		NoSoul:              true,
		MockProvider:        mockllm.New(),
		MemoryDir:           t.TempDir(),
		UserModelDir:        t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		catalogClassificationObserver: func(entries map[string]server.ClassificationEntry) {
			got = entries
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	for _, name := range []string{
		"Remember", "Recall", "SearchMemory", "InspectMemory", "ForgetMemory", "UndoMemory",
		"RememberUser", "RecallUser", "SearchUserModel", "InspectUserMemory", "ForgetUserMemory", "UndoUserMemory",
	} {
		entry, ok := got[name]
		if !ok {
			t.Errorf("real catalog classification missing %q", name)
			continue
		}
		if entry.Kind != server.KindCallerOwned {
			t.Errorf("%s kind = %q, want %q", name, entry.Kind, server.KindCallerOwned)
		}
	}
}
