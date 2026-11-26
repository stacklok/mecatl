//go:build !linux

package executionexecutor

func touchWorkspace(root string) error { return touchNamed(root) }
