//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package clientauth

import (
	"context"
	"errors"
	"syscall"

	"github.com/godbus/dbus/v5"
)

func secretServiceHelper(ctx context.Context) int {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		// godbus v5.2.2 exposes no sentinel for its no-address result.
		if err.Error() == "dbus: couldn't determine address of session bus" || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return 81
		}
		return 82
	}
	defer func() { _ = conn.Close() }()
	if conn.Auth(nil) != nil {
		return 82
	}
	if conn.Hello() != nil {
		return 82
	}
	var present bool
	if conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", dbus.FlagNoAutoStart, "org.freedesktop.secrets").Store(&present) != nil {
		return 82
	}
	if present {
		return 80
	}
	return 81
}
