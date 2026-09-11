package configgen

import (
	"reflect"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// Docs maps "StructName.FieldName" to a field's doc-comment text. The generator
// harvests it via go/ast (the ONLY go/ast user); BuildModel attaches it to the
// reflected fields. Tests can pass an empty Docs (the structure is still exercised).
type Docs map[string]string

const (
	configDurationType = "duration"
	configAbsent       = "(absent)"
	configRequired     = "(required)"
)

// BuildModel constructs the settings.yaml Model by REFLECTING over the permconfig
// *Section structs (yaml tags + types, in declaration order) and attaching the
// harvested doc-comments, the hand-pinned tier map, and the enable notes / examples.
// It uses reflect ONLY (no go/ast), so it is safe to compile anywhere and is the ONE
// place the surface's shape, semantics, and provenance come together — both the
// generator and the configgen tests call it, so neither can build a different model.
func BuildModel(docs Docs) *Model {
	return &Model{Subtrees: []*Subtree{
		permissionsSubtree(docs),
		guardrailsSubtree(docs),
		postureSubtree(docs),
		reasoningEffortSubtree(docs),
		planModeAutoApproveSubtree(docs),
		providersSubtree(docs),
		llmSubtree(docs),
		providerOverridesSubtree(docs),
		learningSubtree(docs),
		retentionSubtree(docs),
		temporaryStorageSubtree(docs),
		storageManagementSubtree(docs),
		steerSubtree(docs),
		modelsSubtree(docs),
		openRouterSubtree(docs),
		mcpSubtree(docs),
	}}
}

// fieldsOf reflects a struct's fields in declaration order, returning each yaml key +
// rendered type + doc-comment + the rendered zero/default value. Fields with no yaml
// tag (or `yaml:"-"`) are skipped.
func fieldsOf(structName string, v any, docs Docs) []*Field {
	t := reflect.TypeOf(v)
	var out []*Field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		key := strings.Split(tag, ",")[0]
		out = append(out, &Field{
			Key:     key,
			Type:    renderType(sf.Type),
			Doc:     docs[structName+"."+sf.Name],
			Default: zeroDefault(sf.Type),
		})
	}
	return out
}

// zeroDefault renders a field's zero value as the operator-meaningful DEFAULT — the
// value the harness uses when the key is ABSENT. A string zero is `(empty)`, a bool
// `false`, an int `0`, a nil map/slice/pointer / zero nested struct `(absent)` (the key
// was never set). It is the real default, DISTINCT from the skeleton's illustrative
// example values.
func zeroDefault(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "(empty)"
	case reflect.Bool:
		return "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "0"
	default:
		// nil map/slice/pointer and a zero nested struct all mean "not configured".
		return configAbsent
	}
}

// renderType maps a reflect.Type to the short type string the reference shows.
func renderType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return renderType(t.Elem())
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Struct {
			return "[]" + strings.ToLower(t.Elem().Name())
		}
		return "[]" + renderType(t.Elem())
	case reflect.Map:
		return "map[" + renderType(t.Key()) + "]" + renderType(t.Elem())
	case reflect.Struct:
		return strings.ToLower(t.Name())
	default:
		return t.Kind().String()
	}
}

func permissionsSubtree(docs Docs) *Subtree {
	fields := fieldsOf("Permissions", permconfig.Permissions{}, docs)
	for _, f := range fields {
		if f.Key == "subagent" {
			f.Nested = fieldsOf("SubagentPermissions", permconfig.SubagentPermissions{}, docs)
		}
	}
	return &Subtree{
		Key:  "permissions",
		Tier: TierProject,
		Doc: "Allow/ask/deny rule-spec lists. Each entry is \"Tool(pattern)\" or a bare " +
			"\"Tool\". Deny is deny-dominant and binds children too; allow/ask bind the main " +
			"engine; the subagent block binds child engines. A project allow is trust-gated.",
		CommentedOut: true,
		Fields:       fields,
		Example: []string{
			"permissions:",
			"  allow:",
			`    - "Shell(go test*)"`,
			"  ask:",
			`    - "Shell(git push*)"`,
			"  deny:",
			`    - "Read(./.git/**)"`,
		},
	}
}

