package ui

// surface.go is the ONE file the issue #555 Phase-2 surface interface owns: the
// `surface` interface an overlay implements (Render/HandleKey/HandleMsg/
// HandleWheel/Close) plus the shared `surfaceDeps` collaborator struct and
// the (m *Model).surfaceDeps() builder. Migrating an overlay (soul is the proof)
// touches this file for the interface and its own file for the state/behaviour;
// the Model-side routing (view/update/builtins/selection) is the thin registration
// point. The structural gate (surface_arch_test.go) confines surface/soul
// vocabulary to surface.go + the surface's own file. The deps are held ON THE
// SURFACE STATE (set once at Open): a surface non-Render method with a deps
// param is archived-past design, not current (see docs/design/surface-migration-plan.md).

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// surface is an overlay that owns the conversation region while open: it
// renders a body, consumes keys/wheel/msgs, and holds its teardown. The Model
// holds at most ONE (the modal surface field); nil-vs-set IS the active
// predicate. Implemented by state structs on POINTER receivers strictly so the
// Model can call them on the single instance the interface value holds and
// mutate it in place — no copy-back ceremony.
// GEOMETRY (immediate-mode, the ImGui discipline): geometry flows through
// Render on EVERY frame — there is NO resize event. A surface sizes itself
// from the offered width/height args inline on every call and MUST NOT center
// its own output. Geometry-dependent view state is re-derived at the TOP of
// Render from the fresh args (marked `// view cache:` on the struct), so it
// can never be stale and needs no event. Parents place, surfaces size. Region
// coordinates are frame-relative; the parent offsets them.
type surface interface {
	// Render is the size QUERY: returns the center-ready body and the regions
	// it built in the SAME layout pass, given the offered geometry. The surface
	// sizes itself from the width/height inline on EVERY call and MUST NOT
	// center its own output. Regions are frame-relative; the parent offsets
	// them to screen coordinates if it ever hit-tests them. nil regions = not
	// clickable.
	Render(width, height int) (body string, regions []ClickableRegion)

	// HandleKey consumes or passes a key press. handled=true means the surface
	// swallowed it (idle input never sees it). Close is driven INSIDE HandleKey
	// (esc self-closes): the returned closed flag tells the Model to tear the
	// surface down.
	HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool)

	// HandleMsg consumes or passes a NON-input message: an RPC result
	// (client.SoulMsg), a timer tick, a status notice. The Model routes every
	// non-key/wheel Msg through the open modal BEFORE its own generic reducer,
	// so the surface owns its RPC-backed state and can be created dynamically
	// at Open with no pre-declared Model field. The surface consumes (handled),
	// kills (closed), or passes (handled=false); on closed the Model tears the
	// surface down, exactly as the HandleKey closed path.
	HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool, closed bool)

	// HandleWheel returns handled=true to CONSUME the event (the default: a
	// modal captures input and the wheel behind it is DEAD while the modal is
	// open), handled=false ONLY if the surface deliberately delegates to the
	// conversation viewport. The boolean preserves delegation; the default
	// moved (the surface CONSUMES unless it says otherwise).
	HandleWheel(msg tea.MouseWheelMsg) (cmd tea.Cmd, handled bool)

	// Close tears the surface down; teardown is surface-authored. RPC results
	// that arrive after close are dropped at the reducer. A surface with
	// nothing to release implements an empty body.
	Close()
}

// surfaceDeps is the SHARED ambient base every surface may reach, built once at
// Open by (m *Model).surfaceDeps() and held on the surface state as its deps
// field. Fields are ambient collaborators only; surface-SPECIFIC deps (RPC
// contexts, lifecycle clients, epoch mints) are fields on the surface's own
// state struct, set next to deps in the same Open literal. Mirrors approvalDeps
// (approval_surface.go).
type surfaceDeps struct {
	theme theme.Theme
	keys  keyMap              // for key.Matches
	marks helpKeys            // render hints (helpKeyMarkings)
	caps  client.Capabilities // capability-gated copy
}

// surfaceDeps builds the surface's shared ambient base from the live Model. It
// is a POINTER receiver so a future deps field that bluffs past the Elm
// value-Model copies (the approvalDeps / &m.sendApproval idiom) works the same
// way; callers take &m at the Open transition.
func (m *Model) surfaceDeps() surfaceDeps {
	return surfaceDeps{
		theme: m.deps.Theme,
		keys:  m.keys,
		marks: m.helpKeyMarkings(),
		caps:  m.caps,
	}
}
