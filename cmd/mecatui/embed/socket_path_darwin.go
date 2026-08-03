//go:build darwin

package embed

// Darwin's sockaddr_un.sun_path is 104 bytes including the terminating NUL.
const unixSocketPathLimit = 104

const darwinShortSocketBase = "/tmp"

func unixSocketPathFits(path string) bool { return len(path) < unixSocketPathLimit }
