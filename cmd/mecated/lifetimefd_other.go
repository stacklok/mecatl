//go:build !unix

package main

// checkLifetimePipeFD does nothing on platforms without fstat.
//
// Validating through os.NewFile is not an option: its wrapper closes the
// descriptor when collected, so a rejection would leave the runtime free to
// close the caller's fd — possibly after that number has been reused. An
// unvalidated descriptor is a worse startup message; a stray close is a
// much worse and far less diagnosable bug.
//
// --lifetime-pipe-fd is a POSIX inherited-descriptor contract in the first
// place, and local spawn on Windows is an explicit non-goal of issue #821 — the
// same boundary socketumask_other.go draws.
func checkLifetimePipeFD(int) error { return nil }
