package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// TestSkillCommandBridgeExpandsSkillBody proves the WHOLE wiring: a discovered
// skill is invocable as /<skill-name> and the expander injects the skill BODY
// (Claude-Code skill-as-slash-command semantics) via the SAME resolved seam the
// Skill tool uses. It exercises the real resolveSkillSeam → buildCommandExpander
// chain over an operator-explicit skills dir (trusted regardless of workspace
// trust, so the bridge lands).
func TestSkillCommandBridgeExpandsSkillBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "deploy the service", "Deploy the service to $ARGUMENTS.")

	cfg := Config{SkillsDirs: []string{dir}, TrustProject: true}
	// resolveSkillSeam is the build-once half buildCatalog runs; buildCommandExpander
	// composes a SkillCommandSource over the stashed inputs. Run the seam, stash the
	// inputs (the buildEngine step), then build the expander.
	seam := resolveFSSkillSeam(context.Background(), cfg)
	if len(seam.metas) != 1 || seam.metas[0].Name != "deploy" {
		t.Fatalf("resolveFSSkillSeam = %+v, want one skill named deploy", seam.metas)
	}
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}

	exp := buildCommandExpander(cfg, nil)
	ws := memfs.NewWorkspace("/proj")
	out, ok, err := exp.Expand(context.Background(), ws, "/deploy staging")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !ok {
		t.Fatal("Expand(/deploy) did not expand; the skill bridge must inject the body")
	}
	// The skill body IS the command template; $ARGUMENTS substitutes the args.
	if out != "Deploy the service to staging." {
		t.Errorf("Expand(/deploy staging) = %q, want the skill body with $ARGUMENTS substituted", out)
	}
}

// TestSkillCommandBridgeUnknownSkillPassesThrough pins that an unknown skill
// name passes through the expander UNCHANGED (found=false is normal) — never an
// error, never a blank substitution.
func TestSkillCommandBridgeUnknownSkillPassesThrough(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "deploy", "Deploy.")
	cfg := Config{SkillsDirs: []string{dir}, TrustProject: true}
	seam := resolveFSSkillSeam(context.Background(), cfg)
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}

	exp := buildCommandExpander(cfg, nil)
	ws := memfs.NewWorkspace("/proj")
	out, ok, err := exp.Expand(context.Background(), ws, "/no-such-skill")
	if err != nil {
		t.Fatalf("Expand(unknown): %v", err)
	}
	if ok {
		t.Error("Expand(/no-such-skill) expanded an unknown skill (should pass through)")
	}
	if out != "/no-such-skill" {
		t.Errorf("Expand(/no-such-skill) = %q, want the input unchanged", out)
	}
}

// TestSkillCommandBridgeLocalCommandShadowsSkill pins the precedence
// dirExp > skillExp: a local command file SHADOWS a same-named skill (a local
// command file is the highest-precedence command surface), and a skill without
// a colliding file still expands.
func TestSkillCommandBridgeLocalCommandShadowsSkill(t *testing.T) {
	skillDir := t.TempDir()
	writeSkill(t, skillDir, "dup", "the skill", "SKILL dup body $ARGUMENTS")

	cfg := Config{SkillsDirs: []string{skillDir}, TrustProject: true}
	seam := resolveFSSkillSeam(context.Background(), cfg)
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}
	// A local command dir that ALSO defines "dup" — the file shadows the skill.
	cfg.CommandsDir = "cmds"
	exp := buildCommandExpander(cfg, nil)

	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "cmds/dup.md", []byte("---\ndescription: file dup\n---\nFILE dup body $ARGUMENTS")); err != nil {
		t.Fatalf("write command file: %v", err)
	}
	ctx := context.Background()

	// The same-named file command shadows the skill.
	out, ok, err := exp.Expand(ctx, ws, "/dup x")
	if err != nil || !ok || out != "FILE dup body x" {
		t.Errorf("Expand(/dup) = (%q, %v, %v), want the FILE body (file shadows skill)", out, ok, err)
	}

	// The palette merges both with first-wins on the collision; the FILE description
	// wins (dirExp precedes skillExp).
	lister, isLister := exp.(prompt.CommandLister)
	if !isLister {
		t.Fatal("composed expander must implement prompt.CommandLister")
	}
	cmds, err := lister.List(ctx, ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "dup" || cmds[0].Description != "file dup" {
		t.Errorf("palette = %+v, want one dup with the FILE description (first-wins)", cmds)
	}
}

