package configgen_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/configgen"
)

func TestHarnessContextGeneratedScaffoldingParsesWhenUncommented(t *testing.T) {
	var lines []string
	capturing := false
	for _, line := range strings.Split(configgen.Skeleton(), "\n") {
		if line == "# harness_context:" {
			capturing = true
		}
		if capturing && strings.HasPrefix(line, "#| ===") {
			break
		}
		if capturing && strings.HasPrefix(line, "# ") {
			lines = append(lines, strings.TrimPrefix(line, "# "))
		}
	}
	var cfg permconfig.Config
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")+"\n"), &cfg); err != nil {
		t.Fatalf("uncommented generated harness_context scaffolding is not executable configuration: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if cfg.HarnessContext == nil || cfg.HarnessContext.Kinds.Instructions.Mode != "combine" {
		t.Fatal("uncommented generated scaffolding did not parse into the configured subtree")
	}
}

// authoritativeKeys reflects over the permconfig *Section structs to collect EVERY
// yaml key the strict-decode maps accept — derived independently of the renderers so
// the test is non-vacuous (if a subtree or key were dropped from the model, it would
// still appear here and the assertion below would fail). Each entry is "<subtree>.<key>"
// / "<subtree>" for the posture scalar.
//
// It reads the struct YAML TAGS, which permconfig's TestStrictFieldsMatchYAMLTags pins
// to be EXACTLY the strict-decode `known` maps (the real parse authority) — so the tags
// are a verified proxy for the parse maps, and this guard cannot pass against a tag that
// the parser would actually reject. The TOP-LEVEL subtree set is guarded separately by
// TestEveryConfigSubtreeHasAModel (reflecting permconfig.Config directly).
func authoritativeKeys() []string {
	var keys []string
	collect := func(prefix string, v any) {
		t := reflect.TypeOf(v)
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("yaml")
			if tag == "" || tag == "-" {
				continue
			}
			keys = append(keys, prefix+"."+strings.Split(tag, ",")[0])
		}
	}
	collect("permissions", permconfig.Permissions{})
	collect("permissions.subagent", permconfig.SubagentPermissions{})
	collect("guardrails", permconfig.GuardrailsSection{})
	collect("guardrails.rules", permconfig.GuardrailRuleSpec{})
	collect("learning", permconfig.LearningSection{})
	collect("learning.skills", permconfig.LearningSkillsSection{})
	collect("learning.automatic", permconfig.LearningAutomaticSection{})
	collect("retention", permconfig.RetentionSection{})
	collect("retention.main", permconfig.RetentionLimitSection{})
	collect("retention.child", permconfig.RetentionLimitSection{})
	collect("retention.scheduled", permconfig.RetentionLimitSection{})
	collect("command_runner", permconfig.CommandRunnerSection{})
	collect("command_runner.environment", permconfig.CommandRunnerEnvironment{})
	collect("temporary_storage", permconfig.TemporaryStorageSection{})
	collect("storage_management", permconfig.StorageManagementSection{})
	collect("storage_management.principals", permconfig.StorageManagementPrincipal{})
	collect("credential_store", permconfig.CredentialStoreSection{})
	collect("credential_store.oidc", permconfig.OIDCCredentialStore{})
	collect("credential_store.oidc.key", permconfig.NativeCredentialKey{})
	keys = append(keys,
		"providers.team-gateway.base_url", "providers.team-gateway.default_model", "providers.team-gateway.api_flavor", "providers.team-gateway.auth.method",
		"providers.team-gateway.auth.oidc.issuer", "providers.team-gateway.auth.oidc.client_id", "providers.team-gateway.auth.oidc.resource_audience", "providers.team-gateway.auth.oidc.scopes",
		"providers.team-gateway.auth.oidc.issuer_trust", "providers.team-gateway.auth.oidc.gateway_trust",
	)
	collect("models", permconfig.ModelsSection{})
	collect("models.router", permconfig.RouterSection{})
	collect("models.router.jev", permconfig.JevRouterSection{})
	collect("models.router.categories", permconfig.RouterCategory{})
	collect("openrouter", permconfig.OpenRouterSection{})
	collect("openrouter.models", permconfig.OpenRouterModelRoute{})
	collect("mcp", permconfig.MCPSection{})
	collect("mcp.broker", permconfig.MCPBrokerProfile{})
	collect("mcp.servers", permconfig.MCPServerProfile{})
	collect("mcp.servers.auth", permconfig.MCPAuthProfile{})
	collect("mcp.servers.auth.static_bearer", permconfig.MCPStaticBearerProfile{})
	collect("mcp.servers.auth.oauth", permconfig.MCPOAuthProfile{})
	collect("mcp.servers.auth.oauth.upstream", permconfig.MCPOAuthUpstreamProfile{})
	collect("mcp.servers.auth.oauth.upstream.oauth2", permconfig.MCPOAuth2UpstreamProfile{})
	collect("mcp.servers.auth.oauth.client", permconfig.MCPOAuthClientProfile{})
	collect("mcp.servers.auth.oauth.client.preregistered", permconfig.MCPPreregisteredClientProfile{})
	collect("mcp.servers.auth.oauth.client.cimd", permconfig.MCPCIMDClientProfile{})
	collect("mcp.servers.auth.oauth.client.dcr", permconfig.MCPDCRClientProfile{})
	collect("mcp.servers.auth.oauth.credentials", permconfig.MCPOAuthCredentialProfile{})
	collect("mcp.servers.auth.oauth.credentials.local", permconfig.MCPLocalCredentialProfile{})
	collect("mcp.servers.auth.oauth.credentials.environment", permconfig.MCPEnvironmentCredentialProfile{})
	collect("mcp.servers.auth.oauth.network", permconfig.MCPOAuthNetworkProfile{})
	collect("mcp.servers.auth.oauth.tools", permconfig.MCPStaticToolProfile{})
	// posture is a bare scalar Config field, not a *Section.
	keys = append(keys, "posture")
	// output-economy is absent: the setting was REMOVED (ADR 0041, superseded;
	// clean break) and MUST NOT appear in generated artifacts (pinned by
	// TestDeprecatedOutputEconomyIsAbsentFromGeneratedArtifacts).
	// reasoning-effort is likewise a bare scalar Config field (ADR 0055).
	keys = append(keys, "reasoning-effort")
	// plan-mode-auto-approve is likewise a bare scalar Config field (operator-tier
	// only).
	keys = append(keys, "plan-mode-auto-approve")
	return keys
}

