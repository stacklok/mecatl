package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const mcpLoginUsage = "usage: mecated mcp login SERVER [--no-browser] [--file PATH | --permission-config PATH ...] [--reset-dcr-registration | --retry-dcr-registration]"

var errMCPLoginUsage = errors.New(mcpLoginUsage)

type mcpLoginArgs struct {
	server            string
	noBrowser         bool
	permissionConfigs []string
	file              string
	dcrAction         mcp.OAuthDCRLoginAction
}

func parseMCPLoginArgs(args []string, out io.Writer) (mcpLoginArgs, error) {
	var parsed mcpLoginArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			_, _ = fmt.Fprintln(out, "Usage: "+strings.TrimPrefix(mcpLoginUsage, "usage: ")+"\n\nAuthorize one operator-configured OAuth MCP server. --no-browser prints the terminal authorization URL. --permission-config selects trusted operator settings and is repeatable. DCR registration recovery requires exactly one explicit --reset-dcr-registration or --retry-dcr-registration operation.")
			return mcpLoginArgs{}, flag.ErrHelp
		}
		handled, err := parseMCPLoginOption(arg, args, &i, &parsed)
		if err != nil {
			return mcpLoginArgs{}, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") || parsed.server != "" {
			return mcpLoginArgs{}, errMCPLoginUsage
		}
		parsed.server = arg
	}
	if parsed.server == "" {
		return mcpLoginArgs{}, errMCPLoginUsage
	}
	return parsed, nil
}

func parseMCPLoginOption(arg string, args []string, index *int, parsed *mcpLoginArgs) (bool, error) {
	switch arg {
	case "--no-browser":
		if parsed.noBrowser {
			return false, errMCPLoginUsage
		}
		parsed.noBrowser = true
		return true, nil
	case "--reset-dcr-registration":
		if parsed.dcrAction != mcp.OAuthDCRLoginReuse {
			return false, errMCPLoginUsage
		}
		parsed.dcrAction = mcp.OAuthDCRLoginResetRegistration
		return true, nil
	case "--retry-dcr-registration":
		if parsed.dcrAction != mcp.OAuthDCRLoginReuse {
			return false, errMCPLoginUsage
		}
		parsed.dcrAction = mcp.OAuthDCRLoginRetryRegistration
		return true, nil
	case mcpFileFlag, "--permission-config":
		return parseMCPLoginPathOption(arg, args, index, parsed)
	}
	if path, ok := strings.CutPrefix(arg, "--file="); ok {
		return setMCPLoginFile(path, parsed)
	}
	if path, ok := strings.CutPrefix(arg, "--permission-config="); ok {
		return addMCPLoginPermissionConfig(path, parsed)
	}
	return false, nil
}

func parseMCPLoginPathOption(option string, args []string, index *int, parsed *mcpLoginArgs) (bool, error) {
	if *index+1 >= len(args) {
		return false, errMCPLoginUsage
	}
	*index = *index + 1
	path := args[*index]
	if strings.HasPrefix(path, "-") {
		return false, errMCPLoginUsage
	}
	if option == mcpFileFlag {
		return setMCPLoginFile(path, parsed)
	}
	return addMCPLoginPermissionConfig(path, parsed)
}

func setMCPLoginFile(path string, parsed *mcpLoginArgs) (bool, error) {
	if path == "" || parsed.file != "" || len(parsed.permissionConfigs) != 0 {
		return false, errMCPLoginUsage
	}
	parsed.file = path
	return true, nil
}

func addMCPLoginPermissionConfig(path string, parsed *mcpLoginArgs) (bool, error) {
	if path == "" || parsed.file != "" {
		return false, errMCPLoginUsage
	}
	parsed.permissionConfigs = append(parsed.permissionConfigs, path)
	return true, nil
}

func selectMCPLoginServer(profiles *cliconfig.MCPProfiles, name string) (mcp.ServerConfig, error) {
	if server, ok := profiles.OAuthServer(name); ok {
		if server.OAuth.CredentialStore == nil || server.OAuth.CredentialReader != nil {
			return mcp.ServerConfig{}, fmt.Errorf("MCP server %q uses read-only environment credentials; environment credentials cannot be mutated, so set mcp.servers[].auth.oauth.credentials.mode: local", server.Name)
		}
		return server, nil
	}
	if profiles != nil {
		for _, server := range profiles.Servers {
			if strings.EqualFold(server.Name, name) {
				return mcp.ServerConfig{}, fmt.Errorf("MCP server %q is not OAuth-enabled; set mcp.servers[].auth.mode: oauth", server.Name)
			}
		}
	}
	return mcp.ServerConfig{}, fmt.Errorf("MCP server %q is not configured; add it under mcp.servers[] with auth.mode: oauth", name)
}

