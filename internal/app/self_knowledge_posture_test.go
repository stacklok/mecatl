package app

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// self_knowledge_posture_test.go pins mecatl's account of itself. Answering "what
// are mecatl's permissions in each of its modes?" depends on model behaviour three
// times over: the model must recognise the question is not a codebase search, keep
// the session permission mode distinct from the operator posture ladder, and treat
// https://mecatl.dev/docs/ as authoritative instead of guessing. All three are
// prompt-layer facts, and the rule for anything that depends on model behaviour is
// that the instruction must be in the model's prompt AND a test must prove it
// arrives there through the real factory path. Hence the two wiring tests below:
// deleting either applySelfKnowledgePosture call site must fail CI, because a
// helper tested in isolation stays green while the call site is gone.
//
// The fourth test is the drift gate. The note names doc pages and mode/posture
// tokens, and both are pinned to the live user-docs/ tree and the live domain
// constants rather than trusted to stay accurate by hand.

// selfKnowledgeCatalog builds a catalog occupying just the names
// applySelfKnowledgePosture gates on, so the unit test drives the gate directly.
func selfKnowledgeCatalog(t *testing.T, grep, webFetch bool) *tool.Catalog {
	t.Helper()
	cat := tool.NewCatalog()
	if grep {
		cat.MustRegister(stubTool{name: "Grep"})
	}
	if webFetch {
		cat.MustRegister(stubTool{name: "WebFetch"})
	}
	return cat
}

// TestApplySelfKnowledgePostureClauseGates proves the identity, axis, and doc-map
// clauses are unconditional (every session can be asked about mecatl, including one
// with no filesystem and no network), while the two tool-dependent clauses mirror
// the catalog: the workspace-search warning needs Grep to be actionable, and the
// depth path is the WebFetch variant or the offline variant, never both and never
// neither.
func TestApplySelfKnowledgePostureClauseGates(t *testing.T) {
	t.Parallel()

	always := []string{
		selfKnowledgePostureLead,
		selfKnowledgePostureComponents,
		selfKnowledgePostureAxes,
		selfKnowledgePostureMap,
		selfKnowledgePostureDirect,
	}

	// A nil catalog carries the deployment-independent clauses and neither
	// tool-dependent one — there is no inventory to make a claim against.
	nilCat := applySelfKnowledgePosture(prompt.Config{Role: "custom-role"}, nil)
	if !strings.HasPrefix(nilCat.Role, "custom-role") {
		t.Fatalf("explicit Role was not preserved; got %q", nilCat.Role)
	}
	for _, clause := range always {
		if !strings.Contains(nilCat.Role, clause) {
			t.Errorf("nil catalog dropped an unconditional clause\nmissing=%q", clause)
		}
	}
	for _, clause := range []string{selfKnowledgePostureNoWorkspaceSearch, selfKnowledgePostureFetch, selfKnowledgePostureOffline} {
		if strings.Contains(nilCat.Role, clause) {
			t.Errorf("nil catalog carries a tool-dependent clause it cannot substantiate\nunexpected=%q", clause)
		}
	}

	// Empty Role falls back to DefaultRole (the applyNoFSPosture idiom) rather
	// than shipping a Role that is nothing but the note.
	empty := applySelfKnowledgePosture(prompt.Config{}, selfKnowledgeCatalog(t, true, true))
	if !strings.HasPrefix(empty.Role, prompt.DefaultRole()) {
		t.Fatal("applySelfKnowledgePosture on an empty Config must fall back to DefaultRole")
	}

	// A full catalog: every unconditional clause, the workspace-search warning,
	// and the fetch variant of the depth path.
	full := applySelfKnowledgePosture(prompt.Config{Role: "r"}, selfKnowledgeCatalog(t, true, true))
	for _, clause := range append(append([]string{}, always...), selfKnowledgePostureNoWorkspaceSearch, selfKnowledgePostureFetch) {
		if !strings.Contains(full.Role, clause) {
			t.Errorf("a fully-equipped session is missing a clause\nmissing=%q", clause)
		}
	}
	if strings.Contains(full.Role, selfKnowledgePostureOffline) {
		t.Error("a session WITH WebFetch is told it cannot fetch web pages")
	}

	// No Grep (the "no-fs" profile): the workspace-search warning describes a
	// temptation this session cannot act on, so it is withheld — but the
	// self-account itself must survive.
	noGrep := applySelfKnowledgePosture(prompt.Config{Role: "r"}, selfKnowledgeCatalog(t, false, true))
	for _, clause := range always {
		if !strings.Contains(noGrep.Role, clause) {
			t.Errorf("a search-less session lost its self-account\nmissing=%q", clause)
		}
	}
	if strings.Contains(noGrep.Role, selfKnowledgePostureNoWorkspaceSearch) {
		t.Error("a session with no Grep is warned off a workspace search it cannot run")
	}

	// No WebFetch: the depth path flips to the offline variant. This is the
	// clause pair's whole point — the contract ("never stop at see-the-docs")
	// holds either way, only the mechanism changes.
	noFetch := applySelfKnowledgePosture(prompt.Config{Role: "r"}, selfKnowledgeCatalog(t, true, false))
	if !strings.Contains(noFetch.Role, selfKnowledgePostureOffline) {
		t.Error("a session with no WebFetch is missing the offline depth path")
	}
	if strings.Contains(noFetch.Role, selfKnowledgePostureFetch) {
		t.Error("a session with no WebFetch is told to fetch a page with WebFetch")
	}
}