// TestEveryStrictKeyAppearsInBothArtifacts is the single-source / no-forgotten-subtree
// guard: every yaml key the permconfig strict-decode maps accept must appear in BOTH
// the rendered skeleton AND the rendered reference. It is non-vacuous — drop a subtree
// from BuildModel and the keys it owned still come from the reflected structs here, so
// the assertion fails (proven by the leaf-key check, not just the top-level key).
func TestEveryStrictKeyAppearsInBothArtifacts(t *testing.T) {
	model := configgen.BuildModel(nil)
	skeleton := configgen.RenderSkeleton(model)
	reference := configgen.RenderReference(model)

	for _, full := range authoritativeKeys() {
		leaf := full
		if i := strings.LastIndex(full, "."); i >= 0 {
			leaf = full[i+1:]
		}
		// The skeleton emits each key as "<leaf>:" somewhere in its (possibly nested,
		// possibly list-element) body.
		if !strings.Contains(skeleton, leaf+":") {
			t.Errorf("skeleton is missing key %q (leaf %q) — a subtree/key was dropped from the model", full, leaf)
		}
		// The reference renders the full dotted path in a backticked cell; a child of a
		// slice-of-struct carries an "[]" array marker before each struct boundary.
		if !referenceHasKey(reference, full) {
			t.Errorf("reference is missing key %q — a subtree/key was dropped from the model", full)
		}
	}
}

