//go:build !darwin

package main

import "golang.org/x/sys/unix"

// mcpSettingsDevice normalizes unix.Stat_t.Dev, which is uint64 on non-Darwin unix platforms.
func mcpSettingsDevice(st unix.Stat_t) uint64 { return st.Dev }
