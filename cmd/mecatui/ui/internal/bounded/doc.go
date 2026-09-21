// Package bounded provides pointer-owned, ANSI-aware bounded terminal controls.
//
// Viewport offsets and List logical selection are independent. ViewportView is a
// contentless projection over lines supplied by its caller for each render; ListView
// contains ListRow metadata. Layout is measured
// in terminal display cells and invalid geometry produces an empty view. Item IDs
// must be unique and stable across refreshes: refreshes preserve the selected ID
// and top-visible semantic anchor; a missing selection adopts its clamped
// replacement and does not snap back. Callers own selected styling and hit regions.
// Controls are stateful and pointer-owned; they have no Bubble Tea, theme, or
// Mecatl domain dependency.
package bounded
