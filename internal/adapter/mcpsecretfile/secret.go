// Package mcpsecretfile reads bounded operator-owned OAuth client secrets.
package mcpsecretfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// MaxBytes bounds the OAuth client secret file to 64 KiB.
const MaxBytes = 64 << 10

// Read rejects non-regular files before reading, including FIFOs opened without blocking.
func Read(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open client secret file: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat client secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("client secret file is not a regular file")
	}
	secret, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read client secret file: %w", err)
	}
	defer clear(secret)
	if len(secret) > MaxBytes {
		return "", errors.New("client secret file exceeds 64 KiB")
	}
	value := strings.TrimSpace(string(secret))
	if value == "" {
		return "", errors.New("client secret file is empty")
	}
	return value, nil
}
