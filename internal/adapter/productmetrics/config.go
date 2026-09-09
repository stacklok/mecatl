// Package productmetrics is a fully independent, opt-out-by-default OTel
// metrics adapter reporting bounded adoption/usage counters to Stacklok's
// public metrics collector. It shares no import, struct, MeterProvider, or
// destination with internal/adapter/telemetry (mecatl's operator-facing
// observability pipeline) — the two are combined only at the composition
// edge (internal/cliconfig), by fanning both into the engine's
// port.EventSink/port.ToolCallRecorder seams.
//
// Every exported type in this package that can become a metric attribute is
// a closed Go string-alias enum. Nothing here carries a session id, model
// id/alias, tool or MCP-server name, file path, or free text.
package productmetrics

// Feature is the closed set of major toggleable features reported at
// heartbeat time. Never a def/model/tool name — only these four values.
type Feature string

const (
	FeatureMemory     Feature = "memory"
	FeatureGuardrails Feature = "guardrails"
	FeatureMCP        Feature = "mcp"
	FeatureScheduling Feature = "scheduling"
)

// ProviderFamily is the closed set of configured LLM provider families.
// Never a model id or alias.
type ProviderFamily string

const (
	ProviderAnthropic  ProviderFamily = "anthropic"
	ProviderOpenAI     ProviderFamily = "openai"
	ProviderOpenRouter ProviderFamily = "openrouter"
	ProviderOther      ProviderFamily = "other"
)

// DeploymentMode is the closed set of process shapes.
type DeploymentMode string

const (
	ModeInteractive DeploymentMode = "interactive"
	ModeHeadless    DeploymentMode = "headless"
	ModeK8s         DeploymentMode = "k8s"
)

// Binary is the closed set of the four mecatl entry points.
type Binary string

const (
	BinaryMecated   Binary = "mecated"
	BinaryMecatui   Binary = "mecatui"
	BinaryMecatequi Binary = "mecatequi"
	BinaryMecak8s   Binary = "mecak8s"
)

// FeatureSnapshot is a closed-shape, read-only snapshot of which major
// features are enabled and which provider family / deployment mode this
// process runs as. It carries no free text and no model id/alias.
type FeatureSnapshot struct {
	Memory     bool
	Guardrails bool
	MCP        bool
	Scheduling bool
	Provider   ProviderFamily
	Mode       DeploymentMode
}

// enabled returns every Feature mapped to whether this snapshot reports it
// enabled. It is the single place Heartbeat iterates, so adding a Feature
// const without adding it here is caught by the exhaustiveness this map
// documents (and by TestFeatureSnapshotEnabledIsClosedAndBounded above).
func (s FeatureSnapshot) enabled() map[Feature]bool {
	return map[Feature]bool{
		FeatureMemory:     s.Memory,
		FeatureGuardrails: s.Guardrails,
		FeatureMCP:        s.MCP,
		FeatureScheduling: s.Scheduling,
	}
}

// Config configures a Provider/Recorder pair for one process.
type Config struct {
	// Binary identifies which of the four entry points this process is.
	Binary Binary
	// Version is the mecatl build version (resource attribute service.version).
	Version string
	// InstallID is this process's persisted anonymous install identifier.
	InstallID string
}
