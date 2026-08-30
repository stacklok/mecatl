package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

// savedConnectController projects only public registry metadata into the UI.
type savedConnectController struct{}

func (savedConnectController) ListConnectTargets(context.Context) ([]ui.ConnectTarget, error) {
	registry, err := clientauth.OpenExistingRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		return nil, fmt.Errorf("saved targets unavailable: opening the connection registry failed: %w", err)
	}
	connections, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("saved targets unavailable: reading the connection registry failed: %w", err)
	}
	targets := make([]ui.ConnectTarget, 0, len(connections))
	for _, conn := range connections {
		targets = append(targets, ui.ConnectTarget{
			Target: conn.Identity.Target, Issuer: conn.Identity.Issuer,
			ClientID: conn.Identity.ClientID, Audience: conn.Identity.Audience,
		})
	}
	return targets, nil
}

func savedConnection(target string) (clientauth.Connection, error) {
	registry, err := clientauth.OpenExistingRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		return clientauth.Connection{}, fmt.Errorf("saved target unavailable: opening the connection registry failed: %w", err)
	}
	connection, err := registry.FindTarget(target)
	if err != nil {
		// Not enrolled is an ordinary state with an obvious remedy; an unreadable
		// registry is a local fault. main.go's copy of this lookup already
		// separated them, and this one reporting both as one message is why an
		// unenrolled target reached the server as an anonymous dial.
		if clientauth.IsNotEnrolled(err) {
			return clientauth.Connection{}, fmt.Errorf("no saved login for %s; run 'mecatui login %s'", target, target)
		}
		return clientauth.Connection{}, fmt.Errorf("saved target unavailable: reading the connection registry failed: %w", err)
	}
	return connection, nil
}
