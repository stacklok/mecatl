package sessiongroup_test

import (
	"context"

	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

// This compile-checked sketch shows intended navigation, not an executable
// hosting implementation. Open currently returns ErrNotImplemented. The
// application supplies an existing group ID; parsing does not create a group.
func ExampleSessionGroup() {
	// A real host must supply validated lease/lifecycle and member inputs.
	// This zero config is only a placeholder in the non-executable sketch.
	var config sessiongroup.Config
	ctx := context.Background()
	id, err := groupid.ParseGroupID("GRP1111111111111111111111")
	if err != nil {
		panic(err)
	}
	group, err := sessiongroup.Open(ctx, id, config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := group.Drain(); err != nil {
			panic(err)
		}
		select {
		case <-group.Done():
		case <-ctx.Done():
			// Ending this wait does not stop group-owned shutdown.
			panic(ctx.Err())
		}
		report, err := group.Report()
		if err != nil {
			panic(err)
		}
		if report.Release != sessiongroup.ShutdownReleaseReleased || len(report.Unresolved) != 0 {
			// Completion can still report uncertain release or live work.
			panic("session group shutdown left unresolved responsibility")
		}
	}()

	members, err := group.Members(ctx) // Includes members with no loaded runtime.
	if err != nil {
		panic(err)
	}
	if len(members) < 2 {
		return
	}
	first, err := group.Load(ctx, members[0])
	if err != nil {
		panic(err)
	}
	second, err := group.Load(ctx, members[1])
	if err != nil {
		panic(err)
	}
	// These are peer runtime handles under one residency boundary. Neither load
	// starts execution. Each runtime controls access to its own passive Session.
	if _, err := first.Snapshot(ctx); err != nil {
		panic(err)
	}
	if _, err := second.Snapshot(ctx); err != nil {
		panic(err)
	}
}
