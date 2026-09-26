// Package scrollback owns Mecatui's logical conversation document.
//
// It records the ordered cards shown in the main conversation, their stable
// document-local identities, and the revisions that invalidate rendered-card
// cache entries. It deliberately has no dependency on client events, terminal
// geometry, styling, rendering, frame provenance, viewport state, or Bubble Tea.
// The ui package translates client events into the typed operations here, then
// adapts immutable snapshots into renderer-owned presentation inputs.
//
// Start with Conversation. Use its component facades to create and update cards:
//
//   - Messages appends user, assistant, notice, error, hook, delivery, and
//     turn-stat cards and applies assistant streaming updates.
//   - Tools creates and resolves ordinary tool cards.
//   - Subagents and Teams specialize a matching pending tool card and apply the
//     corresponding delegation lifecycle updates.
//   - RecordFileChange and AppendixSnapshot manage the document-local
//     changed-files appendix.
//
// Conversation owns card state. Facade inputs are detached before storage, and
// SnapshotAt and SnapshotForCall return detached snapshots, so a caller cannot
// mutate model-owned state. MetadataAt provides only ID, revision, and kind for
// renderer cache checks; renderers should request a full snapshot only after a
// cache miss.
//
// PayloadSnapshot is a closed family. Consumers may inspect its Kind and use a
// type switch over the package-defined snapshot types, but cannot define another
// card family or mutate a stored payload. A render-visible state change advances
// the enclosing card revision exactly once. Identical lifecycle replays retain
// their revision; conflicting or invalid lifecycle transitions report failure
// without changing the document.
package scrollback