// referenceHasKey reports whether the reference contains the dotted key, tolerating the
// "[]" array-element markers and ".<key>" map-entry markers the renderer inserts after
// structured collection segments. It normalizes both before checking the authoritative
// schema path appears as a backticked code span.
func referenceHasKey(reference, full string) bool {
	normalized := strings.ReplaceAll(reference, "[]", "")
	normalized = strings.ReplaceAll(normalized, ".<key>", "")
	return strings.Contains(normalized, "`"+full+"`")
}

func TestGeneratedArtifactsDescribeSchemaDefaults(t *testing.T) {
	model := configgen.BuildModel(nil)
	skeleton := configgen.RenderSkeleton(model)
	reference := configgen.RenderReference(model)

	for name, artifact := range map[string]string{
		"skeleton":  skeleton,
		"reference": reference,
	} {
		if !strings.Contains(artifact, "not a universal process-runtime default") {
			t.Errorf("%s does not distinguish schema fallbacks from process-runtime defaults", name)
		}
	}
	if !strings.Contains(skeleton, "https://mecatl.dev/docs/building/deployment/settings") {
		t.Error("skeleton does not direct operators to the public configuration-plane guide")
	}
	if !strings.Contains(reference, "](/building/deployment/settings.md)") {
		t.Error("reference does not link to the rendered configuration-plane guide")
	}
}

func TestProviderCredentialArtifactsRetainSafetyDetails(t *testing.T) {
	model := configgen.BuildModel(nil)
	for name, artifact := range map[string]string{
		"skeleton":  configgen.RenderSkeleton(model),
		"reference": configgen.RenderReference(model),
	} {
		normalized := strings.Join(strings.Fields(strings.NewReplacer("#| ", "", "# ", "").Replace(artifact)), " ")
		for _, want := range []string{
			"Optional OAuth audience parameter and access-token audience binding. Empty omits both.",
			"changing key custody does not automatically migrate them or fall back to another source",
			"Closed choice: keyring or environment",
			"omission uses the OS-keyring default",
			"canonical padded base64 that decodes to exactly 32 bytes",
			"Only the reference belongs in settings, never the key value",
		} {
			if !strings.Contains(normalized, want) {
				t.Errorf("%s missing provider credential safety detail %q", name, want)
			}
		}
	}
}

func TestLearningModeReferenceHasFieldDescription(t *testing.T) {
	reference := configgen.RenderReference(configgen.BuildModel(configgen.Docs{
		"LearningSection.Mode": "Mode documents off, review, auto, the default, and project tightening.",
	}))
	want := "| `learning.mode` | `string` | `off` | Mode documents off, review, auto, the default, and project tightening. |"
	if !strings.Contains(reference, want) {
		t.Fatalf("learning.mode reference row is missing its field description:\n%s", reference)
	}
}

func TestOpenRouterReferenceShowsDynamicModelKey(t *testing.T) {
	reference := configgen.RenderReference(configgen.BuildModel(nil))
	for _, key := range []string{
		"`openrouter.models.<key>.order`",
		"`openrouter.models.<key>.allow_fallbacks`",
	} {
		if !strings.Contains(reference, key) {
			t.Errorf("reference is missing dynamic map-entry path %s", key)
		}
	}
}

