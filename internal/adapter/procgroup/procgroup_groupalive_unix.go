//go:build unix && !darwin

package procgroup

import "syscall"

// GroupAlive reports whether the managed process group remains alive. It does
// not inspect escaped descendants, which are outside the managed contract.
func GroupAlive(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || err == syscall.EPERM
}