// TestSelfKnowledgePostureLandsInBuiltEngineSystemPrompt is the wiring gate for
// the per-session factory path. Asserted on the StablePrefix — the Role layer
// applySelfKnowledgePosture owns — rather than the combined Render(), so removing
// the factory wiring fails here: the tool inventory in the rendered prompt also
// names WebFetch and Grep, which would keep a Render() oracle green.
func TestSelfKnowledgePostureLandsInBuiltEngineSystemPrompt(t *testing.T) {
	ctx := context.Background()
	const sessionModel = "gpt-5"

	capture := func(profile server.SessionProfile) prompt.Layered {
		t.Helper()
		var captured prompt.Layered
		var invoked bool
		provider := mockllm.NewWith([]mockllm.Option{
			mockllm.WithRequestObserver(func(req port.LLMRequest) {
				captured = req.System
				invoked = true
			}),
		}, mockllm.TextTurn("ok"))
		cfg := Config{Model: sessionModel}
		reg := regForTest(provider, providerOpenAI, sessionModel)
		factory := sessionEngineFactory(cfg, reg, provider, memstore.New(),
			permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
			prompt.RootAssembler{}, catalogAssets{}, nil)
		res, err := factory(ctx, server.ProviderSelector{}, nil, profile, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory(%q): %v", profile, err)
		}
		defer func() { _ = res.Close() }()

		sess := session.New("s1", session.ModeDefault,
			session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
			session.Limits{MaxTurns: 1}, time.Now())
		run := res.Engine.Run(ctx, sess, memEnvironment("/ws"),
			agent.RunRequest{Text: "what are your permission modes?", Parts: nil})
		for range run.Events() {
		}
		if !invoked {
			t.Fatal("the LLM was not invoked; the mock script may be insufficient")
		}
		return captured
	}

	def := capture(server.ProfileDefault)
	for _, clause := range []string{
		selfKnowledgePostureLead,
		selfKnowledgePostureComponents,
		selfKnowledgePostureAxes,
		selfKnowledgePostureMap,
		selfKnowledgePostureDirect,
		selfKnowledgePostureNoWorkspaceSearch,
		selfKnowledgePostureFetch,
	} {
		if !strings.Contains(def.StablePrefix, clause) {
			t.Errorf("StablePrefix missing a self-knowledge clause (the Role-layer applySelfKnowledgePosture wiring)\nmissing=%q\ngot StablePrefix (first 900):\n%s",
				clause, firstN(def.StablePrefix, 900))
		}
	}

	// The no-fs profile is the honest-absence half, exercised through the REAL
	// catalog rather than a stand-in: no Grep to warn about, WebFetch still
	// present, so the self-account and the fetch path both survive.
	nofs := capture(server.ProfileNoFS)
	for _, clause := range []string{selfKnowledgePostureLead, selfKnowledgePostureComponents, selfKnowledgePostureAxes, selfKnowledgePostureFetch} {
		if !strings.Contains(nofs.StablePrefix, clause) {
			t.Errorf("a no-fs session cannot describe mecatl\nmissing=%q", clause)
		}
	}
	if strings.Contains(nofs.StablePrefix, selfKnowledgePostureNoWorkspaceSearch) {
		t.Error("a no-fs session is warned off a workspace search it has no tool for")
	}
}

