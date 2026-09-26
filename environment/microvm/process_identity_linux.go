//go:build linux

package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func platformProcessStartIdentity(ctx context.Context, pid int) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return "", errors.New("malformed process stat identity")
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return "", errors.New("process stat identity omits start time")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", errors.New("invalid process start time")
	}
	return fields[19], nil
}
