package governance

import "testing"

// TestADR_0314_CopyMoveEvaluatesBothOperandsIndependently pins the conservative
// multi-resource permission semantics for Copy/Move that ADR 0314 requires: a
// path-scoped rule matching only ONE of the two operands (source or
// destination) must never be treated as authoritative for the whole call.
// Source and destination are evaluated independently against the rule set and
// the WORST effect wins — mirroring the existing Bash compound-command fold
// (doc 08 gauntlet #10: a deny on any segment denies the whole command).
//
// MUTATION-VERIFY: resolvePatterns is the only code path that evaluates the
// "source"/"destination" keys; nonBashPattern's single-field probe never sees
// either. Route Copy/Move through nonBashPattern instead (e.g. delete the
// resolvePatterns branch in resolve) and every sub-test below except
// AllowOnBothOperandsApprovesWhole goes red, since the derived pattern falls
// back to "" (tool-wide) and cannot distinguish source from destination.
func TestADR_0314_CopyMoveEvaluatesBothOperandsIndependently(t *testing.T) {
	for _, tool := range []string{"Copy", "Move"} {
		t.Run(tool+"/DenyOnSourceDeniesWhole", func(t *testing.T) {
			// A deny scoped to the SOURCE path must deny the call even though the
			// destination is unrestricted (mirrors gauntlet #10: a deny on any
			// resource wins over an allow on the others).
			rules := []Rule{
				{Scope: ScopeManaged, Tool: tool, Pattern: "/secret/*", Effect: Deny},
			}
			e := NewEvaluator(rules)
			got := e.Evaluate(tool, copyMoveArgs("/secret/creds.txt", "/tmp/out.txt"), false)
			if got.Effect != Deny {
				t.Fatalf("%s(source=/secret/creds.txt, dest=/tmp/out.txt) = %v (%s); want Deny (source-scoped deny must gate the whole call)", tool, got.Effect, got.Reason)
			}
		})

		t.Run(tool+"/DenyOnDestinationDeniesWhole", func(t *testing.T) {
			// Symmetric case: a deny scoped to the DESTINATION must deny the call
			// even though the source is unrestricted.
			rules := []Rule{
				{Scope: ScopeManaged, Tool: tool, Pattern: "/secret/*", Effect: Deny},
			}
			e := NewEvaluator(rules)
			got := e.Evaluate(tool, copyMoveArgs("/tmp/in.txt", "/secret/out.txt"), false)
			if got.Effect != Deny {
				t.Fatalf("%s(source=/tmp/in.txt, dest=/secret/out.txt) = %v (%s); want Deny (destination-scoped deny must gate the whole call)", tool, got.Effect, got.Reason)
			}
		})

		t.Run(tool+"/AllowOnSourceOnlyDoesNotApproveWhole", func(t *testing.T) {
			// An Allow that matches ONLY the source path must NOT approve the call
			// when the destination is unrestricted — the destination still resolves
			// through the no-match-default Ask, so the compound must be Ask, not
			// Allow. A rule that matched only one operand approving the whole call
			// would be the exact under-authorization the panel review flagged.
			rules := []Rule{
				{Scope: ScopeManaged, Tool: tool, Pattern: "/allowed/*", Effect: Allow},
			}
			e := NewEvaluator(rules)
			got := e.Evaluate(tool, copyMoveArgs("/allowed/in.txt", "/other/out.txt"), false)
			if got.Effect != Ask {
				t.Fatalf("%s(source=/allowed/in.txt [allow-matched], dest=/other/out.txt [unmatched]) = %v (%s); want Ask (destination must be independently evaluated, not silently approved by the source-scoped allow)", tool, got.Effect, got.Reason)
			}
		})

		t.Run(tool+"/AllowOnDestinationOnlyDoesNotApproveWhole", func(t *testing.T) {
			// Symmetric case: an Allow matching only the destination must not
			// approve an unmatched source.
			rules := []Rule{
				{Scope: ScopeManaged, Tool: tool, Pattern: "/allowed/*", Effect: Allow},
			}
			e := NewEvaluator(rules)
			got := e.Evaluate(tool, copyMoveArgs("/other/in.txt", "/allowed/out.txt"), false)
			if got.Effect != Ask {
				t.Fatalf("%s(source=/other/in.txt [unmatched], dest=/allowed/out.txt [allow-matched]) = %v (%s); want Ask (source must be independently evaluated, not silently approved by the destination-scoped allow)", tool, got.Effect, got.Reason)
			}
		})

		t.Run(tool+"/AllowOnBothOperandsApprovesWhole", func(t *testing.T) {
			// The positive control: when a rule allows BOTH operands (e.g. a broad
			// glob), the call resolves Allow. This proves the independent-operand
			// fold does not simply deny/ask everything — it converges to Allow when
			// both resources are genuinely covered.
			rules := []Rule{
				{Scope: ScopeManaged, Tool: tool, Pattern: "/workdir/*", Effect: Allow},
			}
			e := NewEvaluator(rules)
			got := e.Evaluate(tool, copyMoveArgs("/workdir/in.txt", "/workdir/out.txt"), false)
			if got.Effect != Allow {
				t.Fatalf("%s(source, dest both under /workdir/*) = %v (%s); want Allow", tool, got.Effect, got.Reason)
			}
		})
	}
}