// TestSelfKnowledgePostureLandsOnSharedEngine pins the SECOND wiring site. A
// default-profile, zero-selector session on the launch-root workspace rides the
// SHARED engine assembled in buildEngine, not sessionEngineFactory — the plain
// mecatui launch. Without the call there, the commonest deployment is the one that
// cannot account for itself. NoUserModel keeps the session off the per-session
// factory (a resolvable user-model dir builds a learned-skill repository, which
// routes every session through it), so this asserts the shared site and not the
// factory site a second time.
func TestSelfKnowledgePostureLandsOnSharedEngine(t *testing.T) {
	// NOT parallel: t.Setenv below. Neutralising the XDG config base is what
	// actually forces the shared engine — resolveUserModelDir falls back to the
	// real <xdg>/mecatl/usermodel, which builds a learned-skill repository, and a
	// non-nil Config.LearnedSkills routes EVERY session through the per-session
	// factory. Clearing it also keeps the test off the developer's real
	// ~/.config. NoUserModel alone does not do this: it disables the user-model
	// STORE, not the learned-skill repository.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	ctx := context.Background()
	var captured prompt.Layered
	var invoked bool
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			captured = req.System
			invoked = true
		}),
	}, mockllm.TextTurn("ok"))

	// The launch root must be SYMLINK-RESOLVED: Build sets SharedEngineRoot to
	// cfg.Workspace verbatim, while placement verifies the session's root through
	// the filesystem, and sessionNeedsPerFactory compares the two for equality. On
	// macOS t.TempDir() hands back /var/... which resolves to /private/var/..., so
	// an unresolved root silently routes this session through the PER-SESSION
	// factory and the test stops gating the shared engine at all (proven by
	// mutation: with an unresolved root, deleting the buildEngine call site leaves
	// this test green).
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	built, err := Build(ctx, Config{
		Workspace:    ws,
		Model:        "mock",
		StoreDir:     t.TempDir(),
		NoUserModel:  true,
		MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(ctx, sess.ID, "what posture am I running under?", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	if !invoked {
		t.Fatal("the LLM was not invoked; the mock script may be insufficient")
	}
	for _, clause := range []string{
		selfKnowledgePostureLead,
		selfKnowledgePostureComponents,
		selfKnowledgePostureAxes,
		selfKnowledgePostureMap,
		selfKnowledgePostureDirect,
		selfKnowledgePostureNoWorkspaceSearch,
		selfKnowledgePostureFetch,
	} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Errorf("the SHARED engine's StablePrefix is missing a self-knowledge clause — applySelfKnowledgePosture must run on the shared engine's deps in buildEngine\nmissing=%q", clause)
		}
	}
}

// docPathRef matches every /docs/... page the note points the model at.
var docPathRef = regexp.MustCompile(`/docs/[a-z0-9/-]*`)

