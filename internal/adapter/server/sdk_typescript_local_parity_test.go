package server

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

func TestSDKTypescriptLocal_Scenario9_ReadyDocSchemaParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptLocalParityPaths(t)
	typescript := readParitySource(t, paths.spawnSource)
	typedFields := parseTypescriptStringManifest(
		t,
		paths.spawnSource,
		"// BEGIN MECATL_READY_DOCUMENT_FIELDS",
		"// END MECATL_READY_DOCUMENT_FIELDS",
		regexp.MustCompile(`^[a-z][a-z0-9_]*$`),
	)
	interfaceFields := typescriptInterfaceFields(t, typescript, "ReadyDocument")
	assertParitySets(t, "TypeScript ready-document manifest/interface", typedFields, interfaceFields)

	daemonHosting := readParitySource(t, paths.daemonHostingSource)
	readyDocBody := sourceBetween(t, daemonHosting, "type readyDoc struct {", "\n}")
	wireFields := sourceCaptureSet(
		t,
		readyDocBody,
		regexp.MustCompile("`json:\"([a-z][a-z0-9_]*)(?:,omitempty)?\"`"),
		"readyDoc JSON fields",
	)
	assertParitySets(t, "Go/TypeScript ready-document fields", wireFields, typedFields)

	typedSchema := singleSourceValue(
		t,
		typescript,
		regexp.MustCompile(`(?m)^const READY_SCHEMA = "([^"]+)";$`),
		"TypeScript ready-document schema",
	)
	wireSchema := singleSourceValue(
		t,
		daemonHosting,
		regexp.MustCompile(`(?m)^const readyDocSchema = "([^"]+)"$`),
		"Go ready-document schema",
	)
	if typedSchema != wireSchema {
		t.Fatalf("Go/TypeScript ready-document schema drift: TypeScript=%q Go=%q", typedSchema, wireSchema)
	}
}

func TestSDKTypescriptLocal_Scenario9_ClientMCPFeatureIdParity(t *testing.T) {
	t.Parallel()

	typescript := readParitySource(t, sdkTypescriptLocalParityPaths(t).spawnSource)
	typed := singleSourceValue(
		t,
		typescript,
		regexp.MustCompile(`(?m)^const CLIENT_MCP_ON_CREATE_FEATURE = "([a-z0-9_]+)";$`),
		"TypeScript client-MCP feature",
	)
	if typed != FeatureMCPServersOnCreate {
		t.Fatalf("Go/TypeScript client-MCP feature drift: TypeScript=%q Go=%q", typed, FeatureMCPServersOnCreate)
	}
}

func TestSDKTypescriptLocal_Scenario9_McpServerSpecFieldParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptLocalParityPaths(t)
	typed := parseTypescriptStringManifest(
		t,
		paths.toolSource,
		"// BEGIN MECATL_MCP_SERVER_SPEC_FIELDS",
		"// END MECATL_MCP_SERVER_SPEC_FIELDS",
		regexp.MustCompile(`^[a-z][a-z0-9_]*$`),
	)
	interfaceFields := typescriptInterfaceFields(
		t,
		readParitySource(t, paths.clientSource),
		"SessionMcpServer",
	)
	assertParitySets(t, "TypeScript MCP-server manifest/interface", typed, interfaceFields)

	descriptorFields := (&mecatlv1.McpServerSpec{}).ProtoReflect().Descriptor().Fields()
	wire := make(map[string]struct{}, descriptorFields.Len())
	for i := range descriptorFields.Len() {
		name := string(descriptorFields.Get(i).Name())
		if _, duplicate := wire[name]; duplicate {
			t.Fatalf("duplicate McpServerSpec field %q", name)
		}
		wire[name] = struct{}{}
	}
	assertParitySets(t, "Go/TypeScript McpServerSpec fields", wire, typed)
}