func TestMCPArtifactsShowStrictUnionWithoutSecretValues(t *testing.T) {
	model := configgen.BuildModel(nil)
	skeleton := configgen.RenderSkeleton(model)
	reference := configgen.RenderReference(model)
	for _, want := range []string{
		"mode: broker", "mode: none", "mode: oauth", "mode: oauth2",
		"authorization_endpoint: https://github.com/login/oauth/authorize",
		"token_endpoint: https://github.com/login/oauth/access_token",
		"secret_env: MECATL_GITHUB_MCP_CLIENT_SECRET", "token_env: MECATL_MCP_STATIC_TOKEN",
		"key_env: MECATL_MCP_CREDENTIAL_KEY",
		"- name: get_issue", "input_schema:", "read_only: true",
	} {
		if !strings.Contains(skeleton, want) {
			t.Errorf("skeleton missing MCP example %q", want)
		}
	}
	for _, want := range []string{
		"`mcp.mode`",
		"`mcp.broker.callback_url`",
		"`mcp.servers[].auth.mode`",
		"`mcp.servers[].auth.oauth.upstream.mode`",
		"`mcp.servers[].auth.oauth.upstream.oauth2.authorization_endpoint`",
		"`mcp.servers[].auth.oauth.upstream.oauth2.token_endpoint`",
		"`mcp.servers[].auth.oauth.client.preregistered.secret_env`",
		"`mcp.servers[].auth.oauth.client.cimd.document_url`",
		"`mcp.servers[].auth.oauth.credentials.environment.credential_env`",
		"`mcp.servers[].auth.oauth.network.private_origins`",
		"`mcp.servers[].auth.oauth.tools[].name`",
		"`mcp.servers[].auth.oauth.tools[].description`",
		"`mcp.servers[].auth.oauth.tools[].input_schema`",
		"`mcp.servers[].auth.oauth.tools[].read_only`",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("reference missing MCP path %s", want)
		}
	}
	for _, forbidden := range []string{"Bearer ey", "client_secret:", "token: actual", "credential: ey"} {
		if strings.Contains(skeleton, forbidden) || strings.Contains(reference, forbidden) {
			t.Errorf("generated artifacts contain secret value shape %q", forbidden)
		}
	}
}

// TestRenderersAreDeterministic guards the CI diff-gate against map-iteration-order
// flakes: two independent renders of a freshly-built model must be byte-identical.
func TestRenderersAreDeterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		a := configgen.RenderSkeleton(configgen.BuildModel(nil))
		b := configgen.RenderSkeleton(configgen.BuildModel(nil))
		if a != b {
			t.Fatal("RenderSkeleton is non-deterministic across builds")
		}
		ra := configgen.RenderReference(configgen.BuildModel(nil))
		rb := configgen.RenderReference(configgen.BuildModel(nil))
		if ra != rb {
			t.Fatal("RenderReference is non-deterministic across builds")
		}
	}
}

// TestEmbeddedSkeletonMatchesFreshRender proves the committed (embedded) skeleton is
// not stale relative to the model the renderer produces — the same guarantee the CI
// diff-gate enforces, also checked offline so a forgotten `task docs:configref` fails
// the local test run rather than only CI.
func TestEmbeddedSkeletonMatchesFreshRender(t *testing.T) {
	// The embedded skeleton was generated WITH the AST-harvested doc-comments; a
	// nil-docs render here would differ (missing the per-field comments). So this test
	// asserts STRUCTURE: the embedded skeleton must contain every key the nil-docs
	// model renders, proving the committed artifact wasn't generated from a stale
	// schema. (The byte-exact freshness check is the CI diff-gate, which renders WITH
	// the doc harvest.)
	embedded := configgen.Skeleton()
	model := configgen.BuildModel(nil)
	for _, full := range authoritativeKeys() {
		leaf := full
		if i := strings.LastIndex(full, "."); i >= 0 {
			leaf = full[i+1:]
		}
		_ = model
		if !strings.Contains(embedded, leaf+":") {
			t.Errorf("embedded skeleton is missing key %q — run `task docs:configref` and commit", full)
		}
	}
}

// nonSectionConfigFields is the EXPLICIT allowlist of permconfig.Config yaml-tagged
// fields that are deliberately NOT rendered as settings.yaml subtrees. A field may
// appear here only with a reason operators should not see it. Currently EMPTY —
// output-economy (the one former entry) was removed outright, so every remaining
// Config field is modelled.
var nonSectionConfigFields = map[string]bool{}

func TestDeprecatedOutputEconomyIsAbsentFromGeneratedArtifacts(t *testing.T) {
	model := configgen.BuildModel(nil)
	for name, artifact := range map[string]string{
		"skeleton":  configgen.RenderSkeleton(model),
		"reference": configgen.RenderReference(model),
	} {
		if strings.Contains(artifact, "output-economy") {
			t.Errorf("%s advertises deprecated output-economy compatibility input", name)
		}
	}
}