// TestSelfKnowledgePostureNamesLiveDocPages is the single-source-of-truth gate:
// whatever ships in the prompt must not become a second hand-maintained copy of
// user-docs/ that silently drifts from it. The note is deliberately not a prose
// copy of user-docs/ — it carries structural facts and delegates the rest to the
// documentation — but two kinds of structural fact CAN still drift silently: a doc
// page that moves or is renamed, and a permission mode or posture tier that is
// renamed in the domain. Both are pinned here against the live tree and the live
// constants, so the drift fails CI in this package instead of surfacing as a
// confidently wrong answer at runtime.
func TestSelfKnowledgePostureNamesLiveDocPages(t *testing.T) {
	t.Parallel()

	note := strings.Join([]string{
		selfKnowledgePostureLead,
		selfKnowledgePostureComponents,
		selfKnowledgePostureAxes,
		selfKnowledgePostureMap,
		selfKnowledgePostureDirect,
		selfKnowledgePostureNoWorkspaceSearch,
		selfKnowledgePostureFetch,
		selfKnowledgePostureOffline,
	}, "\n\n")

	// user-docs/ is the canonical source the mecatl.dev site renders; the site
	// serves it under routeBasePath /docs.
	docsRoot := filepath.Join("..", "..", "user-docs")
	if _, err := os.Stat(docsRoot); err != nil {
		t.Fatalf("user-docs/ not reachable from this package (%v) — fix the path, do not delete the gate", err)
	}

	seen := map[string]bool{}
	pages := 0
	for _, ref := range docPathRef.FindAllString(note, -1) {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		rel := strings.TrimPrefix(ref, "/docs/")
		// The bare site root (the https://mecatl.dev/docs/ authority reference)
		// names no page and always resolves; only deeper refs are checked.
		if rel == "" {
			continue
		}
		pages++
		// A trailing slash names a section index; anything else is a page, which
		// Docusaurus serves from either <path>.md or <path>/index.md.
		var candidates []string
		if strings.HasSuffix(rel, "/") {
			candidates = []string{filepath.Join(docsRoot, rel, "index.md")}
		} else {
			candidates = []string{
				filepath.Join(docsRoot, rel+".md"),
				filepath.Join(docsRoot, rel, "index.md"),
			}
		}
		found := false
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				found = true
				break
			}
		}
		if !found {
			sort.Strings(candidates)
			t.Errorf("the note points the model at %q, which has no page under user-docs/ (tried %v) — update the note when a doc page moves",
				ref, candidates)
		}
	}
	if pages == 0 {
		t.Error("the self-knowledge note names no specific /docs/ page: the model has nothing to escalate to but the site root")
	}

	// The axis VALUE sets must match the domain, not a remembered spelling of it.
	for _, mode := range []session.PermissionMode{session.ModeDefault, session.ModePlan, session.ModeAccept} {
		if !strings.Contains(note, string(mode)) {
			t.Errorf("permission mode %q is not named in the self-knowledge note — a session can be in a mode the note cannot explain", mode)
		}
	}
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		if !strings.Contains(note, posture.String()) {
			t.Errorf("posture tier %q is not named in the self-knowledge note", posture)
		}
	}

	// Every shipped component must be named, in BOTH directions. A question that
	// says "mecatui" or "mecak8s" instead of "mecatl" has to be recognised as a
	// question about the model itself, so a renamed binary must not leave the note
	// answering for a component that no longer exists, and a NEW binary must not
	// ship unnamed and fall through to a search. cmd/ is the inventory.
	//
	// mecademo is deliberately excluded: it is the offline demonstration of the
	// loop, not a component anyone deploys or asks about. Excluding a future entry
	// is a deliberate edit here, which is the point.
	entries, err := os.ReadDir(filepath.Join("..", "..", "cmd"))
	if err != nil {
		t.Fatalf("read cmd/ (the component inventory): %v", err)
	}
	const notAComponent = "mecademo"
	named := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == notAComponent {
			continue
		}
		if !strings.Contains(note, e.Name()) {
			t.Errorf("component %q ships under cmd/ but is not named in the self-knowledge note: a question naming it would not be recognised as a question about mecatl itself", e.Name())
			continue
		}
		named++
	}
	if named == 0 {
		t.Fatal("no component names were checked; the cmd/ inventory lookup is broken, not satisfied")
	}
}
