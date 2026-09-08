//go:build darwin

package procgroup

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin's XNU process state value for a zombie process (SZOMB).
const darwinZombieState int8 = 5

var darwinGroupProcesses = func(pid int) ([]int8, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if err != nil {
		return nil, err
	}
	states := make([]int8, len(processes))
	for i, process := range processes {
		states[i] = process.Proc.P_stat
	}
	return states, nil
}

// darwinGroupHasLiveProcess reports false only when kernel inspection finds a
// non-empty process group containing exclusively zombies. A failed or
// contradictory lookup remains alive so cleanup fails closed.
func darwinGroupHasLiveProcess(pid int) bool {
	states, err := darwinGroupProcesses(pid)
	if err != nil || len(states) == 0 {
		return true
	}
	for _, state := range states {
		if state != darwinZombieState {
			return true
		}
	}
	return false
}

// GroupAlive reports whether the managed process group remains alive. It does
// not inspect escaped descendants, which are outside the managed contract.
func GroupAlive(pid int) bool {
	err := syscall.Kill(-pid, 0)
	if err != nil {
		return err == syscall.EPERM
	}
	return darwinGroupHasLiveProcess(pid)
}
