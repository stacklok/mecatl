//go:build darwin

package main

// sunPathMax is the size of sockaddr_un.sun_path on Darwin: 104 bytes INCLUDING
// the terminating NUL, so a usable pathname is at most 103 bytes.
//
// This is the tightest limit of any platform mecated targets, and it is easy to
// exceed by accident: a macOS TMPDIR is already ~50 bytes
// (/var/folders/xy/…/T/), so two nested directories and a filename can run out.
// Validating it at startup is the difference between a sentence naming the path
// and an EINVAL from bind(2).
const sunPathMax = 104
