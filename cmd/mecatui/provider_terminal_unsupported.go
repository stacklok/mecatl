//go:build !linux && !darwin

package main

import (
	"context"
	"errors"
	"os"
)

func readProviderTerminalLine(ctx context.Context, _ *os.File, _ *os.File, _ string, _ bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("local provider terminal input is supported on Linux and macOS")
}
