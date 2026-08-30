package ui

import (
	"context"
	"net/url"
	"path"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// ServerInfoGetter reads safe identity and sanitized diagnostic display data for the
// caller's already-known active provider from a remote server. It never returns
// connection instructions.
type ServerInfoGetter interface {
	GetServerInfo(ctx context.Context, providerID string) (client.ServerInfo, error)
}

// diagnosticsMsg carries a remote server-info result back to the update loop.
type diagnosticsMsg struct {
	info client.ServerInfo
	err  error
}

func (m Model) diagnosticsCmd() tea.Cmd {
	return func() tea.Msg {
		info, err := m.deps.ServerInfo.GetServerInfo(m.deps.Ctx, m.resolvedSessionModel.ProviderID)
		return diagnosticsMsg{info: info, err: err}
	}
}

func (m Model) runDiagnostics() (tea.Model, tea.Cmd) {
	m.palette.open = false
	m.palette.filtered = nil
	m.palette.cursor = 0
	m.prompt.Reset()
	if m.deps.Embedded {
		m.prompt.Rewrite(m.diagnosticsReport(m.deps.ClientBuild, m.deps.ServerImpl, m.deps.Server, "", "embedded"))
		return m.submitDiagnosticsReport()
	}
	if m.deps.ServerInfo == nil {
		m.prompt.Rewrite(m.diagnosticsReport("", "", "", "", "invalid-response"))
		return m.submitDiagnosticsReport()
	}
	return m, m.diagnosticsCmd()
}

func (m Model) diagnosticsReport(serverBuild, serverImplementation, displayServerEndpoint, llmProviderDisplayEndpoint, lookup string) string {
	provider, model, mode := unavailableText, unavailableText, unavailableText
	if m.resolvedSessionModel.ProviderID != "" {
		provider = diagnosticToken(m.resolvedSessionModel.ProviderID)
	}
	if m.resolvedSessionModel.ModelID != "" {
		model = diagnosticToken(m.resolvedSessionModel.ModelID)
	}
	if m.activeMode != "" {
		mode = diagnosticToken(m.activeMode)
	}
	report := "Mecatl diagnostics (current client state only):\n" +
		"platform: " + runtime.GOOS + "/" + runtime.GOARCH + "\n" +
		"client build: " + diagnosticToken(m.deps.ClientBuild) + "\n" +
		"server mode: " + map[bool]string{true: "embedded", false: "remote"}[m.deps.Embedded] + "\n" +
		"server build: " + diagnosticToken(serverBuild) + "\n" +
		"server implementation: " + diagnosticToken(serverImplementation) + "\n" +
		"server endpoint: " + diagnosticEndpoint(displayServerEndpoint) + "\n" +
		"LLM provider endpoint: " + diagnosticEndpoint(llmProviderDisplayEndpoint)
	if !m.deps.Embedded {
		report += "\nserver lookup: " + lookup
	}
	return report + "\n" +
		"active provider: " + provider + "\n" +
		"active model: " + model + "\n" +
		"permission mode: " + mode
}

// diagnosticToken admits only short, single-line identity tokens. This keeps a
// malformed server response from injecting untrusted content into the report.
func diagnosticToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 128 {
		return "unavailable"
	}
	for _, r := range s {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._/-+", r)
		if !valid {
			return "unavailable"
		}
	}
	return s
}

func diagnosticEndpoint(raw string) string {
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) {
		return unavailableText
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return unavailableText
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || strings.ContainsAny(u.Host, "\\/?#@") {
		return unavailableText
	}
	escapedPath := u.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	} else {
		escapedPath = path.Clean(escapedPath)
		if !strings.HasPrefix(escapedPath, "/") {
			escapedPath = "/" + escapedPath
		}
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host + escapedPath
}

func (m Model) handleDiagnostics(msg diagnosticsMsg) (tea.Model, tea.Cmd) {
	build, implementation, displayServerEndpoint, providerDisplayEndpoint, lookup := "", "", "", "", client.SafeInfoFailure(msg.err)
	if msg.err == nil {
		build, implementation, displayServerEndpoint, providerDisplayEndpoint, lookup = msg.info.BuildID, msg.info.ServerImplementation, msg.info.DisplayServerEndpoint, msg.info.LLMProviderDisplayEndpoint, "ok"
	}
	m.prompt.Rewrite(m.diagnosticsReport(build, implementation, displayServerEndpoint, providerDisplayEndpoint, lookup))
	return m.submitDiagnosticsReport()
}

func (m Model) submitDiagnosticsReport() (tea.Model, tea.Cmd) {
	if m.phase == phaseRunning {
		return m.enqueuePrompt()
	}
	return m.submitPrompt()
}
