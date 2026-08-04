// Package cliconfig holds the small slices of CLI/composition wiring that the four
// command mains (cmd/mecated, cmd/mecatui, cmd/mecatequi, cmd/mecak8s) would otherwise
// copy-paste — extracted here so they cannot drift apart. It is a CMD-SIDE composition
// helper: it may read the process environment (os.Getenv) and register flags
// (flag.FlagSet), then apply the resolved values onto an app.Config.
//
// Why it lives in internal/ and not in internal/app: app.Build deliberately reads the
// environment ONLY through its injected envDetector seam (see internal/app/build.go),
// so the os.Getenv reads for provider credentials belong OUTSIDE app — in the cmd layer
// or a cmd-side helper like this one. The dependency direction stays inward
// (cmd -> cliconfig -> app); cliconfig never imports a cmd main. The credentials-FILE
// parsing itself (auth.yaml) lives one layer further out, in the small leaf adapter
// internal/adapter/authfile, so a future credential-writing subcommand can depend on
// that schema directly without pulling in cliconfig's flag/model-alias machinery too;
// cliconfig only wires it (path resolution + precedence against the environment).
package cliconfig

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
)

// knownAuthProviders is the closed set of provider names an auth.yaml entry
// may use — passed into authfile.Load so that package stays agnostic of which
// providers mecatl specifically knows about.
var knownAuthProviders = []string{"anthropic", "openai", "openrouter", "opencode"}

// Provider credential / base-URL environment variables. These are the SECRET-shaped
// inputs the cmd layer reads on the operator's behalf (the registry also auto-detects
// them via its envDetector, but reading them here makes credential custody explicit and
// keeps it identical across the three mains). They are NEVER logged or printed.
const (
	envOpenAIKey     = "OPENAI_API_KEY"
	envOpenRouterKey = "OPENROUTER_API_KEY"
	envAnthropicKey  = "ANTHROPIC_API_KEY"
	envOpenCodeKey   = "OPENCODE_API_KEY"
)

// ProviderFlagHelp carries the per-main help text for the three provider base-URL
// flags. The three mains word these slightly differently (mecated is the daemon;
// mecatui prefixes "embedded server only:"), so the help is passed in rather than
// hard-coded — keeping each main's --help BYTE-IDENTICAL across the extraction. A
// zero ProviderFlagHelp falls back to DefaultProviderFlagHelp (the mecated wording),
// which is what a new consumer (mecatequi) uses.
type ProviderFlagHelp struct {
	OpenAIBaseURL     string
	OpenRouterBaseURL string
	AnthropicBaseURL  string
	OpenCodeBaseURL   string
}

// DefaultProviderFlagHelp is the mecated-style wording, used when a field of the passed
// ProviderFlagHelp is empty. It is the right default for a fresh consumer (mecatequi).
var DefaultProviderFlagHelp = ProviderFlagHelp{
	OpenAIBaseURL:     "override the OpenAI API base URL (compatible endpoints)",
	OpenRouterBaseURL: "override the OpenRouter API base URL (default https://openrouter.ai/api/v1; key from OPENROUTER_API_KEY)",
	AnthropicBaseURL:  "override the native Anthropic API base URL (compatible/proxy endpoints; key from ANTHROPIC_API_KEY)",
	OpenCodeBaseURL:   "override the OpenCode Go API base URL (default https://opencode.ai/zen/go/v1; key from OPENCODE_API_KEY)",
}

// ProviderFlags holds the values bound by RegisterProviderFlags. The base-URL fields
// are populated by flag parsing; the key fields are populated by Apply (read from the
// environment, then from an auth.yaml credentials file) so a key never has to
// round-trip through the process argv. It is the ONE place the three provider
// credentials + base URLs are wired onto app.Config, so the six fields can never again
// be partially wired (the bug that left mecatequi unable to reach Anthropic /
// OpenRouter).
type ProviderFlags struct {
	openAIBaseURL     *string
	openRouterBaseURL *string
	anthropicBaseURL  *string
	openCodeBaseURL   *string
	authFile          *string
}

