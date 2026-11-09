// Package dtogen generates independently selected CBOR, Clone, Equal, and view
// methods from statically loaded Go declarations for the isolated kernel.
// A shared typed graph covers complete in-memory state; CBOR mapping and bounds
// apply only to persisted edges. The package README defines declaration syntax,
// supported shapes, and the generation workflow.
//
// Loading uses GOWORK=off and synthetic signatures, never owner execution. Generation
// renders and typechecks the selected owner closure before installing outputs;
// each regular marked file is replaced atomically. Filesystem failure may leave
// earlier replacements installed; deterministic regeneration repairs the closure.
// Repository check mode validates inventory and bytes without writing files.
package dtogen
