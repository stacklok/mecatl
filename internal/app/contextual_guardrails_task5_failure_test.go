package app

import "testing"

func TestContextualGuardrailCheckerDownDefaultsFailClosed(t *testing.T) {
	if !guardrailFailClosed("") || !guardrailFailClosed("fail") {
		t.Fatal("empty and fail must be fail-closed")
	}
	if guardrailFailClosed("warn") {
		t.Fatal("only explicit warn may continue on checker outage")
	}
}