// RegisterProviderFlags registers --openai-base-url / --openrouter-base-url /
// --anthropic-base-url / --auth-file on fs and returns the binding to pass to Apply
// later. The help text comes from help, falling back per-field to
// DefaultProviderFlagHelp so a caller may pass a zero value (or override only the
// fields it words differently).
func RegisterProviderFlags(fs *flag.FlagSet, help ProviderFlagHelp) *ProviderFlags {
	help = help.withDefaults()
	pf := &ProviderFlags{
		openAIBaseURL:     new(string),
		openRouterBaseURL: new(string),
		anthropicBaseURL:  new(string),
		openCodeBaseURL:   new(string),
		authFile:          new(string),
	}
	fs.StringVar(pf.openAIBaseURL, "openai-base-url", "", help.OpenAIBaseURL)
	fs.StringVar(pf.openRouterBaseURL, "openrouter-base-url", "", help.OpenRouterBaseURL)
	fs.StringVar(pf.anthropicBaseURL, "anthropic-base-url", "", help.AnthropicBaseURL)
	fs.StringVar(pf.openCodeBaseURL, "opencode-base-url", "", help.OpenCodeBaseURL)
	fs.StringVar(pf.authFile, "auth-file", "",
		"path to a YAML credentials file (providers.<name>.api_key for anthropic/openai/openrouter/opencode); "+
			"overrides the conventional default $XDG_CONFIG_HOME/mecatl/auth.yaml (usually ~/.config/mecatl/auth.yaml, "+
			"a settings.yaml sibling). A credential already present in the environment always wins over this file "+
			"for that provider")
	return pf
}

// Apply reads the three provider-credential environment variables, then fills any
// still-empty credential from an auth.yaml credentials file (the explicit --auth-file
// path, or the conventional $XDG_CONFIG_HOME/mecatl/auth.yaml default), and writes all
// six app.Config values (3 keys + 3 base URLs) onto cfg. It is safe to call exactly
// once after fs has been parsed. It returns the resolved keys so a caller that needs to
// make a presence decision (mecated flips UseOpenAI on a present key; mecatui's "no
// provider at all" guard) can read them without re-querying the environment — but it
// NEVER logs them. A non-empty ResolvedKeys.AuthFileWarning should be surfaced by the
// caller (cmd/ mains: slog.Warn) — see loadAuthFile for when it fires.
func (pf *ProviderFlags) Apply(cfg *app.Config) ResolvedKeys {
	keys := ReadProviderKeys()

	// A nil receiver (a config built WITHOUT RegisterProviderFlags — e.g. a test that
	// constructs the cmd config struct directly) has no --auth-file flag to read, so it
	// falls through to the conventional default path exactly like an unset flag would.
	explicitPath := ""
	if pf != nil {
		explicitPath = *pf.authFile
	}
	path := explicitPath
	if path == "" {
		path = authfile.DefaultPath(xdgconfig.OSEnv)
	}
	af, warning := authfile.Load(path, explicitPath != "", xdgconfig.OSEnv, knownAuthProviders)
	keys.AuthFileWarning = warning
	// The environment always wins: cmp.Or keeps a credential already present and
	// falls back to the file only when the environment left it empty, so a
	// deployment that only ever used env vars sees byte-identical behavior
	// whether or not an auth.yaml happens to exist.
	keys.OpenAI = cmp.Or(keys.OpenAI, af.APIKey("openai"))
	keys.OpenRouter = cmp.Or(keys.OpenRouter, af.APIKey("openrouter"))
	keys.Anthropic = cmp.Or(keys.Anthropic, af.APIKey("anthropic"))
	keys.OpenCode = cmp.Or(keys.OpenCode, af.APIKey("opencode"))

	cfg.OpenAIKey = keys.OpenAI
	cfg.OpenRouterKey = keys.OpenRouter
	cfg.AnthropicKey = keys.Anthropic
	cfg.OpenCodeKey = keys.OpenCode
	// A nil receiver (a config built WITHOUT RegisterProviderFlags — e.g. a test that
	// constructs the cmd config struct directly) applies the env/auth-file keys but
	// leaves the base URLs at their zero value, exactly as the pre-extraction inline
	// code did when the base-url flags were never set. This keeps embeddedConfig/
	// appConfig safe to call on a hand-built config.
	if pf != nil {
		cfg.OpenAIBaseURL = *pf.openAIBaseURL
		cfg.OpenRouterBaseURL = *pf.openRouterBaseURL
		cfg.AnthropicBaseURL = *pf.anthropicBaseURL
		cfg.OpenCodeBaseURL = *pf.openCodeBaseURL
	}
	return keys
}

