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
		outputEconomySubtree(docs),
		reasoningEffortSubtree(docs),
		planModeAutoApproveSubtree(docs),
		modelsSubtree(docs),
		schedulesSubtree(docs),
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

func outputEconomySubtree(docs Docs) *Subtree {
	return &Subtree{
		Key:  "output-economy",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER output-economy scalar (ADR 0041): \"\" / \"normal\" / \"terse\". " +
			"\"terse\" slims the agent's prose (the ladder + prose scope + safety carveout); " +
			"a project-tier output-economy: is IGNORED with a WARN (a project cannot raise " +
			"the automation posture). Empty = keep the CLI/default tone.",
		CommentedOut: true,
		Scalar:       true,
		Fields: []*Field{{
			Key:          "output-economy",
			Type:         "string",
			Default:      "(empty)",
			Doc:          docFor(docs, "Config.OutputEconomy", "the output-economy tier (normal/terse)"),
			ExampleValue: "terse",
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

func schedulesSubtree(docs Docs) *Subtree {
	// SchedulesSection is a bare sequence (no mapping keys), so the subtree's fields
	// ARE the ScheduleDecl element fields — there is no `items:` wrapper. Model them
	// directly so the skeleton renders `- name: ...` list elements and the reference
	// table shows `schedules[].name` etc.
	fields := fieldsOf("ScheduleDecl", permconfig.ScheduleDecl{}, docs)
	return &Subtree{
		Key:  "schedules",
		Tier: TierOperator,
		Doc: "OPERATOR-TIER scheduled-tasks declarations (issue #233, Phase 2b): a list of " +
			"schedules the harness upserts into the durable ScheduleStore at startup " +
			"(missing → Create, differing → Update, unchanged → no-op; removed schedules " +
			"are NOT deleted). A project-tier schedules: block is IGNORED with a WARN " +
			"(a project repo cannot register schedules).",
		EnableNote: "Each list entry is one schedule; exactly one of `cron` / `oneShot` is " +
			"required. The harness must be started with --scheduler for declared schedules " +
			"to be reconciled and fired.",
		CommentedOut: true,
		ListBody:     true,
		Fields:       fields,
		Example: []string{
			"schedules:",
			"  - name: nightly-review",
			`    cron: "0 9 * * *"`,
			`    timezone: "UTC"`,
			`    prompt: "Review open PRs and post a summary"`,
			"    provider: openrouter",
			"    model: anthropic/claude-sonnet-4",
			"    mutating: false",
			"    maxTurns: 20",
			"    singleton: true",
		},
	}
}