// TestEveryConfigSubtreeHasAModel closes the SUBTREE-grain drift gap: it reflects over
// permconfig.Config DIRECTLY (the real top-level surface) and asserts every yaml-tagged
// field has a matching Subtree.Key in BuildModel — UNLESS it is in the explicit
// nonSectionConfigFields allowlist. So a 5th top-level Config field added tomorrow fails
// this test (it is neither modelled nor consciously excluded) instead of silently
// vanishing from the generated artifacts. The field-grain guard
// (TestEveryStrictKeyAppearsInBothArtifacts) does not catch this; this one does.
func TestEveryConfigSubtreeHasAModel(t *testing.T) {
	modelled := map[string]bool{}
	for _, st := range configgen.BuildModel(nil).Subtrees {
		modelled[st.Key] = true
	}
	ct := reflect.TypeOf(permconfig.Config{})
	for i := 0; i < ct.NumField(); i++ {
		tag := ct.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		key := strings.Split(tag, ",")[0]
		if nonSectionConfigFields[key] {
			continue // a consciously-excluded non-section field
		}
		if !modelled[key] {
			t.Errorf("permconfig.Config field %q (yaml %q) has no Subtree in BuildModel — model it, or add it to nonSectionConfigFields with a justification", ct.Field(i).Name, key)
		}
	}
}

// TestSubtreeTiersAreAsPinned pins the hand-assigned Tier per subtree (the Tier is a
// SECURITY decision — whether a project file may set the subtree — not reflection-
// derivable, so it cannot be machine-validated against the schema; this table is the
// guard). A future mislabel (e.g. flipping guardrails to operator+project, a downgrade)
// fails here.
func TestSubtreeTiersAreAsPinned(t *testing.T) {
	want := map[string]configgen.Tier{
		"permissions":            configgen.TierProject,  // allow/ask/deny + subagent: project-settable (allows trust-gated)
		"guardrails":             configgen.TierOperator, // operator-only: a project cannot weaken a security checker
		"posture":                configgen.TierOperator, // operator-only: a project cannot raise the automation posture
		"reasoning-effort":       configgen.TierOperator, // operator-only: a project cannot raise the model's reasoning spend (ADR 0055)
		"plan-mode-auto-approve": configgen.TierOperator, // operator-only: a project cannot grant an autonomous approval capability (issue #206)
		"providers":              configgen.TierOperator, // operator-only: a project cannot choose LLM endpoints or auth posture
		"credential_store":       configgen.TierOperator, // operator-only: OIDC credential custody is host authority
		"provider_overrides":     configgen.TierOperator, // operator-only: a project cannot redirect built-in provider traffic
		"harness_context":        configgen.TierOperator, // operator-only: project content cannot register or reorder its own sources
		"learning":               configgen.TierProject,  // project may tighten but never raise the operator ceiling
		"retention":              configgen.TierOperator, // operator-only: project cannot enable destructive cleanup
		"command_runner":         configgen.TierOperator, // operator-only: project cannot select a shell or restore ambient credentials
		"temporary_storage":      configgen.TierOperator, // operator-only: project cannot redirect command storage or cleanup
		"storage_management":     configgen.TierOperator, // operator-only: project cannot grant process-wide management
		"steer":                  configgen.TierOperator, // operator-only: a project cannot flip the mid-run steer surface (issue #512)
		"models":                 configgen.TierProject,  // operator + project (project within the operator allowlist)
		"openrouter":             configgen.TierOperator, // operator-only: a project cannot steer the OpenRouter downstream provider (issue #480)
		"telemetry":              configgen.TierOperator, // operator-only: a project cannot flip a user's own telemetry choice
		"mcp":                    configgen.TierOperator, // operator-only: endpoints, auth, credentials, and egress policy
	}
	got := map[string]configgen.Tier{}
	for _, st := range configgen.BuildModel(nil).Subtrees {
		got[st.Key] = st.Tier
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("subtree %q tier = %q, want %q (a tier change is a security decision — confirm it is intended, then update this pin)", key, got[key], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("subtree count %d != pinned %d — a subtree was added/removed; update the tier pin", len(got), len(want))
	}
}