// ReadProviderKeys reads the three provider credentials from the environment. It is the
// SINGLE definition of which env vars hold which credential — both Apply and any caller
// that must make a credential-presence decision BEFORE an app.Config exists (e.g.
// mecatui's "no provider configured" startup guard) read through it, so the var names
// can never diverge between the guard path and the wiring path. The values are
// SECRET-shaped; callers must not log or print them.
func ReadProviderKeys() ResolvedKeys {
	return ResolvedKeys{
		OpenAI:     os.Getenv(envOpenAIKey),
		OpenRouter: os.Getenv(envOpenRouterKey),
		Anthropic:  os.Getenv(envAnthropicKey),
		OpenCode:   os.Getenv(envOpenCodeKey),
	}
}

// ResolvedKeys is the set of provider credentials resolved by Apply (environment, then
// auth.yaml). It lets a caller branch on credential presence (e.g. "an OpenAI key
// implies the user wants the real provider") without a second os.Getenv. The four key
// fields are SECRET-shaped: callers must not log or print them.
type ResolvedKeys struct {
	OpenAI     string
	OpenRouter string
	Anthropic  string
	OpenCode   string
	// AuthFileWarning is non-empty when the auth.yaml credentials file (the explicit
	// --auth-file path, or the conventional default) could not be read or parsed
	// cleanly. It is set only by Apply (ReadProviderKeys alone never touches the file).
	// Never fatal — Apply always falls back to whatever was resolved from the
	// environment — but a caller should log it (cmd/ mains: slog.Warn) so a typo in
	// auth.yaml doesn't fail silently. Not secret-shaped: it names the file path and the
	// problem, never a key value.
	AuthFileWarning string
}

// Any reports whether at least one provider credential is present. It is the shared
// "is any real provider configured?" predicate (mecatui uses it for its startup guard).
func (k ResolvedKeys) Any() bool {
	return k.OpenAI != "" || k.OpenRouter != "" || k.Anthropic != "" || k.OpenCode != ""
}

func (h ProviderFlagHelp) withDefaults() ProviderFlagHelp {
	if h.OpenAIBaseURL == "" {
		h.OpenAIBaseURL = DefaultProviderFlagHelp.OpenAIBaseURL
	}
	if h.OpenRouterBaseURL == "" {
		h.OpenRouterBaseURL = DefaultProviderFlagHelp.OpenRouterBaseURL
	}
	if h.AnthropicBaseURL == "" {
		h.AnthropicBaseURL = DefaultProviderFlagHelp.AnthropicBaseURL
	}
	if h.OpenCodeBaseURL == "" {
		h.OpenCodeBaseURL = DefaultProviderFlagHelp.OpenCodeBaseURL
	}
	return h
}

// ToolhiveLLMFlagHelp carries the per-main help text for the two ToolHive LLM
// gateway flags (issue #262). mecatui prefixes "embedded server only:" (it
// only matters when mecatui hosts its OWN in-process server); the other three
// mains use DefaultToolhiveLLMFlagHelp verbatim.
type ToolhiveLLMFlagHelp struct {
	Enable  string
	BaseURL string
}