func TestSDKTypescriptLocal_Scenario9_SpawnFlagParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptLocalParityPaths(t)
	spawnSource := readParitySource(t, paths.spawnSource)
	spawnFlagManifest := sourceBetween(
		t,
		spawnSource,
		"// BEGIN MECATL_SPAWN_FLAGS",
		"// END MECATL_SPAWN_FLAGS",
	)
	typed := sourceCaptureSet(
		t,
		spawnFlagManifest,
		regexp.MustCompile(`"(--[a-z0-9-]+)"`),
		"TypeScript spawn flags",
	)

	mainSource := readParitySource(t, paths.mecatedMainSource)
	serveFlagSource := sourceBetween(t, mainSource, "func parseFlagsModeOut(", "\n}")
	serveFlagNames := sourceCaptureSet(
		t,
		serveFlagSource,
		regexp.MustCompile(`fs\.[A-Za-z0-9]*Var\(\s*[^,\n]+,\s*"([a-z0-9-]+)"`),
		"mecated serve flags",
	)
	serveFlags := make(map[string]struct{}, len(serveFlagNames))
	for name := range serveFlagNames {
		serveFlags["--"+name] = struct{}{}
	}
	if missing := setDifference(typed, serveFlags); len(missing) != 0 {
		t.Fatalf("TypeScript spawn uses flags mecated serve does not define: %v", missing)
	}

	if _, enabled := typed["--lifetime-pipe-fd"]; enabled {
		lifetimeSource := readParitySource(t, paths.lifetimeFDSource)
		checkBody := sourceBetween(t, lifetimeSource, "func checkLifetimePipeFD(fd int) error {", "\n}")
		if !strings.Contains(checkBody, "case syscall.S_IFSOCK:") ||
			!strings.Contains(checkBody, "return checkLifetimeSocketpairFD(fd)") {
			t.Fatal("TypeScript passes Node/Bun's socketpair-backed lifetime pipe, but checkLifetimePipeFD no longer admits and validates S_IFSOCK")
		}
	}
}

func TestSDKTypescriptLocal_Scenario9_ClientServerNameGrammarParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptLocalParityPaths(t)
	typescript := readParitySource(t, paths.toolSource)
	rules := sourceBetween(
		t,
		typescript,
		"// BEGIN MECATL_CLIENT_SERVER_NAME_RULES",
		"// END MECATL_CLIENT_SERVER_NAME_RULES",
	)
	typedClass := singleSourceValue(
		t,
		rules,
		regexp.MustCompile(`(?m)^export const CLIENT_SERVER_NAME_PATTERN = /\^\[([^]]+)\]\+\$/;$`),
		"TypeScript client-server name pattern",
	)
	typedRunes := regexpASCIISet(t, "^["+typedClass+"]$", "TypeScript client-server name pattern")

	clientMCPSource := readParitySource(t, paths.clientMCPSource)
	goBody := sourceBetween(t, clientMCPSource, "func clientServerNameRune(r rune) bool {", "\n}")
	wireRunes := goRunePredicateASCIISet(t, goBody)
	assertParitySets(t, "Go/TypeScript client-server name grammar", wireRunes, typedRunes)

	typedNameLen := typescriptIntConstant(t, rules, "MAX_CLIENT_SERVER_NAME_LEN")
	if typedNameLen != mcp.MaxClientServerNameLen {
		t.Fatalf("Go/TypeScript client-server name length drift: TypeScript=%d Go=%d", typedNameLen, mcp.MaxClientServerNameLen)
	}
	typedServerCount := typescriptIntConstant(t, rules, "MAX_CLIENT_SERVERS")
	if typedServerCount != mcp.MaxClientServers {
		t.Fatalf("Go/TypeScript client-server count drift: TypeScript=%d Go=%d", typedServerCount, mcp.MaxClientServers)
	}
}

func TestSDKTypescriptLocal_Scenario9_ToolOutputCapParity(t *testing.T) {
	t.Parallel()

	toolHostSource := readParitySource(t, sdkTypescriptLocalParityPaths(t).toolHostSource)
	manifest := sourceBetween(
		t,
		toolHostSource,
		"// BEGIN MECATL_TOOL_OUTPUT_CAP",
		"// END MECATL_TOOL_OUTPUT_CAP",
	)
	typed := typescriptIntConstant(t, manifest, "TOOLKIT_MAX_OUTPUT_BYTES")
	if typed != toolkit.MaxOutputBytes {
		t.Fatalf("Go/TypeScript tool-output cap drift: TypeScript=%d Go=%d", typed, toolkit.MaxOutputBytes)
	}
}

type localParityPaths struct {
	clientMCPSource     string
	clientSource        string
	daemonHostingSource string
	lifetimeFDSource    string
	mecatedMainSource   string
	spawnSource         string
	toolHostSource      string
	toolSource          string
}

func sdkTypescriptLocalParityPaths(t *testing.T) localParityPaths {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate local parity test source")
	}
	serverDir := filepath.Dir(filename)
	repoRoot := filepath.Join(serverDir, "..", "..", "..")
	return localParityPaths{
		clientMCPSource:     filepath.Join(repoRoot, "internal", "adapter", "mcp", "clientmcp.go"),
		clientSource:        filepath.Join(repoRoot, "sdk", "typescript", "src", "client.ts"),
		daemonHostingSource: filepath.Join(repoRoot, "cmd", "mecated", "daemonhosting.go"),
		lifetimeFDSource:    filepath.Join(repoRoot, "cmd", "mecated", "lifetimefd_unix.go"),
		mecatedMainSource:   filepath.Join(repoRoot, "cmd", "mecated", "main.go"),
		spawnSource:         filepath.Join(repoRoot, "sdk", "typescript", "src", "spawn.ts"),
		toolHostSource:      filepath.Join(repoRoot, "sdk", "typescript", "src", "tool-host.ts"),
		toolSource:          filepath.Join(repoRoot, "sdk", "typescript", "src", "tool.ts"),
	}
}

