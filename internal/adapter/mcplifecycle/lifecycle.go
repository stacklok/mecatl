// Package mcplifecycle contains the reusable, non-UI part of direct MCP
// profile lifecycle operations. It deliberately does not parse argv, inspect a
// terminal, install signals, or render command output.
package mcplifecycle

import (
	"context"
	"errors"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// Progress identifies an operator-visible lifecycle stage. The command owns
// rendering these stages; the package never writes to a stream.
type Progress func(string)

// AddRequest supplies validated command inputs and the already-read settings
// document. Settings publication remains with the caller so its locking and
// CAS policy stay at the file boundary.
type AddRequest struct {
	Name, URL, Issuer string
	Settings          []byte
	CredentialRoot    string
	FileKeyPath       string
	CredentialStore   string
	Attended          bool
	ConfirmFile       mcpcredential.ConfirmFile
	Progress          Progress
}

// AddResult is the prepared settings document and the selected custody backend.
type AddResult struct {
	Settings []byte
	Backend  string
	Locator  string
}

// Add discovers the issuer, selects pinned custody, and prepares (but does not
// publish) the direct MCP profile. Key material is cleared before returning.
// The issuer is supplied when discovery was intentionally performed by the
// caller; an empty issuer performs discovery here.
func Add(ctx context.Context, req AddRequest) (AddResult, error) {
	if err := ctx.Err(); err != nil {
		return AddResult{}, err
	}
	var settings permconfig.Config
	if err := yaml.Unmarshal(req.Settings, &settings); err != nil {
		return AddResult{}, err
	}
	if settings.MCP != nil && settings.MCP.Mode == "broker" {
		return AddResult{}, errors.New("MCP direct onboarding is unavailable when mcp.mode is broker")
	}
	issuer := req.Issuer
	if issuer == "" {
		if req.Progress != nil {
			req.Progress("discovering protected resource")
		}
		discovery, err := mcp.DiscoverDirectIssuer(ctx, req.URL)
		if err != nil {
			return AddResult{}, err
		}
		issuer = discovery.Issuer
	}
	if err := ctx.Err(); err != nil {
		return AddResult{}, err
	}
	if req.Progress != nil {
		req.Progress("selecting credential custody")
	}
	selected, err := mcpcredential.Resolve(ctx, req.CredentialRoot, mcpcredential.Options{
		Requested:   req.CredentialStore,
		FilePath:    req.FileKeyPath,
		Attended:    req.Attended,
		ConfirmFile: req.ConfirmFile,
	})
	if err != nil {
		return AddResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AddResult{}, err
	}
	defer clear(selected.Key)
	locator := ""
	if selected.Backend == mcpcredential.BackendFile {
		locator = selected.Locator
	}
	after, err := permconfig.AddDirectMCPServerWithKey(req.Settings, req.Name, req.URL, issuer, req.CredentialRoot, selected.Backend, locator)
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{Settings: after, Backend: selected.Backend, Locator: locator}, nil
}

// ListEntry is the bounded, non-secret profile projection used by list.
type ListEntry struct {
	Name, URL, Kind, CredentialStatus string
}

// List returns the configured profiles from one winning settings document.
// Credential status is derived solely through mcpcredential's canonical marker
// inspector and never opens a keyring or reads a credential.
func List(cfg permconfig.Config) []ListEntry {
	if cfg.MCP == nil {
		return nil
	}
	entries := make([]ListEntry, 0, len(cfg.MCP.Servers))
	for _, server := range cfg.MCP.Servers {
		entries = append(entries, ListEntry{
			Name:             server.Name,
			URL:              server.URL,
			Kind:             profileKind(server),
			CredentialStatus: credentialStatus(server),
		})
	}
	return entries
}

func profileKind(server permconfig.MCPServerProfile) string {
	if server.Auth.OAuth == nil {
		return server.Auth.Mode
	}
	if client := server.Auth.OAuth.Client; client.Mode != "" {
		return "oauth/" + client.Mode
	}
	return "oauth"
}

const credentialStatusUnknown = "unknown"

func credentialStatus(server permconfig.MCPServerProfile) string {
	if server.Auth.OAuth == nil {
		if server.Auth.Mode == "none" {
			return "ready"
		}
		return credentialStatusUnknown
	}
	credentials := server.Auth.OAuth.Credentials
	if credentials.Mode != "local" || credentials.Local == nil || credentials.Local.Key == nil {
		return credentialStatusUnknown
	}
	switch mcpcredential.InspectMarker(credentials.Local.Root) {
	case mcpcredential.MarkerMissing:
		return "login required"
	case mcpcredential.MarkerUnavailable:
		return "unavailable"
	case mcpcredential.MarkerLocked:
		return "locked"
	case mcpcredential.MarkerRecovery:
		return "recovery required"
	case mcpcredential.MarkerPresent:
		return credentialStatusUnknown
	default:
		return credentialStatusUnknown
	}
}

// Remove prepares settings with the named direct profile removed. Upstream DCR
// removal remains a command-owned concern because it owns profile loading and
// user-facing remedy mapping.
func Remove(settings []byte, name string) ([]byte, error) {
	return permconfig.RemoveDirectMCPServer(settings, name)
}
