package main

import "github.com/stacklok/mecatl/internal/app"

func markRetentionCLIFlag(set *app.RetentionCLISet, name string) {
	switch name {
	case "main-retention":
		set.MainMaxAge = true
	case "main-retention-max-total":
		set.MainMaxCount = true
	case "child-retention":
		set.ChildMaxAge = true
	case "child-retention-max-per-family":
		set.ChildMaxCount = true
	case "schedule-fire-retention":
		set.ScheduledMaxAge = true
	case "schedule-fire-retention-max-total":
		set.ScheduledMaxCount = true
	case "retention-sweep-cadence":
		set.SweepCadence = true
	}
}
