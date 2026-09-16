package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/term"
)

type providerTerminalError string

func (e providerTerminalError) Error() string { return string(e) }

func readHiddenProviderAPIKey(ctx context.Context, provider string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(os.Stderr, "API key for %s: ", providerDisplay(provider)); err != nil {
		return "", providerTerminalError("could not write API key prompt")
	}
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	defer clear(key)
	if _, writeErr := fmt.Fprintln(os.Stderr); writeErr != nil && err == nil {
		return "", providerTerminalError("could not write API key prompt")
	}
	if err != nil {
		return "", providerTerminalError("could not read API key from the local terminal")
	}
	return string(key), nil
}
