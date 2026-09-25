package main

import (
	"testing"
	"time"
)

func TestServerProviderRecovery_Scenario5_FourHostConfigParity(t *testing.T) {
	cfg, err := parseFlags([]string{"--prompt=x", "--llm-recovery-budget=2m", "--llm-max-attempts=7"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.llmRecoveryBudget != 2*time.Minute || cfg.llmMaxAttempts != 7 {
		t.Fatalf("parsed recovery policy = %v/%d", cfg.llmRecoveryBudget, cfg.llmMaxAttempts)
	}
	appCfg := appConfig(cfg, nil, observability{})
	if appCfg.LLMRecoveryBudget != 2*time.Minute || appCfg.LLMMaxAttempts != 7 || appCfg.LLMPerAttemptTimeout != 300*time.Second || appCfg.LLMStreamIdleTimeout != 180*time.Second || appCfg.LLMBreakerThreshold != 5 || appCfg.LLMBreakerCooldown != 30*time.Second {
		t.Fatalf("composed recovery policy = %#v", appCfg)
	}
	for _, args := range [][]string{{"--prompt=x", "--llm-recovery-budget=-1s"}, {"--prompt=x", "--llm-max-attempts=0"}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("accepted invalid recovery flags %v", args)
		}
	}
}
