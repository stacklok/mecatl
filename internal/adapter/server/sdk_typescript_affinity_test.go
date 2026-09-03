package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestADR_0290_TypeScriptRawHelperCompatibility(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript raw affinity source")
	}
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..")
	raw := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "raw.ts"))
	behavior := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "test", "compatibility.test.ts"))
	generated := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "gen", "mecatl", "v1", "harness_pb.ts"))

	for _, want := range []string{
		`export const SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID";`,
		"export function withSessionAffinity(sessionId: string, options: CallOptions = {}): CallOptions {",
		"const headers = new Headers(options.headers);",
		"headers.set(SESSION_ID_HEADER_NAME, sessionId);",
		"return { ...options, headers };",
		"headers.delete(SESSION_ID_HEADER_NAME);",
		"callOptions?.headers,",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("TypeScript raw affinity helper is missing %q", want)
		}
	}
	if strings.Count(raw, "callOptions?.headers,") != 2 {
		t.Error("raw helper must forward affinity headers to both unary and streaming transports")
	}
	if strings.Contains(generated, "MecatlSessionId") || strings.Contains(generated, "mecatlSessionId") {
		t.Error("raw affinity must use transport headers, not generated protobuf fields")
	}

	for _, report := range []string{"mecatl-sdk.api.md", "mecatl-sdk-node.api.md"} {
		api := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "etc", report))
		for _, want := range []string{
			`export const SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID";`,
			"export function withSessionAffinity(sessionId: string, options?: CallOptions): CallOptions;",
		} {
			if !strings.Contains(api, want) {
				t.Errorf("%s does not expose additive raw affinity API %q", report, want)
			}
		}
	}

	for _, want := range []string{
		`it("TestADR_0290_TypeScriptRawHelperCompatibility", async () => {`,
		"expect(grpcHeaders).toHaveLength(3);",
		"expect(headers.get(\"authorization\")).toBe(\"Bearer grpc-credential\");",
		"expect(httpRequests).toHaveLength(4);",
		"expect(request.headers.get(\"authorization\")).toBe(\"Bearer http-credential\");",
		"expect(httpRequests[3]?.headers.has(SESSION_ID_HEADER_NAME)).toBe(false);",
	} {
		if !strings.Contains(behavior, want) {
			t.Errorf("TypeScript raw affinity behavior test is missing %q", want)
		}
	}
}

func TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript affinity test source")
	}
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..")
	client := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "client.ts"))
	http := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "http.ts"))
	behavior := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "test", "session-affinity.test.ts"))

	for _, want := range []string{
		"function sessionAffinityOperations(",
		"withSessionAffinity(sessionId, options)",
		"withSessionAffinity(sourceSessionId)",
		"withSessionAffinity(sessionId)",
	} {
		if !strings.Contains(client, want) {
			t.Errorf("TypeScript high-level client does not centrally bind session affinity: missing %q", want)
		}
	}
	for _, want := range []string{
		"requestHeaders?: HeadersInit",
		"this.#request(route, body, signal, requestHeaders)",
	} {
		if !strings.Contains(http, want) {
			t.Errorf("HTTP control path does not retain stream affinity: missing %q", want)
		}
	}
	if !strings.Contains(behavior, "expect(httpSeen.every(({ affinity }) => affinity === sessionId)).toBe(true)") ||
		!strings.Contains(behavior, "expect(seen.every(({ affinity }) => affinity !== run.id)).toBe(true)") {
		t.Error("behavioral transport test must prove exact session affinity on HTTP and reject run-id substitution")
	}
}
