package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// fakeTool is a minimal Tool used to exercise the Catalog.
type fakeTool struct {
	name     string
	readOnly bool
}

func (f fakeTool) Spec() ToolSpec { return ToolSpec{Name: f.name} }
func (f fakeTool) ReadOnly() bool { return f.readOnly }
func (fakeTool) Execute(context.Context, session.ToolCall, Workspace) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Spec().Name
	}
	return out
}

func TestCatalogLookupAndRegister(t *testing.T) {
	c := NewCatalog()
	read := fakeTool{name: "Read", readOnly: true}
	if err := c.Register(read); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := c.Lookup("Read")
	if !ok || got.Spec().Name != "Read" {
		t.Fatalf("Lookup(Read) = %+v, %v", got, ok)
	}
	if _, ok := c.Lookup("Nope"); ok {
		t.Fatalf("Lookup of missing tool returned ok")
	}
}

func TestCatalogDuplicateRegistration(t *testing.T) {
	c := NewCatalog()
	if err := c.Register(fakeTool{name: "Read", readOnly: true}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := c.Register(fakeTool{name: "Read", readOnly: true})
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate Register err = %v, want ErrDuplicateTool", err)
	}
}

func TestCatalogMustRegisterPanicsOnDuplicate(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	defer func() {
		if recover() == nil {
			t.Fatalf("MustRegister did not panic on duplicate")
		}
	}()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
}

func TestCatalogPlanModeFiltersNonReadOnly(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(fakeTool{name: "Grep", readOnly: true})
	c.MustRegister(fakeTool{name: "Edit", readOnly: false})
	c.MustRegister(fakeTool{name: "Write", readOnly: false})

	plan := names(c.Available(session.ModePlan))
	wantPlan := []string{"Grep", "Read"} // sorted, read-only only
	if !equal(plan, wantPlan) {
		t.Fatalf("plan-mode tools = %v, want %v", plan, wantPlan)
	}

	def := names(c.Available(session.ModeDefault))
	wantDef := []string{"Edit", "Grep", "Read", "Write"} // sorted, all
	if !equal(def, wantDef) {
		t.Fatalf("default-mode tools = %v, want %v", def, wantDef)
	}
}

func TestCatalogSpecsRespectMode(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(fakeTool{name: "Edit", readOnly: false})

	specs := c.Specs(session.ModePlan)
	if len(specs) != 1 || specs[0].Name != "Read" {
		t.Fatalf("plan-mode specs = %v, want [Read]", specs)
	}
}

// planOnlyTool is a minimal Tool implementing PlanOnly (issue #206): it asserts the
// catalog's mode projection excludes a PlanOnly tool from every non-plan mode while
// keeping it visible in plan mode (it is read-only so it passes the plan-mode
// read-only filter too).
type planOnlyTool struct {
	name string
}

func (p planOnlyTool) Spec() ToolSpec { return ToolSpec{Name: p.name} }
func (planOnlyTool) ReadOnly() bool   { return true }
func (planOnlyTool) Execute(context.Context, session.ToolCall, Workspace) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
func (planOnlyTool) PlanOnlyTool() {}

var _ PlanOnly = planOnlyTool{}

// TestCatalogPlanOnlyExcludedFromNonPlanModes pins the projection gate for PlanOnly
// tools (issue #206, Wave 3): a PlanOnly tool is registered everywhere (so the
// shared + per-session catalog name-sets stay equal) but the mode projection
// EXCLUDES it from every non-plan mode (Available / Specs / AdvertisedSpecs all hide
// it under ModeDefault and ModeAccept), while it REMAINS visible under ModePlan.
// The dispatcher's name+mode check is defense-in-depth on top of this gate.
func TestCatalogPlanOnlyExcludedFromNonPlanModes(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(fakeTool{name: "Edit", readOnly: false})
	c.MustRegister(planOnlyTool{name: "PresentPlan"})

	// Non-plan modes: PlanOnly tool is EXCLUDED from Available and Specs.
	for _, mode := range []session.PermissionMode{session.ModeDefault, session.ModeAccept} {
		for _, tl := range c.Available(mode) {
			if tl.Spec().Name == "PresentPlan" {
				t.Fatalf("Available(%q) contains the PlanOnly tool — it must be excluded from non-plan modes", mode)
			}
		}
		for _, spec := range c.Specs(mode) {
			if spec.Name == "PresentPlan" {
				t.Fatalf("Specs(%q) contains the PlanOnly tool — it must be excluded from non-plan modes", mode)
			}
		}
		for _, spec := range c.AdvertisedSpecs(mode) {
			if spec.Name == "PresentPlan" {
				t.Fatalf("AdvertisedSpecs(%q) contains the PlanOnly tool — it must be excluded from non-plan modes", mode)
			}
		}
	}

	// Plan mode: the PlanOnly tool IS visible (it is read-only, so it passes the
	// plan-mode read-only filter; the PlanOnly marker does NOT hide it in plan mode).
	planAvail := names(c.Available(session.ModePlan))
	wantPlan := []string{"PresentPlan", "Read"} // sorted, read-only only
	if !equal(planAvail, wantPlan) {
		t.Fatalf("plan-mode Available = %v, want %v (PlanOnly tool stays visible in plan mode)", planAvail, wantPlan)
	}
	planSpecs := c.Specs(session.ModePlan)
	if len(planSpecs) != len(wantPlan) {
		t.Fatalf("plan-mode Specs len = %d, want %d", len(planSpecs), len(wantPlan))
	}

	// Non-plan Available still carries the ordinary (non-PlanOnly) tools unchanged.
	defAvail := names(c.Available(session.ModeDefault))
	wantDef := []string{"Edit", "Read"} // sorted; PresentPlan excluded
	if !equal(defAvail, wantDef) {
		t.Fatalf("default-mode Available = %v, want %v (only the PlanOnly tool excluded)", defAvail, wantDef)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
