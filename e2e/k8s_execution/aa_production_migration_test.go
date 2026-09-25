//go:build kind_execution_e2e

package k8s_execution_test

import "testing"

func TestKindExecutionProductionCompatiblePrototypeMigration(t *testing.T) {
	runKindExecutionProductionCompatiblePrototypeMigration(t)
}
