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
		learningSubtree(docs),
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
		return "(absent)"
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
			`    - "Bash(go test*)"`,
			"  ask:",
			`    - "Bash(git push*)"`,
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
				"(WebSearch/WebFetch/mcp__*/Bash, enforcing; downgrade via defaultMode: advisory). " +
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

func learningSubtree(docs Docs) *Subtree {
	fields := fieldsOf("LearningSection", permconfig.LearningSection{}, docs)
	fields[0].ExampleValue = "off"
	fields[0].Default = "off"
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
	servers := fields[0]
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
	return &Subtree{
		Key:          "mcp",
		Tier:         TierOperator,
		Doc:          "Strict OPERATOR-TIER named global Streamable HTTP MCP servers. Authentication is a closed none/static_bearer/oauth union; OAuth supports preregistered or CIMD clients and local or environment credentials. All secret-shaped values are MECATL_* environment references, never values in YAML. Project mcp blocks are ignored with a value-free warning.",
		CommentedOut: true,
		Fields:       fields,
		Example: []string{
			"mcp:",
			"  servers:",
			"    - name: public",
			"      url: https://mcp.example.com/public",
			"      auth:",
			"        mode: none",
			"    - name: static_api",
			"      url: https://mcp.example.com/static",
			"      auth:",
			"        mode: static_bearer",
			"        static_bearer:",
			"          token_env: MECATL_MCP_STATIC_TOKEN",
			"    - name: github",
			"      url: https://mcp.example.com/mcp",
			"      auth:",
			"        mode: oauth",
			"        oauth:",
			"          profile: work",
			"          principal: alice@example.com",
			"          issuer: https://id.example.com",
			"          client:",
			"            mode: preregistered",
			"            preregistered:",
			"              id: mecatl-local",
			"              secret_env: MECATL_MCP_GITHUB_CLIENT_SECRET",
			"          scopes: [mcp.read, mcp.write]",
			"          request_refresh_token: true",
			"          credentials:",
			"            mode: local",
			"            local:",
			"              root: /home/alice/.local/state/mecatl/credentials",
			"              key_env: MECATL_MCP_CREDENTIAL_KEY",
			"          network:",
			"            additional_origins: []",
			"            private_origins: []",
			"            max_redirects: 0",
			"    - name: cluster_tools",
			"      url: https://tools.example.com/mcp",
			"      auth:",
			"        mode: oauth",
			"        oauth:",
			"          profile: cluster",
			"          principal: service-account:mecatl",
			"          issuer: https://issuer.example.com",
			"          client:",
			"            mode: cimd",
			"            cimd:",
			"              document_url: https://client.example.com/mecatl.json",
			"          scopes: [mcp.read]",
			"          request_refresh_token: false",
			"          credentials:",
			"            mode: environment",
			"            environment:",
			"              credential_env: MECATL_MCP_CLUSTER_CREDENTIAL",
			"              allow_process_local_refresh: false",
			"          network:",
			"            additional_origins: [https://client.example.com]",
			"            private_origins: []",
			"            max_redirects: 0",
		},
	}
}

func mcpOAuthFields(docs Docs) []*Field {
	fields := fieldsOf("MCPOAuthProfile", permconfig.MCPOAuthProfile{}, docs)
	for _, field := range fields {
		switch field.Key {
		case "client":
			field.Nested = fieldsOf("MCPOAuthClientProfile", permconfig.MCPOAuthClientProfile{}, docs)
			for _, variant := range field.Nested {
				switch variant.Key {
				case "preregistered":
					variant.Nested = fieldsOf("MCPPreregisteredClientProfile", permconfig.MCPPreregisteredClientProfile{}, docs)
				case "cimd":
					variant.Nested = fieldsOf("MCPCIMDClientProfile", permconfig.MCPCIMDClientProfile{}, docs)
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