func guardrailsSubtree(docs Docs) *Subtree {
	fields := fieldsOf("GuardrailsSection", permconfig.GuardrailsSection{}, docs)
	for _, f := range fields {
		switch f.Key {
		case "model":
			f.EnableNote = "Setting a model here ENABLES guardrails (the guardrails-parity " +
				"enable model). A configured model with no rules runs the default BLOCK set " +
				"(WebSearch/WebFetch/mcp__*/Shell, enforcing; downgrade via defaultMode: advisory). " +
				"Leave empty (and pass no --guardrails-model) to keep guardrails OFF."
			f.ExampleValue = "claude-haiku-4-6"
		case "rules":
			f.Nested = fieldsOf("GuardrailRuleSpec", permconfig.GuardrailRuleSpec{}, docs)
		}
	}
	return &Subtree{
		Key:  "guardrails",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER LLM content-checker (issue #27). Parsed strictly. A project-tier " +
			"guardrails: block is IGNORED with a WARN (a project cannot weaken a security checker).",
		EnableNote: "Configuring `model:` ENABLES guardrails; `disabled: true` is the kill-switch " +
			"(the CLI --guardrails=off also sets it).",
		CommentedOut: true,
		Fields:       fields,
	}
}

const configStringType = "string"

func providersSubtree(_ Docs) *Subtree {
	return &Subtree{
		Key:          "providers",
		Tier:         TierOperator,
		CommentedOut: true,
		Doc:          "Strict operator-defined LLM providers. Project-tier definitions are ignored. Provider URLs must be HTTPS without userinfo, query, or fragment; credentials belong only in auth.yaml.",
		Fields: []*Field{{
			Key: "team-gateway", Type: "providerdefinition", Default: configAbsent, ExampleMapKey: "team-gateway",
			Nested: []*Field{
				{Key: "base_url", Type: configStringType, Default: configRequired, ExampleValue: "https://gateway.example/v1"},
				{Key: "default_model", Type: configStringType, Default: configRequired, ExampleValue: "team-chat"},
				{Key: "api_flavor", Type: configStringType, Default: configRequired, ExampleValue: "openai-responses"},
				{Key: "auth", Type: "providerauth", Default: configAbsent, Nested: []*Field{{Key: "method", Type: configStringType, Default: "none", ExampleValue: "api_key"}}},
			},
		}},
	}
}

func llmSubtree(_ Docs) *Subtree {
	trust := func(example string) *Field {
		return &Field{Key: example, Type: "nativetrust", Default: configRequired, Nested: []*Field{
			{Key: "policy", Type: configStringType, Default: configRequired, ExampleValue: "public"},
			{Key: "ca_bundle", Type: configStringType, Default: "(forbidden for public)", ExampleValue: ""},
		}}
	}
	return &Subtree{
		Key: "llm", Tier: TierOperator, CommentedOut: true,
		Doc: "Strict operator-tier native LLM endpoints and their explicit protected credential home. Project values are ignored. Lifecycle commands use exact endpoint IDs and never change provider selection.",
		Fields: []*Field{
			{Key: "credential_home", Type: configStringType, Default: "(required with endpoints)", ExampleValue: "/var/lib/mecatl/provider-oidc"},
			{Key: "credential_key", Type: "nativecredentialkey", Default: "(keyring)", Doc: "Shared encryption-key source for all native endpoints; no automatic fallback or migration. Records always remain encrypted.", Nested: []*Field{
				{Key: "source", Type: configStringType, Default: "keyring", ExampleValue: "environment", Doc: "Closed choice: keyring or environment. Omission of credential_key preserves the OS-keyring default."},
				{Key: "key_env", Type: configStringType, Default: "(required for environment; forbidden for keyring)", ExampleValue: "MECATL_NATIVE_LLM_CREDENTIAL_KEY", Doc: "MECATL_* environment reference containing canonical padded base64 decoding to exactly 32 bytes. Only the reference belongs in settings, never the key value."},
			}},
			{Key: "endpoints", Type: "map[string]nativeendpoint", Default: configAbsent, ExampleMapKey: "corp-gateway", Nested: []*Field{
				{Key: "protocol", Type: configStringType, Default: configRequired, ExampleValue: "openai-responses"},
				{Key: "url", Type: configStringType, Default: configRequired, ExampleValue: "https://gateway.example/v1"},
				{Key: "default_model", Type: configStringType, Default: configRequired, ExampleValue: "corp-model"},
				{Key: "oidc", Type: "nativeoidc", Default: configRequired, Nested: []*Field{
					{Key: "issuer", Type: configStringType, Default: configRequired, ExampleValue: "https://issuer.example"},
					{Key: "client_id", Type: configStringType, Default: configRequired, ExampleValue: "mecatl"},
					{Key: "resource_audience", Type: configStringType, Default: "(empty)", ExampleValue: "https://gateway.example", Doc: "Optional OAuth audience parameter and access-token audience binding. Empty omits both."},
					{Key: "scopes", Type: "[]string", Default: configRequired, ExampleValue: "[models.read, offline_access]"},
				}},
				trust("issuer_trust"), trust("gateway_trust"),
			}},
		},
	}
}

func providerOverridesSubtree(_ Docs) *Subtree {
	return &Subtree{
		Key:          "provider_overrides",
		Tier:         TierOperator,
		CommentedOut: true,
		Doc:          "Strict endpoint overrides for built-in openai, openrouter, anthropic, and opencode only. Codex and ToolHive policies cannot be overridden here.",
		Fields: []*Field{{
			Key: "openai", Type: "provideroverride", Default: configAbsent, ExampleMapKey: "openai",
			Nested: []*Field{{Key: "base_url", Type: configStringType, Default: configRequired, ExampleValue: "https://proxy.example/v1"}},
		}},
	}
}

func retentionSubtree(docs Docs) *Subtree {
	fields := fieldsOf("RetentionSection", permconfig.RetentionSection{}, docs)
	limits := fieldsOf("RetentionLimitSection", permconfig.RetentionLimitSection{}, docs)
	for _, f := range fields {
		switch f.Key {
		case "main", "child", "scheduled":
			f.Nested = limits
		case "version":
			f.Default, f.ExampleValue = "1", "1"
		case "sweep_cadence":
			f.Type, f.Default, f.ExampleValue = configDurationType, "1h", "1h"
		}
	}
	return &Subtree{Key: "retention", Tier: TierOperator, CommentedOut: true,
		Doc: "Versioned automatic session cleanup policy. Operator-tier only; project values are ignored. Zero disables each limit. Explicit compatibility flags outrank these values.", Fields: fields}
}

func temporaryStorageSubtree(docs Docs) *Subtree {
	fields := fieldsOf("TemporaryStorageSection", permconfig.TemporaryStorageSection{}, docs)
	for _, field := range fields {
		switch field.Key {
		case "mode":
			field.Default, field.ExampleValue = "managed", "managed"
		case "managed_root":
			field.Default, field.ExampleValue = "mecatl", "mecatl"
		case "system_temp_dir":
			field.Default, field.ExampleValue = "inherited", ""
		case "command_reap_after", "reap_interval":
			field.Type, field.Default, field.ExampleValue = configDurationType, "1h", "1h"
		case "reap_timeout":
			field.Type, field.Default, field.ExampleValue = configDurationType, "5m", "5m"
		case "shutdown_reap_timeout":
			field.Type, field.Default, field.ExampleValue = configDurationType, "1m", "1m"
		}
	}
	return &Subtree{Key: "temporary_storage", Tier: TierOperator, CommentedOut: true,
		Doc: "Managed command temporary-storage policy. Read only from user-global settings.yaml; project and explicit CLI config values are ignored. Managed mode is Linux-only; system preserves inherited temporary-directory behavior.", Fields: fields}
}

func storageManagementSubtree(docs Docs) *Subtree {
	fields := fieldsOf("StorageManagementSection", permconfig.StorageManagementSection{}, docs)
	principals := fieldsOf("StorageManagementPrincipal", permconfig.StorageManagementPrincipal{}, docs)
	for _, field := range fields {
		switch field.Key {
		case "version":
			field.Default, field.ExampleValue = "1", "1"
		case "principals":
			field.Nested = principals
			principals[0].ExampleValue = "https://idp.example/realms/operators"
			principals[1].ExampleValue = "storage-admin"
		}
	}
	return &Subtree{
		Key: "storage_management", Tier: TierOperator, CommentedOut: true,
		Doc:    "Exact verified OIDC issuer/subject pairs authorized for process-wide storage health, migration, and cleanup. Empty grants nobody; project values are ignored.",
		Fields: fields,
	}
}

func learningSubtree(docs Docs) *Subtree {
	fields := fieldsOf("LearningSection", permconfig.LearningSection{}, docs)
	fields[0].ExampleValue = "off"
	fields[0].Default = "off"
	fields[1].ExampleValue = "balanced"
	fields[1].Default = "balanced"
	skills := fieldsOf("LearningSkillsSection", permconfig.LearningSkillsSection{}, docs)
	skills[0].ExampleValue = "validated"
	skills[0].Default = "validated when mode is explicitly auto; evaluated otherwise"
	fields[2].Nested = skills
	automatic := fieldsOf("LearningAutomaticSection", permconfig.LearningAutomaticSection{}, docs)
	automatic[0].Type, automatic[1].Type = configDurationType, configDurationType
	defaults := []string{"10m", "1h", "8", "100000", "4", "50000"}
	for i := range automatic {
		automatic[i].ExampleValue, automatic[i].Default = defaults[i], defaults[i]
	}
	fields[3].Nested = automatic
	return &Subtree{
		Key: "learning", Tier: TierProject,
		Doc:          "Optional completed-trajectory observation policy. Off means no automatic completed-trajectory reflection or review; project settings may only tighten the operator ceiling off < review < auto. Separately configured consolidation schedules are independent.",
		CommentedOut: true,
		Fields:       fields,
	}
}

func postureSubtree(docs Docs) *Subtree {
	return &Subtree{
		Key:  "posture",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER posture-ladder scalar: strict < trusted < auto < yolo (the graduated " +
			"trust/automation tier). A project-tier posture: is IGNORED with a WARN (a project " +
			"cannot raise the automation posture). Empty = keep the CLI/default.",
		CommentedOut: true,
		Scalar:       true,
		Fields: []*Field{{
			Key:          "posture",
			Type:         "string",
			Default:      "(empty)",
			Doc:          docFor(docs, "Config.Posture", "the posture-ladder tier (strict/trusted/auto/yolo)"),
			ExampleValue: "trusted",
		}},
	}
}

func reasoningEffortSubtree(docs Docs) *Subtree {
	return &Subtree{
		Key:  "reasoning-effort",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER reasoning-effort scalar (ADR 0055): \"\" / \"auto\" (unset — " +
			"the provider default) / \"low\" / \"medium\" / \"high\" / \"xhigh\" / \"max\". " +
			"OpenAI clamps xhigh/max down to high; Anthropic maps all five. A per-session " +
			"CreateSession.reasoning_effort out-ranks this default. A project-tier " +
			"reasoning-effort: is IGNORED with a WARN (a project cannot raise the model's " +
			"reasoning spend). Empty = keep the CLI/default (provider default).",
		CommentedOut: true,
		Scalar:       true,
		Fields: []*Field{{
			Key:          "reasoning-effort",
			Type:         "string",
			Default:      "(empty)",
			Doc:          docFor(docs, "Config.ReasoningEffort", "the reasoning-effort tier (auto/low/medium/high/xhigh/max)"),
			ExampleValue: "high",
		}},
	}
}

func planModeAutoApproveSubtree(docs Docs) *Subtree {
	return &Subtree{
		Key:  "plan-mode-auto-approve",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER plan-mode auto-approve flag (issue #206): when true, a plan-mode " +
			"session that parks awaiting a plan-approval ask is auto-approved (flip to default " +
			"mode and execute) WITHOUT a human reviewing the plan. DEFAULT OFF. A project-tier " +
			"plan-mode-auto-approve: is IGNORED with a WARN (a project cannot grant an autonomous " +
			"approval capability).",
		CommentedOut: true,
		Scalar:       true,
		Fields: []*Field{{
			Key:          "plan-mode-auto-approve",
			Type:         "bool",
			Default:      "false",
			Doc:          docFor(docs, "Config.PlanModeAutoApprove", "auto-approve a presented plan with NO HUMAN REVIEW (default off)"),
			ExampleValue: "true",
		}},
	}
}

func steerSubtree(docs Docs) *Subtree {
	return &Subtree{
		Key:  "steer",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER mid-run steer knob (steer-while-running, issue #512): when true " +
			"(the DEFAULT), a client may inject an operator instruction into an in-flight run, " +
			"drained at the next turn boundary. Set false to disable the steer inbox (the " +
			"capability echo then reads false and a steer frame reports too_late). A " +
			"project-tier steer: is IGNORED with a WARN (the harness's operator surface is not " +
			"a project repo's to flip). Omit = keep the CLI/default (steer ON).",
		CommentedOut: true,
		Scalar:       true,
		Fields: []*Field{{
			Key:          "steer",
			Type:         "bool",
			Default:      "true",
			Doc:          docFor(docs, "Config.Steer", "enable the mid-run steer inbox (default on)"),
			ExampleValue: "false",
		}},
	}
}

func modelsSubtree(docs Docs) *Subtree {
	fields := fieldsOf("ModelsSection", permconfig.ModelsSection{}, docs)
	for _, f := range fields {
		switch f.Key {
		case "default":
			f.ExampleValue = "sonnet"
		case "subagent":
			f.ExampleValue = "coder"
		case "router":
			f.EnableNote = "A non-empty `categories` list ENABLES the router (taxonomy-presence " +
				"enable, ADR 0042 — NOT a CLI enable-flag); `disabled: true` (or " +
				"--subagent-model-router=false) is the kill-switch. Operator-tier only."
			rf := fieldsOf("RouterSection", permconfig.RouterSection{}, docs)
			for _, nf := range rf {
				if nf.Key == "categories" {
					nf.Nested = fieldsOf("RouterCategory", permconfig.RouterCategory{}, docs)
					for _, cf := range nf.Nested {
						switch cf.Key {
						case "name":
							cf.ExampleValue = "refactor"
						case "description":
							cf.ExampleValue = `"large multi-file refactors"`
						case "model":
							cf.ExampleValue = "reasoning"
						}
					}
				}
			}
			f.Nested = rf
		}
	}
	return &Subtree{
		Key:  "models",
		Tier: TierProject,
		Doc: "Per-slot/alias/default model config (ADR 0030) + the operator allowlist cap and " +
			"the semantic Subagent model-router taxonomy (ADR 0031/0042). At the operator tier all " +
			"fields are honoured; a project tier honours slots/aliases/default within the operator " +
			"allowlist on a trusted workspace (router/allowlist are operator-only).",
		CommentedOut: true,
		Fields:       fields,
	}
}

// docFor returns the harvested doc for key, or a fallback when absent.
func docFor(docs Docs, key, fallback string) string {
	if d := docs[key]; d != "" {
		return d
	}
	return fallback
}

func mcpSubtree(docs Docs) *Subtree {
	fields := fieldsOf("MCPSection", permconfig.MCPSection{}, docs)
	var servers *Field
	for _, field := range fields {
		switch field.Key {
		case "servers":
			servers = field
		case "broker":
			field.Nested = fieldsOf("MCPBrokerProfile", permconfig.MCPBrokerProfile{}, docs)
		}
	}
	if servers != nil {
		servers.SkeletonCollapse = true
		servers.Nested = fieldsOf("MCPServerProfile", permconfig.MCPServerProfile{}, docs)
		for _, serverField := range servers.Nested {
			if serverField.Key != "auth" {
				continue
			}
			serverField.Nested = fieldsOf("MCPAuthProfile", permconfig.MCPAuthProfile{}, docs)
			for _, authField := range serverField.Nested {
				switch authField.Key {
				case "static_bearer":
					authField.Nested = fieldsOf("MCPStaticBearerProfile", permconfig.MCPStaticBearerProfile{}, docs)
				case "oauth":
					authField.Nested = mcpOAuthFields(docs)
				}
			}
		}
	}
	return &Subtree{
		Key:          "mcp",
		Tier:         TierOperator,
		Doc:          "Strict OPERATOR-TIER Streamable HTTP MCP authority configuration. Mode selects one mutually exclusive global or session-broker authority; broker mode carries its callback configuration and neutral route declarations. Authentication is a closed none/static_bearer/oauth union. Broker OAuth may use trusted explicit OAuth2 endpoints; all secret-shaped values are MECATL_* environment references, never values in YAML. Project mcp blocks are ignored with a value-free warning.",
		CommentedOut: true,
		Fields:       fields,
		Example: []string{
			"mcp:",
			"  mode: broker",
			"  broker:",
			"    callback_url: https://agent.example/v1/mcp/authorization/callback",
			"  servers:",
			"    - name: docs",
			"      url: https://modelcontextprotocol.io/mcp",
			"      auth:",
			"        mode: none",
			"    - name: github",
			"      url: https://api.githubcopilot.com/mcp/",
			"      auth:",
			"        mode: oauth",
			"        oauth:",
			"          upstream:",
			"            mode: oauth2",
			"            oauth2:",
			"              authorization_endpoint: https://github.com/login/oauth/authorize",
			"              token_endpoint: https://github.com/login/oauth/access_token",
			"          client:",
			"            mode: preregistered",
			"            preregistered:",
			"              id: mecatl-github-mcp",
			"              secret_env: MECATL_GITHUB_MCP_CLIENT_SECRET",
			"          scopes: [repo]",
			"          request_refresh_token: true",
			"          network: {}",
			"          tools:",
			"            - name: get_issue",
			"              description: Read GitHub issue details",
			"              input_schema:",
			"                type: object",
			"                properties:",
			"                  number:",
			"                    type: integer",
			"                required: [number]",
			"              read_only: true",
			"  # Global mode additionally supports static_bearer and OIDC identity profiles:",
			"  # static_bearer:",
			"  #   token_env: MECATL_MCP_STATIC_TOKEN",
			"  # profile: work",
			"  # principal: alice@example.com",
			"  # issuer: https://id.example.com",
			"  # client:",
			"  #   cimd:",
			"  #     document_url: https://client.example.com/mecatl.json",
			"  # DCR is broker-only and requires the explicit oauth2 upstream above:",
			"  # client:",
			"  #   mode: dcr",
			"  #   dcr:",
			"  #     discovery_url: https://auth.example.com/.well-known/oauth-authorization-server",
			"  # credentials:",
			"  #   mode: local",
			"  #   local:",
			"  #     root: /home/operator/.local/state/mecatl/credentials",
			"  #     key_env: MECATL_MCP_CREDENTIAL_KEY",
			"  #   environment:",
			"  #     credential_env: MECATL_MCP_CREDENTIAL_RECORD",
			"  #     allow_process_local_refresh: false",
			"  # network:",
			"  #   additional_origins: []",
			"  #   private_origins: []",
			"  #   max_redirects: 0",
		},
	}
}

func mcpOAuthFields(docs Docs) []*Field {
	fields := fieldsOf("MCPOAuthProfile", permconfig.MCPOAuthProfile{}, docs)
	for _, field := range fields {
		switch field.Key {
		case "upstream":
			field.Nested = fieldsOf("MCPOAuthUpstreamProfile", permconfig.MCPOAuthUpstreamProfile{}, docs)
			for _, variant := range field.Nested {
				if variant.Key == "oauth2" {
					variant.Nested = fieldsOf("MCPOAuth2UpstreamProfile", permconfig.MCPOAuth2UpstreamProfile{}, docs)
				}
			}
		case "client":
			field.Nested = fieldsOf("MCPOAuthClientProfile", permconfig.MCPOAuthClientProfile{}, docs)
			for _, variant := range field.Nested {
				switch variant.Key {
				case "preregistered":
					variant.Nested = fieldsOf("MCPPreregisteredClientProfile", permconfig.MCPPreregisteredClientProfile{}, docs)
				case "cimd":
					variant.Nested = fieldsOf("MCPCIMDClientProfile", permconfig.MCPCIMDClientProfile{}, docs)
				case "dcr":
					variant.Nested = fieldsOf("MCPDCRClientProfile", permconfig.MCPDCRClientProfile{}, docs)
				}
			}
		case "credentials":
			field.Nested = fieldsOf("MCPOAuthCredentialProfile", permconfig.MCPOAuthCredentialProfile{}, docs)
			for _, variant := range field.Nested {
				switch variant.Key {
				case "local":
					variant.Nested = fieldsOf("MCPLocalCredentialProfile", permconfig.MCPLocalCredentialProfile{}, docs)
				case "environment":
					variant.Nested = fieldsOf("MCPEnvironmentCredentialProfile", permconfig.MCPEnvironmentCredentialProfile{}, docs)
				}
			}
		case "network":
			field.Nested = fieldsOf("MCPOAuthNetworkProfile", permconfig.MCPOAuthNetworkProfile{}, docs)
		case "tools":
			field.Nested = fieldsOf("MCPStaticToolProfile", permconfig.MCPStaticToolProfile{}, docs)
		}
	}
	return fields
}

func openRouterSubtree(docs Docs) *Subtree {
	fields := fieldsOf("OpenRouterSection", permconfig.OpenRouterSection{}, docs)
	for _, f := range fields {
		if f.Key == "models" {
			// The per-model routing entries (order + allow_fallbacks). The map value
			// type is OpenRouterModelRoute; render its keys as the nested shape under an
			// illustrative model-id key so the skeleton round-trips the schema.
			f.ExampleMapKey = `"anthropic/claude-sonnet-4-6"`
			rf := fieldsOf("OpenRouterModelRoute", permconfig.OpenRouterModelRoute{}, docs)
			for _, nf := range rf {
				if nf.Key == "order" {
					nf.ExampleValue = `["anthropic", "google-vertex"]`
				}
			}
			f.Nested = rf
		}
	}
	return &Subtree{
		Key:  "openrouter",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER OpenRouter downstream-provider routing (issue #480): a per-model " +
			"preferred DOWNSTREAM provider order, sent as OpenRouter's `provider` request-body " +
			"object. Setting an order disables OpenRouter's default price load-balancing; " +
			"allow_fallbacks: false pins hard to the order. A project-tier openrouter: block is " +
			"IGNORED with a WARN (a project cannot pick the downstream provider).",
		CommentedOut: true,
		Fields:       fields,
		Example: []string{
			"openrouter:",
			"  models:",
			`    "anthropic/claude-sonnet-4-6":`,
			`      order: ["anthropic", "google-vertex"]`,
			"      allow_fallbacks: false",
		},
	}
}
