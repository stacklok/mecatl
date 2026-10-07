// Command mecabroker runs the process-local ToolHive MCP broker behind an
// authenticated gRPC boundary. Browser OAuth routes share the same lifecycle
// but remain unauthenticated; opaque broker-created state is their authority.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const (
	defaultConfigFile   = "/etc/mecabroker/broker.json"
	defaultAdminAddress = "127.0.0.1:8081"
	adminCheckTimeout   = 5 * time.Second
)

type brokerLifecycle interface {
	Start() <-chan error
	Close(context.Context) error
}

var newProduction = func(ctx context.Context, cfg mcpbrokerserver.ProductionConfig) (brokerLifecycle, error) {
	return mcpbrokerserver.NewProduction(ctx, cfg)
}

func main() {
	if len(os.Args) == 2 {
		var err error
		switch os.Args[1] {
		case "health":
			err = requestLocalAdmin(http.MethodGet, "/healthz", adminCheckTimeout)
		case "ready":
			err = requestLocalAdmin(http.MethodGet, "/readyz", adminCheckTimeout)
		case "drain":
			cfg, configErr := readConfig(defaultConfigFile)
			if configErr != nil {
				err = configErr
			} else {
				err = requestLocalAdmin(http.MethodGet, "/drain", cfg.drainRequestTimeout())
			}
		default:
			goto serve
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "mecabroker: local administration failed")
			os.Exit(1)
		}
		return
	}
serve:
	cfg, level, warning, err := parseFlagsWithLogging()
	if err != nil {
		reportStartupError(os.Stderr, "configuration", err)
		os.Exit(1)
	}
	logger := cliconfig.NewTextLogger(os.Stderr, level, warning)
	// ToolHive's ambient slog is not an operator-safe boundary: its token
	// reader may attach tsid and raw dependency errors. Keep Mecatl diagnostics
	// on the explicit injected logger while suppressing ambient ToolHive output.
	installAmbientSlogDiscard()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, slogdiag.NewFromLogger(logger)); err != nil {
		reportStartupError(os.Stderr, "startup or serving", publicStartupError(err))
		os.Exit(1)
	}
}

func installAmbientSlogDiscard() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})))
}

func reportStartupError(w io.Writer, stage string, err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "mecabroker: %s failed: %s\n", stage, err)
}

func run(ctx context.Context, cfg fileConfig, diagnostics port.Diagnostics) error {
	productionConfig, err := cfg.loadProductionConfig(diagnostics)
	if err != nil {
		return err
	}
	lifecycle, err := newProduction(ctx, productionConfig)
	if err != nil {
		return err
	}
	var closeOnce sync.Once
	var closeErr error
	shutdown := func() error {
		closeOnce.Do(func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), cfg.Drain.ListenerShutdownTimeout.value())
			defer cancel()
			closeErr = lifecycle.Close(closeCtx)
		})
		return closeErr
	}
	errs := lifecycle.Start()
	select {
	case <-ctx.Done():
		return shutdown()
	case <-errs:
		return errors.Join(errors.New("broker listener stopped"), shutdown())
	}
}

func requestLocalAdmin(method, path string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(method, "http://"+defaultAdminAddress+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("local drain rejected")
	}
	return nil
}

func publicStartupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "broker configuration"):
		return errors.New("broker configuration invalid")
	case strings.HasPrefix(message, "complete "):
		return errors.New("broker configuration incomplete")
	case strings.HasPrefix(message, "load server identity"):
		return errors.New("load server identity")
	case strings.HasPrefix(message, "read workload-JWT trust bundle"):
		return errors.New("read workload-JWT trust bundle")
	case strings.HasPrefix(message, "protected "):
		return errors.New("protected storage unavailable")
	case strings.HasPrefix(message, "broker listener stopped"):
		return errors.New("broker listener stopped")
	case strings.HasPrefix(message, "local drain rejected"):
		return errors.New("local drain rejected")
	default:
		return errors.New("broker startup or serving failed")
	}
}