// TestSkillCommandBridgeSkillShadowsDriverSource pins the precedence
// skillExp > sourceExp: a skill SHADOWS a same-named driver command, and a
// driver-only command still expands when no skill collides.
func TestSkillCommandBridgeSkillShadowsDriverSource(t *testing.T) {
	skillDir := t.TempDir()
	writeSkill(t, skillDir, "dup", "the skill", "SKILL dup body")

	cfg := Config{SkillsDirs: []string{skillDir}, TrustProject: true}
	seam := resolveFSSkillSeam(context.Background(), cfg)
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}
	// A stashed driver source that ALSO defines "dup" + a driver-only "driver-only".
	cfg.commandSource = stubCommandSource{
		bodies: map[string]string{
			"dup":         "DRIVER dup body",
			"driver-only": "DRIVER body for $1",
		},
		list: []prompt.Command{
			{Name: "driver-only", Description: "driver only"},
			{Name: "dup", Description: "driver dup"},
		},
	}
	exp := buildCommandExpander(cfg, nil)
	ws := memfs.NewWorkspace("/proj")
	ctx := context.Background()

	// The skill shadows the same-named driver command.
	out, ok, err := exp.Expand(ctx, ws, "/dup")
	if err != nil || !ok || out != "SKILL dup body" {
		t.Errorf("Expand(/dup) = (%q, %v, %v), want the SKILL body (skill shadows driver)", out, ok, err)
	}
	// A driver-only command still expands through the driver source.
	out, ok, err = exp.Expand(ctx, ws, "/driver-only y")
	if err != nil || !ok || out != "DRIVER body for y" {
		t.Errorf("Expand(/driver-only) = (%q, %v, %v), want the driver body", out, ok, err)
	}
}

// TestSkillCommandBridgeNoSkillsIsNoOp pins the no-skills path: with no skill
// seam stashed, buildCommandExpander does NOT compose a SkillCommandSource —
// the bridge is a no-op and an unknown /name passes through unchanged. This
// guards the nil-safe contract (don't break the no-skills path).
func TestSkillCommandBridgeNoSkillsIsNoOp(t *testing.T) {
	// No skillCommandInputs stashed (the zero value: nil metas, nil source).
	cfg := Config{}
	exp := buildCommandExpander(cfg, nil)
	if _, isNoop := exp.(prompt.NoopExpander); !isNoop {
		t.Fatalf("no-skills path must be the NoopExpander, got %T", exp)
	}
}

// TestSkillCommandBridgeProjectTierWithheldWhenUntrusted pins the trust gate
// is INHERITED by construction: an untrusted workspace's PROJECT-tier skills
// (the conventional .mecatl/skills / .claude/skills dirs under the workspace)
// never enter the seam, so they never become invocable as /<skill-name>.
// Operator-explicit SkillsDirs and user-tier skills stay invocable regardless
// (they are not project-tier), so the test asserts the SPECIFIC project-tier
// skill is absent, not the full count.
func TestSkillCommandBridgeProjectTierWithheldWhenUntrusted(t *testing.T) {
	ws := t.TempDir()
	// A project-tier skill under the conventional .mecatl/skills dir: the skill
	// lives at <workspace>/.mecatl/skills/<name>/SKILL.md.
	writeSkill(t, filepath.Join(ws, ".mecatl/skills"), "sneaky", "sneaky", "SNEAKY BODY")

	// Untrusted workspace: project tier NOT admitted → the conventional skill
	// is NOT discovered, so the bridge does not see it.
	cfg := Config{Workspace: ws, SkillsConventional: true, TrustProject: false}
	seam := resolveFSSkillSeam(context.Background(), cfg)
	if hasSkillNamed(seam.metas, "sneaky") {
		t.Fatalf("untrusted workspace discovered the project-tier skill 'sneaky' (trust gate leak): %+v", seam.metas)
	}
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}
	exp := buildCommandExpander(cfg, nil)

	wsReader := memfs.NewWorkspace(ws)
	out, ok, err := exp.Expand(context.Background(), wsReader, "/sneaky")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if ok {
		t.Error("untrusted workspace expanded a PROJECT-tier skill as a command (security gap)")
	}
	if out != "/sneaky" {
		t.Errorf("Expand(/sneaky) = %q, want the input unchanged (untrusted skill withheld)", out)
	}
}

