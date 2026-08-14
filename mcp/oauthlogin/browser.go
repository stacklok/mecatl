package oauthlogin

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
)

// ErrBrowserLaunch indicates that the fixed host browser launcher could not start.
var ErrBrowserLaunch = errors.New("could not open browser")

type browserLaunchError struct{}

func (browserLaunchError) Error() string {
	return "could not open browser; retry in no-browser mode"
}

func (browserLaunchError) Is(target error) bool {
	return target == ErrBrowserLaunch
}

func newBrowserLaunchError() error { return browserLaunchError{} }

type commandRunner func(context.Context, string, ...string) error

type systemBrowserLauncher struct{}

func (systemBrowserLauncher) Open(ctx context.Context, authorizationURL string) error {
	return launchBrowser(ctx, runtime.GOOS, authorizationURL, runCommand)
}

func launchBrowser(ctx context.Context, goos, authorizationURL string, run commandRunner) error {
	var name string
	var args []string
	switch goos {
	case "darwin":
		name, args = "open", []string{authorizationURL}
	case "windows":
		name, args = "rundll32.exe", []string{"url.dll,FileProtocolHandler", authorizationURL}
	default:
		name, args = "xdg-open", []string{authorizationURL}
	}
	if err := run(ctx, name, args...); err != nil {
		return newBrowserLaunchError()
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