func mcpLoginRemedy(err error) error {
	var provider *oauthlogin.AuthorizationErrorResponse
	var rejected *oauthlogin.CallbackRejectedError
	var bind *oauthlogin.CallbackBindError
	switch {
	case errors.Is(err, mcp.ErrOAuthDCRRecoveryRequired):
		switch mcp.OAuthDCRRecoveryCategoryOf(err) {
		case mcp.OAuthDCRRecoveryPending:
			return errors.New("MCP OAuth DCR previous registration attempt did not complete and its exact safe failure stage was not recorded; use --retry-dcr-registration only if creating a duplicate or orphan client is acceptable")
		case mcp.OAuthDCRRecoveryRegistrationOutcomeUnknown:
			return errors.New("MCP OAuth DCR registration request outcome is unknown; use --retry-dcr-registration only if a possible orphan client is acceptable")
		case mcp.OAuthDCRRecoveryResponseInvalid:
			return errors.New("the OAuth provider returned a registration response that Mecatl could not safely use for DCR; use --retry-dcr-registration only if a possible orphan client is acceptable")
		case mcp.OAuthDCRRecoveryReadyPersistence:
			return errors.New("MCP OAuth DCR registration response was accepted but the ready record was not persisted; use --retry-dcr-registration only if a possible orphan client is acceptable")
		case mcp.OAuthDCRRecoveryResetRequired:
			return errors.New("MCP OAuth DCR valid ready registration identity differs from the current profile, principal, canonical resource, or exact issuer; use --reset-dcr-registration")
		case mcp.OAuthDCRRecoveryPendingIdentityMismatch:
			return errors.New("MCP OAuth DCR pending registration identity does not match the current configuration; restore the matching profile, principal, canonical resource, and exact issuer, then use --retry-dcr-registration")
		default:
			return errors.New("MCP OAuth DCR registration state is corrupt or unreadable; do not retry or reset registration. Preserve the records and configuration without editing, deleting, or renaming them. Contact the deployment operator or support team with only the server name and redacted command error; never send credential contents, OAuth URLs, client IDs, tokens, keys, or a raw response. Reset/retry do not repair corrupt state or revoke an upstream client")
		}
	case errors.As(err, &provider) && errors.Is(err, app.ErrMCPLoginAuthorization):
		return fmt.Errorf("MCP OAuth authorization server rejected login (%s); review the requested scopes and provider policy", provider.Sanitized())
	case errors.As(err, &rejected) && errors.Is(err, app.ErrMCPLoginAuthorization):
		return fmt.Errorf("MCP OAuth browser callback was rejected (%s); retry login and complete the newest browser flow", rejected.Sanitized())
	case errors.As(err, &bind) && errors.Is(err, app.ErrMCPLoginAuthorization):
		if bind.Reason == oauthlogin.CallbackBindAddressInUse {
			return errors.New("MCP OAuth callback listener is already in use; stop the other login process and retry")
		}
		return errors.New("MCP OAuth callback listener is unavailable; check local callback permissions and retry")
	case errors.Is(err, app.ErrMCPLoginConfig):
		return errors.New("MCP OAuth login configuration is invalid; verify auth.mode: oauth and credentials.mode: local")
	case errors.Is(err, app.ErrMCPLoginAuthorization):
		return errors.New("MCP OAuth authorization is unavailable or login was rejected; retry login and complete the browser callback")
	case errors.Is(err, app.ErrMCPLoginConnect):
		return errors.New("MCP OAuth login could not connect to or verify the MCP server; verify the server URL, TLS, and network origin policy")
	case errors.Is(err, app.ErrMCPLoginCredential):
		return errors.New("MCP OAuth login did not establish a durable credential; verify the local credential store path and encryption key")
	case errors.Is(err, app.ErrMCPLoginCleanup):
		return errors.New("MCP OAuth login could not clean up its temporary session; retry after checking local callback availability")
	default:
		return err
	}
}

var executeMCPLogin = func(ctx context.Context, server mcp.ServerConfig, runtimeOpts oauthlogin.Options, loginOpts app.MCPLoginOptions) error {
	runtime, err := oauthlogin.New(runtimeOpts)
	if err != nil {
		return errors.New("MCP OAuth login runtime unavailable")
	}
	return app.LoginMCPWithOptions(ctx, server, runtime, loginOpts)
}

