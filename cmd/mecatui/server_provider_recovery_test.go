package main

import (
	"testing"
	"time"
)

func TestServerProviderRecovery_Scenario5_FourHostConfigParity(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "--llm-recovery-budget=2m", "--llm-max-attempts=7"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	cfg, err := parseRunConfig(res)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.llmRecoveryBudget != 2*time.Minute || cfg.llmMaxAttempts != 7 {
		t.Fatalf("parsed recovery policy = %v/%d", cfg.llmRecoveryBudget, cfg.llmMaxAttempts)
	}
	appCfg := embeddedConfig(cfg, nil)
	if appCfg.LLMRecoveryBudget != 2*time.Minute || appCfg.LLMMaxAttempts != 7 {
		t.Fatalf("composed recovery policy = %v/%d", appCfg.LLMRecoveryBudget, appCfg.LLMMaxAttempts)
	}
	for _, args := range [][]string{{"mecatui", "--llm-recovery-budget=-1s"}, {"mecatui", "--llm-max-attempts=0"}} {
		res := resolveInvocation(args)
		err := res.err
		if err == nil {
			var bad config
			bad, err = parseRunConfig(res)
			if err == nil {
				err = validateRunConfig(bad)
			}
		}
		if err == nil {
			t.Fatalf("accepted invalid recovery flags %v", args)
		}
	}
}
