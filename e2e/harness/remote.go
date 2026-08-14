//go:build e2e

package harness

import (
	"fmt"
	"os"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Remote targets an operator-provided mecated (MECATL_E2E_TARGET=host:port).
// The harness cannot read its state dirs or server log, so StateDir/LogTail
// return "" and the file-/log-level specs Skip themselves. The remote server is
// expected to be configured like the local one (OpenRouter key, skills/soul/
// memory fixtures, --trust-project, the Skill/Parallel/Team allows) — the
// README documents the contract.
type Remote struct {
	addr       string
	workspace  string
	metricsURL string
	cli        *client.Client
}

// NewRemote dials addr. MECATL_E2E_WORKSPACE (the absolute workspace root ON
// THE SERVER HOST) is required; MECATL_E2E_METRICS_URL and
// MECATL_E2E_AUTH_TOKEN are optional.
func NewRemote(addr string) (*Remote, error) {
	ws := os.Getenv("MECATL_E2E_WORKSPACE")
	if ws == "" {
		return nil, fmt.Errorf("MECATL_E2E_TARGET is set (%s) but MECATL_E2E_WORKSPACE is not: the remote target needs the absolute session workspace root on the server host", addr)
	}
	cli, err := client.Dial(client.DialConfig{
		Server:    addr,
		AuthToken: os.Getenv("MECATL_E2E_AUTH_TOKEN"),
	})
	if err != nil {
		return nil, err
	}
	return &Remote{
		addr:       addr,
		workspace:  ws,
		metricsURL: os.Getenv("MECATL_E2E_METRICS_URL"),
		cli:        cli,
	}, nil
}

func (r *Remote) Client() *client.Client    { return r.cli }
func (r *Remote) Workspace() string         { return r.workspace }
func (r *Remote) MetricsURL() string        { return r.metricsURL }
func (r *Remote) StateDir(StateKind) string { return "" }
func (r *Remote) LogTail(int) string        { return "" }
func (r *Remote) IsLocal() bool             { return false }
func (r *Remote) Close() error              { return r.cli.Close() }
