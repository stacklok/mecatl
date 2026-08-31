//go:build !darwin

package main

// sunPathMax is the size of sockaddr_un.sun_path on Linux and the platforms that
// follow it: 108 bytes INCLUDING the terminating NUL.
//
// The BSDs use Darwin's 104. Assuming the larger value here only ever makes the
// startup check LOOSER than the kernel's, so an over-long path on such a
// platform still fails at bind(2) rather than being wrongly accepted as fine —
// the check is a better error message, never a substitute for the syscall.
const sunPathMax = 108
