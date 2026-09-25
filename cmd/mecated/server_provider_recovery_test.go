package main

import (
	"testing"
	"time"
)

func TestServerProviderRecovery_Scenario5_FourHostConfigParity(t *testing.T) {
	cfg, err := parseFlags([]string{"--llm-recovery-budget=2m", "--llm-max-attempts=7"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.llmRecoveryBudget != 2*time.Minute || cfg.llmMaxAttempts != 7 {
		t.Fatalf("parsed recovery policy = %v/%d", cfg.llmRecoveryBudget, cfg.llmMaxAttempts)
	}
	appCfg := appConfig(cfg, nil, nil, nil, nil, nil)
	if appCfg.LLMRecoveryBudget != 2*time.Minute || appCfg.LLMMaxAttempts != 7 {
		t.Fatalf("composed recovery policy = %v/%d", appCfg.LLMRecoveryBudget, appCfg.LLMMaxAttempts)
	}
	for _, args := range [][]string{{"--llm-recovery-budget=-1s"}, {"--llm-max-attempts=0"}} {
		bad, err := parseFlags(args)
		if err == nil {
			err = validateEffectiveConfig(bad)
		}
		if err == nil {
			t.Fatalf("accepted invalid recovery flags %v", args)
		}
	}
}
