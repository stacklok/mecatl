//go:build linux

package kpi

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// readRSS returns the process resident set size in bytes by parsing the VmRSS
// line of /proc/self/status (a "VmRSS:\t   12345 kB" line; kB → bytes). It is a
// zero-dependency direct read, chosen over pulling in gopsutil. Any read/parse failure returns 0 (the sampler treats 0 as
// "no reading" — it never inflates the peak).
func readRSS() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line) // ["VmRSS:", "12345", "kB"]
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
