package mcpbrokergrpc

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/tlsreload"
)

// RemoteFactoryConfig supplies the canonical session transport's TLS and workload credentials.
type RemoteFactoryConfig struct {
	Target     string
	CAFile     string
	ServerName string
	TokenFile  string
	Transport  Config
}

// ProjectedTokenCredentials rereads one bounded projected workload token for
// every RPC and requires transport security.
type ProjectedTokenCredentials struct{ Path string }

func (c ProjectedTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	body, err := tlsreload.ReadCredentialFile(c.Path)
	if err != nil {
		return nil, errors.New("read projected MCP broker credential")
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid projected MCP broker credential")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

func (ProjectedTokenCredentials) RequireTransportSecurity() bool { return true }
