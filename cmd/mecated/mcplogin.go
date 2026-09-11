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
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const mcpLoginUsage = "usage: mecated mcp login SERVER [--no-browser] [--permission-config PATH ...] [--reset-dcr-registration | --retry-dcr-registration]"

var errMCPLoginUsage = errors.New(mcpLoginUsage)

type mcpLoginArgs struct {
	server            string
	noBrowser         bool
	permissionConfigs []string
	dcrAction         mcp.OAuthDCRLoginAction
}

func parseMCPLoginArgs(args []string, out io.Writer) (mcpLoginArgs, error) {
	var parsed mcpLoginArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			_, _ = fmt.Fprintln(out, "Usage: "+strings.TrimPrefix(mcpLoginUsage, "usage: ")+"\n\nAuthorize one operator-configured OAuth MCP server. --no-browser prints the terminal authorization URL. --permission-config selects trusted operator settings and is repeatable. DCR registration recovery requires exactly one explicit --reset-dcr-registration or --retry-dcr-registration operation.")
			return mcpLoginArgs{}, flag.ErrHelp
		case "--no-browser":
			if parsed.noBrowser {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			parsed.noBrowser = true
		case "--reset-dcr-registration":
			if parsed.dcrAction != mcp.OAuthDCRLoginReuse {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			parsed.dcrAction = mcp.OAuthDCRLoginResetRegistration
		case "--retry-dcr-registration":
			if parsed.dcrAction != mcp.OAuthDCRLoginReuse {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			parsed.dcrAction = mcp.OAuthDCRLoginRetryRegistration
		case "--permission-config":
			if i+1 >= len(args) {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			path := args[i+1] // #nosec G602 -- the immediately preceding bound check proves i+1 is valid.
			if path == "" || strings.HasPrefix(path, "-") {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			i++
			parsed.permissionConfigs = append(parsed.permissionConfigs, path)
		default:
			if path, ok := strings.CutPrefix(arg, "--permission-config="); ok {
				if path == "" {
					return mcpLoginArgs{}, errMCPLoginUsage
				}
				parsed.permissionConfigs = append(parsed.permissionConfigs, path)
				continue
			}
			if len(arg) > 0 && arg[0] == '-' {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			if parsed.server != "" {
				return mcpLoginArgs{}, errMCPLoginUsage
			}
			parsed.server = arg
		}
	}
	if parsed.server == "" {
		return mcpLoginArgs{}, errMCPLoginUsage
	}
	return parsed, nil
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
		default:
			return errors.New("MCP OAuth DCR registration state requires separate operator repair; do not retry or reset registration")
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
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true, ExplicitFiles: explicit}, xdgconfig.OSEnv)
	var operator *permconfig.MCPSection
	if resolver != nil {
		operator = resolver.OperatorMCP()
	}
	return cliconfig.LoadMCPProfiles(cliconfig.MCPProfileLoadOptions{Operator: operator, LookupEnv: os.LookupEnv})
}

func runMCPLogin(args []string, stdout io.Writer) error {
	parsed, err := parseMCPLoginArgs(args, stdout)
	if err != nil {
		return err
	}

	profiles, err := loadMCPLoginProfiles(parsed.permissionConfigs)
	if err != nil {
		return err
	}
	defer func() { _ = profiles.Close() }()

	server, err := selectMCPLoginServer(profiles, parsed.server)
	if err != nil {
		return err
	}

	opts := oauthlogin.Options{NoBrowser: parsed.noBrowser}
	if parsed.noBrowser {
		opts.URLWriter = stdout
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := executeMCPLogin(ctx, server, opts, app.MCPLoginOptions{DCRAction: parsed.dcrAction}); err != nil {
		return mcpLoginRemedy(err)
	}
	_, err = fmt.Fprintf(stdout, "MCP OAuth login succeeded for %s\n", server.Name)
	return err
}
