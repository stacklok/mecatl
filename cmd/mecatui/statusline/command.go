package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/procgroup"
)

const maxCommandOutputBytes = 4 << 10

var errCommandOutputLimit = errors.New("status command output limit exceeded")

// PassthroughEnvError reports an invalid passthrough environment name. ReservedName
// is empty when the value does not match the supported environment-name grammar.
type PassthroughEnvError struct {
	ReservedName string
}

func (e *PassthroughEnvError) Error() string {
	if e.ReservedName != "" {
		return fmt.Sprintf("Invalid passthrough_env value. You cannot override reserved variable name %s.", e.ReservedName)
	}
	return "Invalid passthrough_env value. Values must match [A-Za-z_][A-Za-z0-9_]*."
}

// Command is a validated local status command. Path is an absolute executable and
// Args are passed literally. LaunchDir remains private command-runner state and is
// never projected into Input.
type Command struct {
	Path string
	Args []string
	// PassthroughEnv names additional parent environment variables explicitly
	// allowed into the command's otherwise fixed environment.
	PassthroughEnv []string
	LaunchDir      string
	// RefreshInterval optionally refreshes an otherwise idle command. Values below
	// one second are disabled; input changes still use the command debounce.
	RefreshInterval time.Duration
}

// Valid reports whether command specifies an absolute executable and literal args.
func (c Command) Valid() bool {
	if !filepath.IsAbs(c.Path) || !validCommandPart(c.Path) || (c.Path == "/bin/sh" && len(c.Args) == 0) {
		return false
	}
	for _, arg := range c.Args {
		if !validCommandPart(arg) {
			return false
		}
	}
	return c.ValidatePassthroughEnv() == nil
}

// ValidatePassthroughEnv reports whether PassthroughEnv has valid names that do
// not override values owned by the status-command environment.
func (c Command) ValidatePassthroughEnv() error {
	for _, name := range c.PassthroughEnv {
		if !validEnvName(name) {
			return &PassthroughEnvError{}
		}
		if reservedEnvName(name) {
			return &PassthroughEnvError{ReservedName: name}
		}
	}
	return nil
}

func reservedEnvName(name string) bool {
	switch name {
	case "HOME", "PATH", "TERM", "LANG", "LC_ALL", "COLUMNS", "LINES":
		return true
	default:
		return false
	}
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validCommandPart(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsRune(value, 0)
}

// NewCommandSource creates a local command-backed source. It passes complete
// raw Input JSON on stdin and never exposes command failures or captured output
// to the generated status line.
func NewCommandSource(command Command) Source {
	header := compileVariants(SurfaceTemplates{}, defaultHeaderTemplates())
	footer := compileVariants(SurfaceTemplates{}, defaultFooterTemplates())
	var ticks <-chan time.Time
	var ticker *time.Ticker
	if command.RefreshInterval >= time.Second {
		ticker = time.NewTicker(command.RefreshInterval)
		ticks = ticker.C
	}
	source := newSourceWithOptions(ticks, func(ctx context.Context, input Input) renderedStatusLine {
		fallback := renderTemplates(ctx, header, footer, input)
		if !procgroup.Supported() {
			return renderedStatusLine{line: fallback, err: errors.New("status command process trees unsupported")}
		}
		output, err := runCommand(ctx, command, input)
		if err != nil {
			return renderedStatusLine{line: fallback, err: err}
		}
		doc, ok := parse(strings.Trim(string(output), " \t\n\r\v\f"))
		if !ok {
			return renderedStatusLine{line: fallback, err: errors.New("invalid status command output")}
		}
		result := renderedStatusLine{line: fallback, headerSupplied: doc.Header.Present, footerSupplied: doc.Footer.Present}
		if result.headerSupplied {
			result.line.Header = doc.Header
		}
		if result.footerSupplied {
			result.line.Footer = doc.Footer
		}
		return result
	}, false, commandDebounce, commandDeadline)
	if ticker != nil {
		source.stopTicker = ticker.Stop
	}
	return source
}

func runCommand(ctx context.Context, command Command, input Input) ([]byte, error) {
	if !command.Valid() {
		return nil, errors.New("invalid status command")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = commandCWD(command, input)
	cmd.Env = commandEnv(command, input)
	procgroup.Configure(cmd)
	cmd.WaitDelay = commandDeadline
	cmd.Stdin = bytes.NewReader(payload)
	output := &boundedOutput{remaining: maxCommandOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if output.overflow {
		return nil, errCommandOutputLimit
	}
	if err != nil {
		return nil, err
	}
	return output.bytes(), nil
}

func commandCWD(command Command, input Input) string {
	if input.Workspace.Location == "local" && input.Workspace.Path != "" {
		return input.Workspace.Path
	}
	return command.LaunchDir
}

func commandEnv(command Command, input Input) []string {
	baseline := []string{"HOME", "PATH", "TERM", "LANG", "LC_ALL"}
	env := make([]string, 0, len(baseline)+2+len(command.PassthroughEnv))
	reserved := make(map[string]struct{}, len(baseline)+2)
	for _, key := range baseline {
		reserved[key] = struct{}{}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	if input.Terminal.Cols > 0 {
		env = append(env, "COLUMNS="+strconv.Itoa(input.Terminal.Cols))
	}
	if input.Terminal.Rows > 0 {
		env = append(env, "LINES="+strconv.Itoa(input.Terminal.Rows))
	}
	reserved["COLUMNS"] = struct{}{}
	reserved["LINES"] = struct{}{}
	for _, key := range command.PassthroughEnv {
		if _, owned := reserved[key]; owned {
			continue
		}
		reserved[key] = struct{}{}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type boundedOutput struct {
	buf       bytes.Buffer
	remaining int
	overflow  bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.buf.Write(p[:b.remaining])
		b.remaining = 0
		b.overflow = true
		return len(p), nil
	}
	b.buf.Write(p)
	b.remaining -= len(p)
	return len(p), nil
}

func (b *boundedOutput) bytes() []byte { return append([]byte(nil), b.buf.Bytes()...) }

var _ io.Writer = (*boundedOutput)(nil)