// TestSkillCommandBridgeProjectTierAdmittedWhenTrusted is the positive
// counterpart: a TRUSTED workspace's project-tier skill IS discovered and
// invocable as /<skill-name>.
func TestSkillCommandBridgeProjectTierAdmittedWhenTrusted(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, filepath.Join(ws, ".mecatl/skills"), "review", "review", "REVIEW BODY $ARGUMENTS")

	cfg := Config{Workspace: ws, SkillsConventional: true, TrustProject: true}
	seam := resolveFSSkillSeam(context.Background(), cfg)
	if !hasSkillNamed(seam.metas, "review") {
		t.Fatalf("trusted workspace did not discover its project-tier skill 'review': %+v", seam.metas)
	}
	cfg.skillCommandInputs = skillCommandInputs{metas: seam.metas, source: seam.source}
	exp := buildCommandExpander(cfg, nil)

	wsReader := memfs.NewWorkspace(ws)
	out, ok, err := exp.Expand(context.Background(), wsReader, "/review the diff")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !ok {
		t.Fatal("trusted workspace must expand its project-tier skill as a command")
	}
	if out != "REVIEW BODY the diff" {
		t.Errorf("Expand(/review) = %q, want the skill body with $ARGUMENTS substituted", out)
	}
}

// hasSkillNamed reports whether metas contains a skill with the given name.
func hasSkillNamed(metas []tool.SkillMeta, name string) bool {
	for _, m := range metas {
		if m.Name == name {
			return true
		}
	}
	return false
}

// TestSkillCommandBridgeBuildWiresEndToEnd proves the WHOLE Build path wires
// the bridge: app.Build over an operator-explicit skills dir produces an engine
// whose CommandExpander expands /<skill-name> to the skill body. This is the
// one test that catches a dropped stash (e.g. forgetting to set
// cfg.skillCommandInputs after buildCatalog).
func TestSkillCommandBridgeBuildWiresEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "deploy the service", "Deploy the service to $ARGUMENTS.")

	root := t.TempDir()
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:  root,
		Model:      "mock",
		UseMock:    true,
		SkillsDirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// The shared engine's deps were built through baseEngineDeps →
	// engineDepsForProvider → buildCommandExpander, which composed the
	// SkillCommandSource over the stashed seam. The Service exposes the engine's
	// expander indirectly; assert via the command lister RPC the skill is listed.
	created, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := built.Service.ListCommandsForSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	found := false
	for _, c := range got {
		if c.Name == "deploy" {
			found = true
			if c.Description != "deploy the service" {
				t.Errorf("deploy description = %q, want %q", c.Description, "deploy the service")
			}
		}
	}
	if !found {
		t.Fatalf("ListCommands did not list the skill 'deploy' as a command: %+v", got)
	}
}

// ensure the skills import is used (the alias package is referenced via the
// Skill tool registration in other tests; this guards the bridge's re-export).
var _ = skills.ToolName

// TestSkillCommandExpandsThroughEngineLoop is the MUST-FIX 2 loop-level e2e
// proof: Build over an operator skills dir, create a session, send /<skill-name>
// as the user input, and assert the model's recorded user turn carries the
// skill BODY (the expanded command), NOT the raw /<skill-name> invocation.
func TestSkillCommandExpandsThroughEngineLoop(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "deploy the app", "Deploy the service to $ARGUMENTS.")

	llm := mockllm.New(
		mockllm.TextTurn("ok, deploying."),
	)
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		UseMock:             true,
		NoSoul:              true,
		SkillsDirs:          []string{dir},
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-mock"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return llm
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := built.Service.StartRunContent(context.Background(), sess.ID, "/deploy staging", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	// Drain the run; the text turn means no permission ask to answer.
	for range r.Events() {
	}

	reloaded, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	foundExpanded := false
	for _, msg := range reloaded.Conversation.Messages {
		if msg.Role == session.RoleUser && strings.Contains(msg.Text, "/deploy staging") {
			t.Fatalf("raw /deploy invocation leaked into conversation: %q — the expander must rewrite it to the skill body", msg.Text)
		}
		if msg.Role == session.RoleUser && strings.Contains(msg.Text, "Deploy the service to staging.") {
			foundExpanded = true
		}
	}
	if !foundExpanded {
		var texts []string
		for _, msg := range reloaded.Conversation.Messages {
			if msg.Role == session.RoleUser {
				texts = append(texts, msg.Text)
			}
		}
		t.Fatalf("expanded skill body not found in user turns: %q", texts)
	}
}