// DefaultToolhiveLLMFlagHelp is the shared wording every consumer starts from.
// It explicitly disambiguates from the UNRELATED --toolhive flag (ToolHive MCP
// workload discovery) — the two features share a vendor name and nothing
// else, and issue #262's own review flagged the naming collision as the #1
// confusion risk.
var DefaultToolhiveLLMFlagHelp = ToolhiveLLMFlagHelp{
	Enable: "auto-detect a locally-running ToolHive LLM proxy by reading ToolHive's config and probing " +
		"127.0.0.1, and register it as a model provider (id \"toolhive\", no API key needed); unrelated to " +
		"--toolhive (MCP workload discovery). Set =false on shared hosts",
	BaseURL: "explicit ToolHive LLM proxy base URL (must resolve to loopback); skips the config-file " +
		"auto-detect but keeps the startup probe",
}

// ToolhiveLLMFlags holds the values bound by RegisterToolhiveLLMFlags.
type ToolhiveLLMFlags struct {
	enable  *bool
	baseURL *string
}

// RegisterToolhiveLLMFlags registers --toolhive-llm (default true) and
// --toolhive-llm-base-url (default "") on fs. A zero ToolhiveLLMFlagHelp field
// falls back to DefaultToolhiveLLMFlagHelp, mirroring RegisterProviderFlags.
func RegisterToolhiveLLMFlags(fs *flag.FlagSet, help ToolhiveLLMFlagHelp) *ToolhiveLLMFlags {
	if help.Enable == "" {
		help.Enable = DefaultToolhiveLLMFlagHelp.Enable
	}
	if help.BaseURL == "" {
		help.BaseURL = DefaultToolhiveLLMFlagHelp.BaseURL
	}
	tf := &ToolhiveLLMFlags{enable: new(bool), baseURL: new(string)}
	fs.BoolVar(tf.enable, "toolhive-llm", true, help.Enable)
	fs.StringVar(tf.baseURL, "toolhive-llm-base-url", "", help.BaseURL)
	return tf
}

// Apply writes the two resolved values onto cfg. A nil receiver (a config
// built WITHOUT RegisterToolhiveLLMFlags — e.g. a test that constructs the
// cmd config struct directly) leaves both app.Config fields at their zero
// value (ToolhiveLLM=false, ToolhiveLLMBaseURL=""), mirroring
// ProviderFlags.Apply's nil-receiver discipline — so app.Config's
// byte-identical-when-unset invariant holds for a caller that never wires
// this flag set.
func (tf *ToolhiveLLMFlags) Apply(cfg *app.Config) {
	if tf == nil {
		return
	}
	cfg.ToolhiveLLM = *tf.enable
	cfg.ToolhiveLLMBaseURL = *tf.baseURL
}

// KeyValueList is a repeatable "key=value" flag.Value collecting into a
// last-write-wins map. It backs --model-alias (e.g.
// --model-alias fast=gpt-4o-mini --model-alias smart=gpt-5) and --model-slot
// (e.g. --model-slot compaction=cheap) in both cmd/mecated and cmd/mecatui.
// Extracted here (issue #93) so the two mains cannot drift apart — the twin
// copies already had divergent Set error messages in #87.
//
// A nil *KeyValueList is usable: Set lazily allocates the backing map, exactly
// as the pre-extraction inline types did, so a config struct field's zero value
// (a nil map) is a valid flag binding.
type KeyValueList map[string]string

// String implements flag.Value. It renders the map as a sorted, comma-separated
// list of key=value pairs (empty for a nil/empty map), the stable form flag's
// default-value display expects.
func (m KeyValueList) String() string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Set implements flag.Value. A later occurrence of the same key overrides an
// earlier one. A value without '=' (or with an empty key) is a parse error.
func (m *KeyValueList) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return fmt.Errorf("must be key=value, got %q", v)
	}
	if *m == nil {
		*m = KeyValueList{}
	}
	(*m)[k] = strings.TrimSpace(val)
	return nil
}

