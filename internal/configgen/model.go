package configgen

// The MODEL: a provider-neutral description of the operator settings.yaml surface.
// The generator (internal/configgen/cmd/configref) populates a Model by reflecting
// over the permconfig.*Section structs and harvesting their field doc-comments via
// go/ast; the renderers (RenderSkeleton / RenderReference) walk the SAME Model. The
// model carries no go/ast or reflect types, so this file is safe to compile into the
// shipped mecated binary (it never does — only the generator and the renderers use
// it — but the boundary is kept clean by construction).

// Tier classifies which configuration tier honours a subtree. It is HAND-PINNED per
// subtree (the tier is a security decision — whether a project file may set it — not a
// struct-tag derivable by reflection), and is NOT machine-validated against the schema.
// A regression test (TestSubtreeTiersAreAsPinned) pins the expected Tier per subtree so
// a future mislabel is caught.
type Tier string

const (
	// TierOperator subtrees are honoured ONLY from the user-global settings.yaml + CLI
	// (a project-tier file's copy is ignored with a WARN — honouring it would be a
	// security downgrade).
	TierOperator Tier = "operator"
	// TierProject subtrees may ALSO be set in a project .mecatl/settings.yaml (within
	// the operator's cap / trust gate where one applies).
	TierProject Tier = "operator + project"
)

// Model is the whole settings.yaml surface: the ordered top-level subtrees.
type Model struct {
	Subtrees []*Subtree
}

// Subtree is one top-level YAML key (permissions / guardrails / posture / models).
type Subtree struct {
	// Key is the top-level YAML key (e.g. "models").
	Key string
	// Tier is which tier(s) honour the subtree.
	Tier Tier
	// Doc is the subtree-level description (from the struct or Config field comment).
	Doc string
	// EnableNote, when non-empty, is the headline enable-semantics line surfaced in
	// the skeleton AND the reference (e.g. the router taxonomy-presence note). It is
	// the one thing operators most need to know about an opt-in subtree.
	EnableNote string
	// CommentedOut marks an opt-in subtree the skeleton emits fully commented (the
	// operator uncomments to enable). Always-present subtrees (permissions) are live.
	CommentedOut bool
	// Scalar marks a subtree that is a bare scalar (posture) rather than a mapping; its
	// Fields holds a single pseudo-field describing the scalar.
	Scalar bool
	// ListBody marks a subtree whose body is a YAML SEQUENCE of the Fields (each field
	// is one ELEMENT key), not a mapping. The skeleton renders one `- ` list element
	// showing the fields; the reference renders the keys as `subtree[].field`.
	ListBody bool
	// Fields are the keys within the subtree, in declaration order.
	Fields []*Field
	// Example is an optional commented example value block (raw YAML lines, no leading
	// comment markers) the skeleton renders under the subtree to show real shape.
	Example []string
}

// Field is one key within a subtree (or a scalar subtree's single value).
type Field struct {
	// Key is the YAML key (e.g. "classifier-slot").
	Key string
	// Type is the rendered Go-ish type (e.g. "string", "map[string]string",
	// "[]string", "[]category", "bool", "int").
	Type string
	// Default is the rendered zero/default value.
	Default string
	// Doc is the field's one-or-more-line description, harvested from its doc-comment.
	Doc string
	// EnableNote mirrors Subtree.EnableNote at the field grain (e.g. guardrails.model
	// "setting this ENABLES guardrails").
	EnableNote string
	// Nested, when non-nil, describes a structured sub-mapping (e.g. each router
	// category, or the subagent permissions block) so the skeleton can show its shape.
	Nested []*Field
	// SkeletonCollapse renders this field as its type-shaped placeholder in the
	// skeleton even when Nested is populated for the exhaustive reference. It is
	// used for closed unions whose mutually exclusive variants cannot all appear
	// in one uncomment-and-run structural example; Subtree.Example shows valid arms.
	SkeletonCollapse bool
	// ExampleValue is a short inline example used in the skeleton's commented binding
	// (e.g. `default: sonnet`). Empty renders a type-shaped placeholder.
	ExampleValue string
	// ExampleMapKey is the illustrative map key a map-of-struct skeleton entry renders
	// under (e.g. the openrouter.models per-model entry's model id). Empty renders the
	// generic `"<key>"` placeholder. Documentation only — the operator replaces it.
	ExampleMapKey string
}
