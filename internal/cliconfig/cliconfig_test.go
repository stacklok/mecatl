package cliconfig

import (
	"flag"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

// TestRegisterProviderFlagsRegistersThree proves all three base-URL flags are
// registered on the passed FlagSet with the supplied (or defaulted) help text.
func TestRegisterProviderFlagsRegistersThree(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = RegisterProviderFlags(fs, ProviderFlagHelp{})
	for _, name := range []string{"openai-base-url", "openrouter-base-url", "anthropic-base-url"} {
		f := fs.Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s not registered", name)
			return
		}
		if f.Usage == "" {
			t.Errorf("flag --%s has empty help (default fallback should fill it)", name)
		}
	}
	// A zero ProviderFlagHelp uses the mecated-style defaults.
	if got := fs.Lookup("openai-base-url").Usage; got != DefaultProviderFlagHelp.OpenAIBaseURL {
		t.Errorf("openai-base-url help = %q, want the default", got)
	}
}

// TestRegisterProviderFlagsHelpOverride proves a supplied help string wins over the
// default (so mecated/mecatui keep their own wording).
func TestRegisterProviderFlagsHelpOverride(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = RegisterProviderFlags(fs, ProviderFlagHelp{OpenAIBaseURL: "CUSTOM HELP"})
	if got := fs.Lookup("openai-base-url").Usage; got != "CUSTOM HELP" {
		t.Errorf("openai-base-url help = %q, want CUSTOM HELP", got)
	}
	// The unspecified ones still fall back to the default.
	if got := fs.Lookup("anthropic-base-url").Usage; got != DefaultProviderFlagHelp.AnthropicBaseURL {
		t.Errorf("anthropic-base-url help = %q, want the default", got)
	}
}

