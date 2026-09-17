//go:build darwin

package main

import "golang.org/x/sys/unix"

// mcpSettingsDevice normalizes unix.Stat_t.Dev, which is int32 on Darwin.
func mcpSettingsDevice(st unix.Stat_t) uint64 { return uint64(uint32(st.Dev)) } //nolint:gosec // Stat_t device values are non-negative OS identities.
