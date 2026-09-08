package mcpbrokerserver

import (
	"testing"
	"time"
)

func TestPublicListenerConfigRequiresExecuteMarginWhenExplicit(t *testing.T) {
	cfg := DefaultPublicListenerConfig()
	cfg.ExecuteDeadline = time.Second
	cfg.ReadTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	cfg.WriteTimeout = cfg.ExecuteDeadline + publicListenerExecuteMargin
	if cfg.validForExecute() {
		t.Fatal("listener timeouts at the deadline margin must be rejected")
	}
	cfg.ReadTimeout++
	cfg.WriteTimeout++
	if !cfg.validForExecute() {
		t.Fatal("listener timeouts above the deadline margin must be accepted")
	}
}
