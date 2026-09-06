//go:build unix && !linux && !darwin

package managedtemp

import "errors"

func processStartIdentity(int) (string, error) {
	return "", errors.New("managedtemp: process start identity is unsupported on this platform")
}
