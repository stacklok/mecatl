// Command mecaexec drives an executor's Files and Commands services through
// executorclient, for manual testing.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

const usage = `usage: mecaexec [flags] <op> [args]

ops:
  stat PATH            read PATH            ls [PATH]
  create PATH <stdin   replace PATH <stdin  replace-stale PATH <stdin
  glob PATTERN         grep PATTERN [GLOB]
  rm PATH              mv FROM TO           cp FROM TO
  run COMMAND

flags:
`

type config struct {
	addr, ca, cert, key, serverName, root string
	timeout, fileTimeout                  time.Duration
	maxFileBytes, maxOutputBytes          int
}

func main() {
	cfg, args := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}
	code, err := run(ctx, cfg, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mecaexec: %v\nclass: %s\n", err, classify(err))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func parseFlags() (config, []string) {
	var cfg config
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:7443", "executor address")
	flag.StringVar(&cfg.ca, "ca", "", "CA certificate that signed the executor's certificate")
	flag.StringVar(&cfg.cert, "cert", "", "client certificate")
	flag.StringVar(&cfg.key, "key", "", "client private key")
	flag.StringVar(&cfg.serverName, "server-name", "localhost", "expected executor TLS name")
	flag.StringVar(&cfg.root, "root", "/workspace", "executor workspace root")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "deadline for the operation")
	flag.DurationVar(&cfg.fileTimeout, "file-timeout", 0, "deadline for one file call (default 1m)")
	flag.IntVar(&cfg.maxFileBytes, "max-file-bytes", 0, "client file read limit")
	flag.IntVar(&cfg.maxOutputBytes, "max-output-bytes", 0, "client command output limit")
	flag.Usage = func() {
		_, _ = fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 || cfg.ca == "" || cfg.cert == "" || cfg.key == "" {
		flag.Usage()
		os.Exit(2)
	}
	return cfg, flag.Args()
}

func transportCredentials(cfg config) (credentials.TransportCredentials, error) {
	pair, err := tls.LoadX509KeyPair(cfg.cert, cfg.key)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(cfg.ca)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificate in " + cfg.ca)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		RootCAs:      roots,
		ServerName:   cfg.serverName,
	}), nil
}

func run(ctx context.Context, cfg config, args []string) (int, error) {
	creds, err := transportCredentials(cfg)
	if err != nil {
		return 0, err
	}
	conn, err := executorclient.NewClient(cfg.addr, creds)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	opts := []executorclient.Option{
		executorclient.WithMaxFileBytes(cfg.maxFileBytes),
		executorclient.WithMaxOutputBytes(cfg.maxOutputBytes),
		executorclient.WithFileCallTimeout(cfg.fileTimeout),
	}
	if args[0] == "run" {
		if len(args) != 2 {
			return 2, errors.New("run takes one COMMAND argument")
		}
		return runCommand(ctx, executorclient.NewRunner(conn, cfg.root, nil, opts...), args[1])
	}
	ws := executorclient.NewWorkspace(conn, cfg.root, nil, opts...)
	return 0, fileOp(ctx, ws, args[0], args[1:])
}

func runCommand(ctx context.Context, r *executorclient.Runner, command string) (int, error) {
	res, err := r.Run(ctx, command)
	_, _ = io.WriteString(os.Stdout, res.Stdout)
	_, _ = io.WriteString(os.Stderr, res.Stderr)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(os.Stderr, "exit: %d\n", res.ExitCode)
	return res.ExitCode, nil
}

func fileOp(ctx context.Context, ws *executorclient.Workspace, op string, args []string) error {
	arity := map[string][]int{
		"stat": {1}, "read": {1}, "ls": {0, 1}, "create": {1}, "replace": {1},
		"replace-stale": {1}, "glob": {1}, "grep": {1, 2}, "rm": {1}, "mv": {2}, "cp": {2},
	}
	counts, ok := arity[op]
	if !ok {
		return fmt.Errorf("unknown op %q", op)
	}
	if !slices.Contains(counts, len(args)) {
		return fmt.Errorf("%s: wrong number of arguments", op)
	}
	switch op {
	case "create":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		_, err = ws.CreateFile(ctx, args[0], data)
		return err
	case "replace", "replace-stale":
		return replace(ctx, ws, args[0], op == "replace-stale")
	case "rm":
		return ws.Remove(ctx, args[0])
	case "mv":
		return ws.Rename(ctx, args[0], args[1])
	case "cp":
		_, err := ws.CopyFile(ctx, args[0], args[1])
		return err
	}
	return query(ctx, ws, op, args)
}

func query(ctx context.Context, ws *executorclient.Workspace, op string, args []string) error {
	switch op {
	case "stat":
		info, err := ws.Stat(ctx, args[0])
		if err != nil {
			return err
		}
		printInfo(info)
	case "read":
		data, err := ws.Read(ctx, args[0])
		if err != nil {
			return err
		}
		_, _ = os.Stdout.Write(data)
	case "ls":
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		infos, err := ws.ReadDir(ctx, dir)
		if err != nil {
			return err
		}
		for _, info := range infos {
			printInfo(info)
		}
	case "glob":
		paths, err := ws.Glob(ctx, args[0])
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println(p)
		}
	case "grep":
		pathGlob := ""
		if len(args) == 2 {
			pathGlob = args[1]
		}
		matches, err := ws.Grep(ctx, args[0], pathGlob)
		if err != nil {
			return err
		}
		for _, m := range matches {
			fmt.Printf("%s:%d:%s\n", m.Path, m.Line, m.Text)
		}
	}
	return nil
}

// replace with stale first replaces the file once, then retries with the
// version read before it, which the executor must refuse.
func replace(ctx context.Context, ws *executorclient.Workspace, path string, stale bool) error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	_, version, err := ws.ReadVersion(ctx, path)
	if err != nil {
		return err
	}
	if _, err := ws.ReplaceFile(ctx, path, version, data); err != nil || !stale {
		return err
	}
	fmt.Fprintln(os.Stderr, "first replace ok; replaying the old version")
	_, err = ws.ReplaceFile(ctx, path, version, data)
	return err
}

func printInfo(info tool.FileInfo) {
	kind := "file"
	if info.IsDir {
		kind = "dir"
	}
	fmt.Printf("%s\t%s\t%d\t%s\t%s\n",
		kind, info.Mode, info.Size, info.ModTime.Format(time.RFC3339), info.Name)
}

func classify(err error) string {
	var mismatch *tool.VersionMismatchError
	switch {
	case errors.Is(err, executorclient.ErrOutcomeUnknown):
		return "outcome-unknown"
	case errors.Is(err, executorclient.ErrLimitExceeded):
		return "limit-exceeded"
	case errors.Is(err, executorclient.ErrStorageTimeout):
		return "storage-timeout"
	case errors.Is(err, tool.ErrNoShell):
		return "no-shell"
	case errors.As(err, &mismatch):
		return "version-mismatch"
	case errors.Is(err, fs.ErrNotExist):
		return "not-exist"
	case errors.Is(err, fs.ErrExist):
		return "exist"
	case errors.Is(err, tool.ErrDirectoryNotEmpty):
		return "directory-not-empty"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "other"
	}
}
