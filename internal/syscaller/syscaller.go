// Package syscaller stamps the EXPLICIT system principal (ADR 0100 decision 7)
// on the root context of every internal goroutine that has no caller.
//
// Internal work — the session GC, the two dream consolidators, the scheduler's
// tick/fire/delivery/reconcile loops, the startup model-catalog refresh, and the
// token validator's background JWKS refresh — crosses port boundaries with nobody to attribute it to. It runs as
// an explicit `system` principal rather than an ABSENT one, so the day an
// enforcement check lands (the isolation track, #368) these callers neither
// break silently nor get mistaken for an anonymous user.
//
// This is a composition/server concern: the agent loop stays identity-agnostic
// and must never import it (AGENTS.md — the layering rule).
//
// Root is the REGISTRY: every internal context root has a const here and an
// entry in Roots, and the pinning test
// (TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem in internal/app)
// drives each registered root through its real production starter and asserts
// the principal the port actually observes. A new internal goroutine registers
// its root here; forgetting the wrap at the call site turns that root's
// subtest red.
package syscaller

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// Root names one internal goroutine root. It is the Subject of that root's
// system principal, so an observed principal says WHICH internal caller acted.
type Root string

const (
	// RootChildGC is the session retention sweeper (internal/app.startChildGC).
	RootChildGC Root = "childgc"
	// RootMemoryConsolidation is the project-memory dream consolidator.
	RootMemoryConsolidation Root = "memory-consolidation"
	// RootUserModelConsolidation is the user-model dream consolidator.
	RootUserModelConsolidation Root = "user-model-consolidation"
	// RootScheduler is the scheduler's lifecycle root: every tick, fire,
	// delivery and reconcile context descends from Scheduler.Start's ctx.
	RootScheduler Root = "scheduler"
	// RootModelCatalogRefresh is the one-shot startup live-model refresh. It does
	// not cover request-driven stale-model refreshes, which retain their caller.
	RootModelCatalogRefresh Root = "model-catalog-refresh"
	// RootJWKSRefresh is the token validator's background JWKS refresh, which
	// owns the server-root context handed to the validator constructor.
	RootJWKSRefresh Root = "jwks-refresh"
)

// Issuer is the synthetic issuer half of a system principal's (iss, sub)
// identity pair. It is deliberately not a URL: no IdP mints these.
const Issuer = "mecatl:internal"

// Roots is the registry of internal context roots — the single enumeration the
// production wiring and the pinning test share.
var Roots = []Root{
	RootChildGC,
	RootMemoryConsolidation,
	RootUserModelConsolidation,
	RootScheduler,
	RootModelCatalogRefresh,
	RootJWKSRefresh,
}

// Context returns ctx carrying the explicit system principal for root. Call it
// where the goroutine's root context is CREATED, not per port call.
func Context(ctx context.Context, root Root) context.Context {
	return session.WithPrincipal(ctx, &session.Principal{
		Issuer:    Issuer,
		Subject:   string(root),
		GrantType: session.GrantTypeSystem,
	})
}
