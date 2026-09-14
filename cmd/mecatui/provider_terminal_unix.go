//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Unlike File.Fd, SyscallConn does not put an originally nonblocking file into
// blocking mode. The caller keeps the file open for the duration of the read.
func providerTerminalFD(file *os.File) (int, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd := -1
	err = conn.Control(func(value uintptr) { fd = int(value) })
	return fd, err
}

// Read synchronously in noncanonical mode: canonical terminals can silently
// truncate a pasted key at 4096 bytes. Polling also leaves no blocked worker on
// cancellation. The duplicate shares flags with stdin, so restore those too.
//
//nolint:gocyclo // Keep terminal cleanup, bounded polling, and the small byte-editing state machine together.
func readProviderTerminalLine(ctx context.Context, input, output *os.File, prompt string, hidden bool) (value string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	inputFD, inputErr := providerTerminalFD(input)
	outputFD, outputErr := providerTerminalFD(output)
	if inputErr != nil || outputErr != nil || !term.IsTerminal(inputFD) || !term.IsTerminal(outputFD) {
		return "", errors.New("provider input and prompt output require local terminals")
	}
	fd, err := unix.FcntlInt(uintptr(inputFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return "", errors.New("could not open terminal reader")
	}
	defer func() {
		if closeErr := unix.Close(fd); closeErr != nil {
			value, err = "", errors.New("could not close terminal reader")
		}
	}()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return "", errors.New("could not read terminal flags")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", errors.New("could not hide terminal input")
	}
	defer func() {
		_, flagErr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
		restoreErr := term.Restore(fd, state)
		if flagErr != nil || restoreErr != nil {
			value, err = "", errors.New("terminal restoration failed; restore the terminal before continuing")
		}
	}()
	if err := unix.SetNonblock(fd, true); err != nil {
		return "", errors.New("could not configure terminal reader")
	}
	if _, err := fmt.Fprint(output, strings.ReplaceAll(prompt, "\n", "\r\n")+": "); err != nil {
		return "", errors.New("could not write terminal prompt")
	}
	defer func() { _, _ = fmt.Fprint(output, "\r\n") }()
	var line []byte
	tooLong, invalid := false, false
	defer func() { clear(line) }()
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} //nolint:gosec // POSIX file descriptors are signed C ints.
	var one [1]byte
	defer clear(one[:])
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := unix.Poll(poll, 50); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return "", errors.New("terminal input failed")
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return "", errors.New("terminal input disconnected")
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, one[:])
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return "", errors.New("terminal input failed")
		}
		if n == 0 {
			return "", io.EOF
		}
		switch b := one[0]; b {
		case 3: // Raw mode disables ISIG; Ctrl-C has the same cancellation contract.
			return "", context.Canceled
		case 4:
			return "", io.EOF
		case '\r', '\n':
			if tooLong {
				return "", errProviderInputTooLong
			}
			if invalid {
				return "", errors.New("terminal input contains unsupported control characters")
			}
			if !utf8.Valid(line) {
				return "", errors.New("terminal input must be valid text")
			}
			for _, r := range string(line) {
				if !unicode.IsPrint(r) || isTrustControl(r) {
					return "", errors.New("terminal input contains unsupported control characters")
				}
			}
			return string(line), nil
		case 8, 127:
			if len(line) > 0 {
				_, size := utf8.DecodeLastRune(line)
				clear(line[len(line)-size:])
				line = line[:len(line)-size]
				if !hidden {
					if _, err := fmt.Fprint(output, "\b \b"); err != nil {
						return "", errors.New("could not write terminal prompt")
					}
				}
			}
		default:
			if b < 32 {
				invalid = true
				continue
			}
			if len(line) == 8*1024 || tooLong {
				tooLong = true
				continue
			}
			line = append(line, b)
			if !hidden && b < 127 {
				if _, err := output.Write(one[:]); err != nil {
					return "", errors.New("could not write terminal prompt")
				}
			}
		}
	}
}