func assertParitySets(t *testing.T, label string, left, right map[string]struct{}) {
	t.Helper()
	missing := setDifference(left, right)
	extra := setDifference(right, left)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("%s drift: missing=%v extra=%v", label, missing, extra)
	}
}

func sourceCaptureSet(t *testing.T, source string, pattern *regexp.Regexp, label string) map[string]struct{} {
	t.Helper()
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatalf("locate %s", label)
	}
	values := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, duplicate := values[match[1]]; duplicate {
			t.Fatalf("duplicate %s value %q", label, match[1])
		}
		values[match[1]] = struct{}{}
	}
	return values
}

func typescriptInterfaceFields(t *testing.T, source, name string) map[string]struct{} {
	t.Helper()
	body := sourceBetween(t, source, "interface "+name+" {", "\n}")
	return sourceCaptureSet(
		t,
		body,
		regexp.MustCompile(`(?m)^\s*(?:readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)(?:\?)?:`),
		"TypeScript interface "+name+" fields",
	)
}

func typescriptIntConstant(t *testing.T, source, name string) int {
	t.Helper()
	pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^export const %s = ([0-9][0-9_]*);$`, regexp.QuoteMeta(name)))
	raw := singleSourceValue(t, source, pattern, "TypeScript integer constant "+name)
	value, err := strconv.Atoi(strings.ReplaceAll(raw, "_", ""))
	if err != nil {
		t.Fatalf("parse TypeScript integer constant %s=%q: %v", name, raw, err)
	}
	return value
}

func regexpASCIISet(t *testing.T, expression, label string) map[string]struct{} {
	t.Helper()
	pattern, err := regexp.Compile(expression)
	if err != nil {
		t.Fatalf("compile %s %q: %v", label, expression, err)
	}
	values := make(map[string]struct{})
	for value := rune(0); value < 128; value++ {
		if pattern.MatchString(string(value)) {
			values[string(value)] = struct{}{}
		}
	}
	return values
}

func goRunePredicateASCIISet(t *testing.T, body string) map[string]struct{} {
	t.Helper()
	expression := singleSourceValue(
		t,
		body,
		regexp.MustCompile(`(?m)^\s*return (.+)$`),
		"clientServerNameRune return expression",
	)
	rangePattern := regexp.MustCompile(`r >= ('(?:[^'\\]|\\.)+') && r <= ('(?:[^'\\]|\\.)+')`)
	singlePattern := regexp.MustCompile(`r == ('(?:[^'\\]|\\.)+')`)
	values := make(map[string]struct{})
	for _, match := range rangePattern.FindAllStringSubmatch(expression, -1) {
		first := goRuneLiteral(t, match[1])
		last := goRuneLiteral(t, match[2])
		if first > last || first < 0 || last >= 128 {
			t.Fatalf("clientServerNameRune contains unsupported range %s..%s", match[1], match[2])
		}
		for value := first; value <= last; value++ {
			values[string(value)] = struct{}{}
		}
	}
	for _, match := range singlePattern.FindAllStringSubmatch(expression, -1) {
		value := goRuneLiteral(t, match[1])
		if value < 0 || value >= 128 {
			t.Fatalf("clientServerNameRune contains unsupported non-ASCII literal %s", match[1])
		}
		values[string(value)] = struct{}{}
	}
	remainder := rangePattern.ReplaceAllString(expression, "")
	remainder = singlePattern.ReplaceAllString(remainder, "")
	remainder = strings.ReplaceAll(remainder, "||", "")
	remainder = strings.TrimSpace(remainder)
	if remainder != "" {
		t.Fatalf("clientServerNameRune uses an unparsed predicate %q", remainder)
	}
	if len(values) == 0 {
		t.Fatal("clientServerNameRune permits no parsed ASCII runes")
	}
	return values
}

func goRuneLiteral(t *testing.T, literal string) rune {
	t.Helper()
	value, _, tail, err := strconv.UnquoteChar(literal[1:len(literal)-1], '\'')
	if err != nil || tail != "" {
		t.Fatalf("parse Go rune literal %q: %v", literal, err)
	}
	return value
}