// TestApplyMapsAllSixFields proves the env keys and the parsed base URLs land on every
// one of the six app.Config fields, for all three providers.
func TestApplyMapsAllSixFields(t *testing.T) {
	t.Setenv(envOpenAIKey, "sk-openai")
	t.Setenv(envOpenRouterKey, "sk-openrouter")
	t.Setenv(envAnthropicKey, "sk-anthropic")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse([]string{
		"--openai-base-url", "https://oai.example",
		"--openrouter-base-url", "https://or.example",
		"--anthropic-base-url", "https://ant.example",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.OpenAIKey != "sk-openai" || cfg.OpenRouterKey != "sk-openrouter" || cfg.AnthropicKey != "sk-anthropic" {
		t.Errorf("keys not mapped: %q / %q / %q", cfg.OpenAIKey, cfg.OpenRouterKey, cfg.AnthropicKey)
	}
	overrides := pf.EndpointOverrides()
	if overrides["openai"].BaseURL != "https://oai.example" || overrides["openrouter"].BaseURL != "https://or.example" || overrides["anthropic"].BaseURL != "https://ant.example" {
		t.Errorf("endpoint overrides not mapped: %#v", overrides)
	}
	if !keys.Any() {
		t.Errorf("returned keys should be present; Any()=%v", keys.Any())
	}
	if keys.OpenAI != "sk-openai" || keys.OpenRouter != "sk-openrouter" || keys.Anthropic != "sk-anthropic" {
		t.Errorf("returned ResolvedKeys mismatch: %+v", keys)
	}
}

// TestApplyEmptyEnvLeavesEmptyFields proves an unset environment yields empty key
// fields (and Any() is false), so a caller's "no provider configured" guard works.
func TestApplyEmptyEnvLeavesEmptyFields(t *testing.T) {
	t.Setenv(envOpenAIKey, "")
	t.Setenv(envOpenRouterKey, "")
	t.Setenv(envAnthropicKey, "")
	// Point XDG_CONFIG_HOME at an empty temp dir so this test is hermetic against
	// whatever the machine running it happens to have at ~/.config/mecatl/auth.yaml
	// (Apply/Resolve now consult it too — an operator auth.yaml on the dev box must
	// not make "no credentials configured" flip to "has credentials").
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cfg app.Config
	keys := pf.Apply(&cfg)

	if cfg.OpenAIKey != "" || cfg.OpenRouterKey != "" || cfg.AnthropicKey != "" {
		t.Errorf("empty env must leave empty key fields; got %q / %q / %q", cfg.OpenAIKey, cfg.OpenRouterKey, cfg.AnthropicKey)
	}
	if overrides := pf.EndpointOverrides(); len(overrides) != 0 {
		t.Errorf("unset endpoint overrides must be empty; got %#v", overrides)
	}
	if keys.Any() {
		t.Error("ResolvedKeys.Any() must be false with no credentials")
	}
}

// TestExplicitlyEmptyBaseURLFlagsRegisterNoOverride proves the flag PRESENT with an empty
// value (`--openai-base-url=`) is equivalent to omitting it entirely: EndpointOverrides
// registers nothing, so the provider keeps its own default endpoint. This is the
// invariant the slack-bot example's docker-compose.yml leans on — it passes
// `--openai-base-url=${OPENAI_BASE_URL:-}`, which Compose interpolates to an empty
// value when the operator configures no gateway. Distinct from
// TestApplyEmptyEnvLeavesEmptyFields, which covers the flags being ABSENT.
func TestExplicitlyEmptyBaseURLFlagsRegisterNoOverride(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	pf := RegisterProviderFlags(fs, ProviderFlagHelp{})
	if err := fs.Parse([]string{
		"--openai-base-url=",
		"--openrouter-base-url=",
		"--anthropic-base-url=",
		"--opencode-base-url=",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if overrides := pf.EndpointOverrides(); len(overrides) != 0 {
		t.Errorf("explicitly empty base-url flags must register no override; got %#v", overrides)
	}
}

// TestReadProviderKeysIsTheSingleSeam proves ReadProviderKeys reads the same three env
// vars Apply uses — the one definition both the guard path and the wiring path share.
func TestReadProviderKeysIsTheSingleSeam(t *testing.T) {
	t.Setenv(envOpenAIKey, "a")
	t.Setenv(envOpenRouterKey, "b")
	t.Setenv(envAnthropicKey, "c")
	t.Setenv(envTypesafeKey, "d")
	keys := ReadProviderKeys()
	if keys.OpenAI != "a" || keys.OpenRouter != "b" || keys.Anthropic != "c" || keys.Typesafe != "d" {
		t.Errorf("ReadProviderKeys mismatch: %+v", keys)
	}
}

func TestTypesafeKeyProjectsToCompositionWithoutAuthYAML(t *testing.T) {
	t.Setenv(envTypesafeKey, "jev-secret")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keys := (&ProviderFlags{}).Resolve()
	var cfg app.Config
	(&ProviderFlags{}).ApplyResolved(&cfg, keys)
	if cfg.TypesafeAPIKey != "jev-secret" {
		t.Fatal("TYPESAFE_API_KEY did not reach app.Config")
	}
}

// TestKeyValueListParsesAndOverrides proves KeyValueList.Set collects repeatable
// key=value pairs with last-write-wins semantics and trims surrounding whitespace
// — the contract both --model-alias and --model-slot rely on.
func TestKeyValueListParsesAndOverrides(t *testing.T) {
	var m KeyValueList
	if err := m.Set("fast=gpt-4o-mini"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Set("fast=gpt-5"); err != nil { // later occurrence overrides
		t.Fatalf("Set override: %v", err)
	}
	if err := m.Set(" smart = gpt-5-pro "); err != nil { // whitespace trimmed
		t.Fatalf("Set trim: %v", err)
	}
	got := map[string]string(m)
	if got["fast"] != "gpt-5" {
		t.Errorf("fast = %q, want gpt-5 (last write wins)", got["fast"])
	}
	if got["smart"] != "gpt-5-pro" {
		t.Errorf("smart = %q, want gpt-5-pro (trimmed)", got["smart"])
	}
}

// TestKeyValueListRejectsMalformed pins the ONE parse-error message both mains
// now share (issue #93: the twin copies had diverged — mecated said "model alias
// must be key=value", mecatui said "must be key=value"). A missing '=' and an
// empty key both fail with the unified message.
func TestKeyValueListRejectsMalformed(t *testing.T) {
	var m KeyValueList
	for _, bad := range []string{"bogus", "=novalue", "  =novalue"} {
		err := m.Set(bad)
		if err == nil {
			t.Errorf("Set(%q) should error", bad)
			continue
		}
		if got := err.Error(); got != `must be key=value, got "`+bad+`"` {
			t.Errorf("Set(%q) error = %q, want the unified message", bad, got)
		}
	}
}

// TestKeyValueListStringIsStable proves String renders a sorted, comma-separated
// list (empty for nil/empty) — the stable form flag's default-value display needs.
func TestKeyValueListStringIsStable(t *testing.T) {
	var m KeyValueList
	if got := m.String(); got != "" {
		t.Errorf("nil String = %q, want empty", got)
	}
	m = KeyValueList{"smart": "gpt-5", "fast": "gpt-4o-mini"}
	if got := m.String(); got != "fast=gpt-4o-mini,smart=gpt-5" {
		t.Errorf("String = %q, want sorted comma-joined", got)
	}
}

// TestKeyValueListAsMapIsNilSafe proves AsMap on a nil pointer returns nil (not
// an empty map), so an unset flag yields the byte-identical default on app.Config.
func TestKeyValueListAsMapIsNilSafe(t *testing.T) {
	var m *KeyValueList
	if got := m.AsMap(); got != nil {
		t.Errorf("nil AsMap = %v, want nil", got)
	}
	m = new(KeyValueList)
	if err := m.Set("fast=gpt-4o-mini"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := m.AsMap()
	if got["fast"] != "gpt-4o-mini" {
		t.Errorf("AsMap = %v, want fast=gpt-4o-mini", got)
	}
}

// TestRegisterModelFlagsRegistersBoth proves RegisterModelFlags registers
// --model-alias and --model-slot on the passed FlagSet with the supplied (or
// defaulted) help text, and returns distinct bindings that parse independently.
func TestRegisterModelFlagsRegistersBoth(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	aliases, slots := RegisterModelFlags(fs, ModelFlagHelp{})
	for _, name := range []string{"model-alias", "model-slot"} {
		if f := fs.Lookup(name); f == nil {
			t.Fatalf("flag --%s not registered", name)
			return
		}
	}
	// A zero ModelFlagHelp uses the mecated-style defaults.
	if got := fs.Lookup("model-alias").Usage; got != DefaultModelFlagHelp.ModelAlias {
		t.Errorf("model-alias help = %q, want the default", got)
	}

	if err := fs.Parse([]string{
		"--model-alias", "fast=gpt-4o-mini",
		"--model-slot", "compaction=cheap",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := aliases.AsMap()["fast"]; got != "gpt-4o-mini" {
		t.Errorf("aliases[fast] = %q, want gpt-4o-mini", got)
	}
	if got := slots.AsMap()["compaction"]; got != "cheap" {
		t.Errorf("slots[compaction] = %q, want cheap", got)
	}
}

// TestRegisterModelFlagsHelpOverride proves a supplied help string wins over the
// default (so mecated/mecatui keep their own wording), with the unspecified one
// still falling back.
func TestRegisterModelFlagsHelpOverride(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_, _ = RegisterModelFlags(fs, ModelFlagHelp{ModelAlias: "CUSTOM HELP"})
	if got := fs.Lookup("model-alias").Usage; got != "CUSTOM HELP" {
		t.Errorf("model-alias help = %q, want CUSTOM HELP", got)
	}
	if got := fs.Lookup("model-slot").Usage; got != DefaultModelFlagHelp.ModelSlot {
		t.Errorf("model-slot help = %q, want the default", got)
	}
}

// TestKeyValueListSatisfiesFlagValue pins the flag.Value interface contract so a
// future refactor can't accidentally widen KeyValueList into something flag.Var
// would reject — the exact contract RegisterModelFlags and both mains rely on.
func TestKeyValueListSatisfiesFlagValue(t *testing.T) {
	var v flag.Value = new(KeyValueList)
	if err := v.Set("k=v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := v.String(); got != "k=v" {
		t.Errorf("String = %q, want k=v", got)
	}
}

// TestRegisterToolhiveLLMFlags_Defaults pins issue #262's R4.1/R4.2: --toolhive-llm
// defaults to true (auto-detect is ON by default), --toolhive-llm-base-url
// defaults to "" (no explicit override), and the help text disambiguates from
// the unrelated --toolhive (MCP workload discovery) flag.
func TestRegisterToolhiveLLMFlags_Defaults(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = RegisterToolhiveLLMFlags(fs, ToolhiveLLMFlagHelp{})

	enableFlag := fs.Lookup("toolhive-llm")
	if enableFlag == nil {
		t.Fatal("flag --toolhive-llm not registered")
	}
	if enableFlag.DefValue != "true" {
		t.Errorf("--toolhive-llm default = %q, want true", enableFlag.DefValue)
	}
	if !strings.Contains(enableFlag.Usage, "unrelated to --toolhive") {
		t.Errorf("--toolhive-llm help missing the --toolhive disambiguation: %q", enableFlag.Usage)
	}

	baseURLFlag := fs.Lookup("toolhive-llm-base-url")
	if baseURLFlag == nil {
		t.Fatal("flag --toolhive-llm-base-url not registered")
	}
	if baseURLFlag.DefValue != "" {
		t.Errorf("--toolhive-llm-base-url default = %q, want empty", baseURLFlag.DefValue)
	}
}

// TestRegisterToolhiveLLMFlags_HelpOverride proves a supplied help string wins,
// mirroring RegisterProviderFlags/RegisterModelFlags (mecatui prefixes "embedded
// server only:").
func TestRegisterToolhiveLLMFlags_HelpOverride(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = RegisterToolhiveLLMFlags(fs, ToolhiveLLMFlagHelp{Enable: "CUSTOM HELP"})
	if got := fs.Lookup("toolhive-llm").Usage; got != "CUSTOM HELP" {
		t.Errorf("toolhive-llm help = %q, want CUSTOM HELP", got)
	}
	if got := fs.Lookup("toolhive-llm-base-url").Usage; got != DefaultToolhiveLLMFlagHelp.BaseURL {
		t.Errorf("toolhive-llm-base-url help = %q, want the default (unspecified field falls back)", got)
	}
}

// TestToolhiveLLMFlags_ApplyMapsBothFields proves parsed flag values land on
// app.Config.
func TestToolhiveLLMFlags_ApplyMapsBothFields(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	tf := RegisterToolhiveLLMFlags(fs, ToolhiveLLMFlagHelp{})
	if err := fs.Parse([]string{"-toolhive-llm=false", "-toolhive-llm-base-url=http://127.0.0.1:9999/v1"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var cfg app.Config
	tf.Apply(&cfg)
	if cfg.ToolhiveLLM {
		t.Error("ToolhiveLLM = true, want false (explicit -toolhive-llm=false)")
	}
	if cfg.ToolhiveLLMBaseURL != "http://127.0.0.1:9999/v1" {
		t.Errorf("ToolhiveLLMBaseURL = %q, want the parsed override", cfg.ToolhiveLLMBaseURL)
	}
}

// TestToolhiveLLMFlags_ApplyNilReceiver mirrors ProviderFlags.Apply's
// nil-receiver discipline: a config built WITHOUT
// RegisterToolhiveLLMFlags leaves app.Config's two fields at their zero
// value (ToolhiveLLM=false, ToolhiveLLMBaseURL="") rather than panicking.
func TestToolhiveLLMFlags_ApplyNilReceiver(t *testing.T) {
	var tf *ToolhiveLLMFlags
	var cfg app.Config
	tf.Apply(&cfg) // must not panic
	if cfg.ToolhiveLLM || cfg.ToolhiveLLMBaseURL != "" {
		t.Errorf("nil-receiver Apply mutated cfg: %+v", cfg)
	}
}
