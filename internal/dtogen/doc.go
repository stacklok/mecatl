// Package dtogen generates independently selected CBOR, Clone, Equal, and view
// methods from statically loaded Go declarations for the isolated kernel.
// A shared typed graph covers complete in-memory state; CBOR mapping and bounds
// apply only to persisted edges. The package README defines declaration syntax,
// supported shapes, and the generation workflow.
//
// Exactly kernel/value.Value and kernel/value.Map are handwritten leaf owners,
// verified by package/module provenance and directly declared operation signatures.
// Their private recursive state is never expanded and arbitrary compatible methods
// remain unsupported. Generated Clone/Equal parents propagate their fixed-depth
// programmer-error panics, including through imported or excluded state.
//
// Automatic map views accept string-underlying keys with scalar entries or
// pointers to selected context-free DTO views. Adapters receive existing values,
// never addresses of copied map entries. Context-bearing or by-value DTO maps
// require handwritten navigation. Exact structured primitives borrow value.View
// or value.MapView instead of exposing mutable authoring storage.
//
// Loading uses GOWORK=off and synthetic signatures, never owner execution. Generation
// renders and typechecks the selected owner closure before installing outputs;
// each regular marked file is replaced atomically. Filesystem failure may leave
// earlier replacements installed; deterministic regeneration repairs the closure.
// Repository check mode validates inventory and bytes without writing files.
package dtogen