// AsMap returns the backing map as a plain map[string]string (the type
// app.Config.ModelAliases / ModelSlots expects), or nil for a nil receiver so
// an unset flag yields the byte-identical default (a nil map, not an empty
// one). It is the ONE conversion a cmd main does to thread a parsed KeyValueList
// onto app.Config.
func (m *KeyValueList) AsMap() map[string]string {
	if m == nil {
		return nil
	}
	return map[string]string(*m)
}

// ModelFlagHelp carries the per-main help text for the two repeatable model
// flags --model-alias and --model-slot. The two mains word these slightly
// differently (mecatui prefixes "embedded server only:"), so the help is passed
// in rather than hard-coded — keeping each main's --help BYTE-IDENTICAL across
// the extraction. A zero ModelFlagHelp falls back to DefaultModelFlagHelp (the
// mecated wording), which is what a new consumer (mecatequi) would use.
type ModelFlagHelp struct {
	ModelAlias string
	ModelSlot  string
}

// DefaultModelFlagHelp is the mecated-style wording, used when a field of the
// passed ModelFlagHelp is empty. It mirrors the help text the mecated twin
// carried before the extraction.
var DefaultModelFlagHelp = ModelFlagHelp{
	ModelAlias: "model alias mapping as name=model-id (repeatable), e.g. --model-alias fast=gpt-4o-mini. Aliases are resolved only in the composition layer; an agent def's `model: <alias>` resolves through this map (then the built-in sonnet/opus/haiku aliases)",
	ModelSlot:  "per-slot model binding as slot=selector (repeatable), e.g. --model-slot compaction=cheap --model-slot cheap=gpt-4o-mini (ADR 0030). A SLOT routes an internal lightweight LLM call to its own model: the wired slots are `compaction` (the compaction summary call), `ask-reviewer` (the headless child-ask reviewer), and `guardrail` (the content checker); a TIER key (`cheap`/`fast`/`reasoning`) gives a default a slot falls through to (each routed slot defaults to `cheap`). The selector is an alias (resolved through --model-alias / the built-ins) or a concrete id. Empty (no --model-slot) keeps every call on the session model (byte-identical default). FAIL-SOFT: a typo'd slot or an alias meaning inherit WARNs and keeps the session model. For ask-reviewer/guardrail the slot supersedes the model of --subagent-ask-reviewer/--guardrails-model but does NOT enable them (those flags stay the on/off gate). Operator-tier only; the YAML twin is the user-global settings.yaml `models.slots:` subtree",
}

// RegisterModelFlags registers --model-alias / --model-slot on fs, each bound
// to its own *KeyValueList, and returns the pair so the caller can thread them
// onto app.Config (ModelAliases / ModelSlots). The help text comes from help,
// falling back per-field to DefaultModelFlagHelp so a caller may pass a zero
// value (or override only the fields it words differently). It is the ONE place
// the two repeatable model flags are wired, so the two mains (and a future
// mecatequi consumer) cannot drift apart.
func RegisterModelFlags(fs *flag.FlagSet, help ModelFlagHelp) (aliases, slots *KeyValueList) {
	help = help.withModelDefaults()
	aliases = new(KeyValueList)
	slots = new(KeyValueList)
	fs.Var(aliases, "model-alias", help.ModelAlias)
	fs.Var(slots, "model-slot", help.ModelSlot)
	return aliases, slots
}

func (h ModelFlagHelp) withModelDefaults() ModelFlagHelp {
	if h.ModelAlias == "" {
		h.ModelAlias = DefaultModelFlagHelp.ModelAlias
	}
	if h.ModelSlot == "" {
		h.ModelSlot = DefaultModelFlagHelp.ModelSlot
	}
	return h
}
