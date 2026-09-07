//go:build linux

package managedtemp

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func processStartIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("managedtemp: read process start identity: %w", err)
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", errors.New("managedtemp: malformed process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	// Field 22 is starttime; the suffix begins at field 3.
	if len(fields) <= 19 || fields[19] == "" {
		return "", errors.New("managedtemp: malformed process start identity")
	}
	return fields[19], nil
}