func loadMCPLoginProfiles(explicit []string) (*cliconfig.MCPProfiles, error) {
	return loadMCPLoginProfilesSelected(explicit, "")
}

func loadMCPLoginProfilesSelected(explicit []string, selected string) (*cliconfig.MCPProfiles, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true, ExplicitFiles: explicit}, xdgconfig.OSEnv)
	var operator *permconfig.MCPSection
	if resolver != nil {
		operator = resolver.OperatorMCP()
	}
	return cliconfig.LoadMCPProfiles(cliconfig.MCPProfileLoadOptions{Operator: operator, LookupEnv: os.LookupEnv, SelectedName: selected})
}

func runMCPLogin(args []string, stdout io.Writer, stderr ...io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runMCPLoginContext(ctx, args, stdout, stderr...)
}

func runMCPLoginContext(ctx context.Context, args []string, stdout io.Writer, stderr ...io.Writer) error {
	diagnosticOut := io.Discard
	if len(stderr) > 0 && stderr[0] != nil {
		diagnosticOut = stderr[0]
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := parseMCPLoginArgs(args, stdout)
	if err != nil {
		return err
	}

	explicit := parsed.permissionConfigs
	selected := ""
	if parsed.file != "" {
		selected = parsed.file
	} else if len(explicit) > 0 {
		selected = explicit[0]
	}
	path, pathErr := mcpSettingsFile(selected)
	if pathErr != nil {
		return pathErr
	}
	extraSources := explicit
	if len(extraSources) > 0 {
		extraSources = extraSources[1:]
	}
	sources, sourceErr := mcpSettingsSources(path, extraSources)
	if sourceErr != nil {
		return sourceErr
	}
	for _, source := range sources {
		_, _ = fmt.Fprintf(stdout, "MCP settings source: %s\n", source.path)
	}
	for _, source := range sources {
		if source.config.MCP != nil {
			_, _ = fmt.Fprintf(stdout, "MCP settings winner: %s\n", source.path)
			break
		}
	}
	if parsed.file != "" {
		explicit = []string{path}
	}
	profiles, err := loadMCPLoginProfilesSelected(explicit, parsed.server)
	if err != nil {
		return err
	}
	defer func() { _ = profiles.Close() }()

	server, err := selectMCPLoginServer(profiles, parsed.server)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(stdout, "MCP onboarding: preparing browser authorization")
	// PinCallbackPath fixes the callback's path (not its port) for the preregistered
	// and CIMD client kinds loadOAuthClient (mcpprofile.go) populates directly onto
	// opts.Client: both commit to a redirect_uri that must be registered ahead of
	// time, and a random callback path can never match a value fixed in advance.
	// Both target MCP-shaped, RFC 8252-aware authorization servers, which accept any
	// port for a registered loopback redirect_uri as long as the path matches — so
	// unlike oauthlogin.ExactRedirectURL (a fully fixed callback, port included, for
	// a general-purpose OIDC target that cannot be assumed to implement RFC 8252
	// dynamic-port matching), this login still gets an unpredictable port every run,
	// preserving the squatting resistance a foreseeable port would give up.
	//
	// A DCR-configured server (opts.Client.DCR) never reaches this fixed path: it
	// registers its own random redirect_uri at registration time and is driven
	// through LoginMCPWithOptions's AuthorizeWithCallbackPath call instead, whose
	// explicit registration-bound callback path always takes priority over
	// PinCallbackPath (see oauthlogin.resolveCallbackMode's case order) — so setting
	// PinCallbackPath here unconditionally is safe for every client kind
	// selectMCPLoginServer can return.
	opts := oauthlogin.Options{NoBrowser: parsed.noBrowser, PinCallbackPath: true}
	if parsed.noBrowser {
		opts.URLWriter = stdout
	}
	_, _ = fmt.Fprintln(stdout, "MCP onboarding: verifying MCP connection")
	if err := executeMCPLogin(ctx, server, opts, app.MCPLoginOptions{DCRAction: parsed.dcrAction, Diagnostics: slogdiag.NewText(diagnosticOut)}); err != nil {
		return mcpLoginRemedy(err)
	}
	_, err = fmt.Fprintf(stdout, "MCP OAuth login succeeded for %s\n", server.Name)
	return err
}
