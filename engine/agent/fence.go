package agent

import "strings"

// fence.go is the single source of truth for the UNTRUSTED-content fencing the
// harness uses to quarantine model- and peer-authored text inside a prompt it also
// builds trusted instructions into. Every place that interpolates untrusted data
// into a model-visible prompt — team turn prompts, the synthesis prompt, the
// ask-review prompt, and the guardrail content checker — wraps that data with the
// same fence and the same framing-neutralisation, so a crafted body cannot forge the
// fence or a section header to break out of its block and smuggle instructions to
// the model. The helpers are exported because the fence must be byte-identical
// across every prompt builder, including ones in other packages.

// UntrustedFence is the delimiter wrapping an untrusted block in a model-visible
// prompt. Text BETWEEN a matching open/close pair is data (peer-, operator-, or
// tool-authored), never harness instructions. The marker is chosen to be unlikely in
// prose and is neutralised out of any enclosed body by NeutraliseFraming, so an
// injected body cannot forge its own open/close pair to break out of its block.
const UntrustedFence = "<<<UNTRUSTED"

// WriteUntrustedBlock writes body to b wrapped in a matched UntrustedFence pair, with
// the fence markers and framing headers neutralised out of body first so it cannot
// forge its own closing fence (or a fresh harness section) to escape the block. Use
// it for any untrusted data interpolated into a model-visible prompt.
func WriteUntrustedBlock(b *strings.Builder, body string) {
	b.WriteString(UntrustedFence + "\n")
	b.WriteString(NeutraliseFraming(body))
	b.WriteString("\n" + UntrustedFence + "\n")
}

// FenceUntrusted is the string-returning form of WriteUntrustedBlock: it returns body
// wrapped in a matched UntrustedFence pair with the fence markers and framing headers
// neutralised out of body first. Use WriteUntrustedBlock when you already hold a
// strings.Builder; use this when a caller just wants the wrapped string (e.g. a tool
// interpolating untrusted external content — WebSearch results, LLM01). The bytes are
// identical to WriteUntrustedBlock's, so there is exactly one fence implementation.
func FenceUntrusted(body string) string {
	var b strings.Builder
	WriteUntrustedBlock(&b, body)
	return b.String()
}

// NeutraliseFraming defangs the literal framing markers a model-visible prompt uses
// so an untrusted body cannot forge them: it strips the fence delimiter and the
// recognised section headers (e.g. "Team goal:", "Tool:", "message from ...") that
// would otherwise let a crafted body close its block early or fabricate a new
// "harness" section. Matching is case-insensitive on whole lines for the headers and
// substring for the fence. Apply it to any trusted-but-model-influenced value
// (a team goal, a member name) and to every fenced body (via WriteUntrustedBlock).
func NeutraliseFraming(s string) string {
	s = strings.ReplaceAll(s, UntrustedFence, "[redacted-marker]")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		trimmed := strings.ToLower(strings.TrimSpace(ln))
		if framingHeader(trimmed) {
			lines[i] = "[redacted-framing]"
		}
	}
	return strings.Join(lines, "\n")
}

// framingHeader reports whether a (lower-cased, trimmed) line matches one of the
// literal section headers a harness-composed, model-visible text emits — the team
// turn/synthesis prompt, the ask-review prompt, the model-router prompt, and the
// subagent/Parallel FAILED result — so an untrusted body interpolated into one of them
// cannot forge a fresh "harness" section to smuggle instructions. It is the single list
// every path that calls NeutraliseFraming shares — extend it whenever a NEW literal
// header is introduced into a model-visible text those paths build.
func framingHeader(trimmed string) bool {
	switch {
	case trimmed == "new messages for you:",
		trimmed == "team goal:",
		trimmed == "team status:",
		trimmed == "team roster:",
		trimmed == "your role:",
		trimmed == "recorded findings:",
		trimmed == "messages sent to you:",
		// Ask-review prompt headers (buildAskReviewPrompt): defense-in-depth on top of
		// the load-bearing UntrustedFence — the fenced command cannot forge a fresh
		// trusted section either.
		trimmed == "policy:",
		trimmed == "tool:",
		trimmed == "requested command:",
		// Model-router prompt headers (buildModelRoutePrompt, ADR 0031): defense-in-depth
		// on top of the load-bearing UntrustedFence — the fenced task prompt cannot forge
		// a fresh trusted section or a verdict-shaped "category:" line that the classifier
		// might echo. parseRouterVerdict's whole-output-single-object rule is the primary
		// guard; this neutralises a leading forged header inside the fence.
		trimmed == "categories:",
		trimmed == "task to classify:",
		strings.HasPrefix(trimmed, "category:"),
		strings.HasPrefix(trimmed, "if no category clearly fits"),
		strings.HasPrefix(trimmed, "respond with only "),
		strings.HasPrefix(trimmed, "execution context:"),
		strings.HasPrefix(trimmed, "why the static policy could not resolve it:"),
		strings.HasPrefix(trimmed, "- message from "),
		strings.HasPrefix(trimmed, "you have claimed task "),
		strings.HasPrefix(trimmed, "findings from "),
		strings.HasPrefix(trimmed, "last words from "),
		strings.HasPrefix(trimmed, "completed tasks for "),
		// Team turn-prompt harness note (retryTurnNote): a peer message body in the SAME
		// prompt is neutralised, so without this a peer could forge a "NOTE FROM THE
		// HARNESS: your previous turn FAILED …" line into the target member's prompt.
		strings.HasPrefix(trimmed, "note from the harness:"),
		// Subagent/Parallel FAILED-result headers (subagentErrorBody's two halves are
		// provider- and child-authored, neutralised in that one composer). These are the
		// lines the harness itself emits around them in the PARENT's conversation:
		// the resume handle, the demoted-context label, and the bracketed stop-reason /
		// next-action notes. A forged copy could redirect a resume or fabricate a
		// harness instruction next to the real one.
		strings.HasPrefix(trimmed, "agentid:"),
		strings.HasPrefix(trimmed, "last activity before the failure:"),
		strings.HasPrefix(trimmed, "[the subagent "),
		strings.HasPrefix(trimmed, "[subagent "):
		return true
	}
	return false
}

// StripLoneCodeFence removes a single surrounding ```…``` fence (optionally
// language-tagged) from s, returning the inner text trimmed; if s is not a lone
// fenced block it is returned unchanged. It exists so a verdict parser can accept the
// one benign wrapper a model might add around its single JSON object without
// reopening the door to arbitrary surrounding prose. It is the shared
// implementation both the ask-review and guardrail verdict parsers use — a
// security-sensitive parser that must never diverge.
func StripLoneCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	inner := s[3 : len(s)-3]
	// Drop a leading language tag line (```json\n…), if any.
	if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
		first := strings.TrimSpace(inner[:nl])
		if first == "" || !strings.ContainsAny(first, " \t{}\"") {
			inner = inner[nl+1:]
		}
	}
	return strings.TrimSpace(inner)
}
