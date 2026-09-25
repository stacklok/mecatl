package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

type sdkHTTPMapping struct {
	target string
	source string
}

type sdkHTTPReview struct {
	method          string
	pathTemplate    string
	pathParameters  []sdkHTTPMapping
	queryParameters []sdkHTTPMapping
	requestBody     string
	response        string
	bodyField       string
	responseField   string
}

type sdkServerHTTPRoute struct {
	method  string
	path    string
	handler string
}

func (r sdkServerHTTPRoute) key() string { return r.method + " " + r.path }

func TestSDKTypescriptRelease_Scenario2_HTTPRouteParity(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)
	reviews := parseSDKHTTPReviews(t, source, rows)
	routes, _ := parseSDKServerHTTP(t)
	byRoute := indexSDKServerHTTPRoutes(t, routes)

	httpCount := 0
	for _, row := range rows {
		switch row.httpFactory {
		case "http", "httpBinary":
			httpCount++
			review, ok := reviews[row.key]
			if !ok {
				t.Errorf("catalog row %q has no parseable HTTP review", row.key)
				continue
			}
			if _, ok := byRoute[review.method+" "+review.pathTemplate]; !ok {
				t.Errorf("catalog row %q has stale HTTP route %s %s", row.key, review.method, review.pathTemplate)
			}
			if row.httpFactory == "httpBinary" {
				if review.requestBody != "none" && review.requestBody != "binary" {
					t.Errorf("catalog row %q has unreviewed binary request-body class %q", row.key, review.requestBody)
				}
				if review.response != "json" && review.response != "binary" {
					t.Errorf("catalog row %q has unreviewed binary response class %q", row.key, review.response)
				}
			} else {
				if review.requestBody != "none" && review.requestBody != "json" && review.requestBody != "optional-json" {
					t.Errorf("catalog row %q has unreviewed request-body class %q", row.key, review.requestBody)
				}
				if review.response != "json" && review.response != "sse" {
					t.Errorf("catalog row %q has unreviewed response class %q", row.key, review.response)
				}
			}
			if review.method == "" || review.pathTemplate == "" {
				t.Errorf("catalog row %q has an incomplete method/path review", row.key)
			}
		case "grpcOnly", "routeFamily":
			if _, ok := reviews[row.key]; ok {
				t.Errorf("non-HTTP catalog row %q has a stale HTTP route review", row.key)
			}
		default:
			t.Errorf("catalog row %q has unknown HTTP classification %q", row.key, row.httpFactory)
		}
	}
	if httpCount != len(reviews) {
		t.Fatalf("HTTP review count = %d, parsed reviews = %d", httpCount, len(reviews))
	}
}

func TestSDKTypescriptRelease_Scenario2_HTTPCodecParity(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)
	reviews := parseSDKHTTPReviews(t, source, rows)
	routes, analysis := parseSDKServerHTTP(t)
	byRoute := indexSDKServerHTTPRoutes(t, routes)
	descriptors, _ := mecatlV1ServiceDescriptors(t)

	for _, row := range rows {
		if row.httpFactory != "http" && row.httpFactory != "httpBinary" {
			continue
		}
		review := reviews[row.key]
		route, ok := byRoute[review.method+" "+review.pathTemplate]
		if !ok {
			continue
		}
		method := descriptors[row.service][row.method]
		if method == nil {
			t.Errorf("catalog row %q has no generated request descriptor", row.key)
			continue
		}
		facts := analysis.handlerFacts(route.handler)
		assertSDKHTTPPathCodec(t, row.key, review, route, facts, method.Input())
		assertSDKHTTPQueryCodec(t, row.key, review, facts, method.Input())
		if row.httpFactory == "httpBinary" {
			if (review.requestBody == "binary") != facts.body {
				t.Errorf("catalog row %q binary request body = %q, handler body read = %t", row.key, review.requestBody, facts.body)
			}
			continue
		}
		assertSDKHTTPBodyCodec(t, row.key, review, facts, method.Input())

		wantResponse := "json"
		if facts.sse {
			wantResponse = "sse"
		}
		if review.response != wantResponse {
			t.Errorf("catalog row %q response = %q, handler %s derives %q", row.key, review.response, route.handler, wantResponse)
		}
		wantResponseField := expectedSDKHTTPResponseField(facts, method.Output())
		if review.responseField != wantResponseField {
			t.Errorf("catalog row %q responseField = %q, handler %s derives %q", row.key, review.responseField, route.handler, wantResponseField)
		}
	}

	controls := parseSDKHTTPControls(t, source)
	for name, control := range controls {
		route, ok := byRoute[control.method+" "+control.pathTemplate]
		if !ok {
			t.Errorf("HTTP-only control %q has stale route %s %s", name, control.method, control.pathTemplate)
			continue
		}
		facts := analysis.handlerFacts(route.handler)
		assertSDKStringSetsEqual(t, "HTTP-only control "+name+" path placeholders", placeholderSet(route.path), mappingTargetSet(control.pathParameters))
		if len(control.pathParameters) != 1 || control.pathParameters[0] != (sdkHTTPMapping{target: "id", source: "session_id"}) {
			t.Errorf("HTTP-only control %q path codec = %+v, want id=session_id", name, control.pathParameters)
		}
		wantBody := "none"
		if facts.body {
			wantBody = "json"
			if facts.optionalBody {
				wantBody = "optional-json"
			}
		}
		if control.requestBody != wantBody {
			t.Errorf("HTTP-only control %q body = %q, handler %s derives %q", name, control.requestBody, route.handler, wantBody)
		}
	}
}

func TestSDKTypescriptRelease_Scenario2_RouteToServiceInjectivity(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)
	reviews := parseSDKHTTPReviews(t, source, rows)
	routes, analysis := parseSDKServerHTTP(t)
	byRoute := indexSDKServerHTTPRoutes(t, routes)
	claimed := make(map[string]string, len(reviews))

	for _, row := range rows {
		if row.httpFactory != "http" && row.httpFactory != "httpBinary" {
			continue
		}
		review := reviews[row.key]
		routeKey := review.method + " " + review.pathTemplate
		if prior, duplicate := claimed[routeKey]; duplicate {
			t.Errorf("catalog rows %q and %q share HTTP route %s", prior, row.key, routeKey)
			continue
		}
		claimed[routeKey] = row.key
		route, ok := byRoute[routeKey]
		if !ok {
			continue
		}
		services := analysis.handlerFacts(route.handler).services
		if _, ok := services[row.backingService]; !ok {
			t.Errorf("catalog row %q declares Service.%s, but registered handler %s invokes %v", row.key, row.backingService, route.handler, sortedSDKSet(services))
		}
	}
}

func TestSDKTypescriptRelease_Scenario2_HTTPRoutePartition(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)
	reviews := parseSDKHTTPReviews(t, source, rows)
	controls := parseSDKHTTPControls(t, source)
	routes, _ := parseSDKServerHTTP(t)

	rpcRoutes := make(map[string]struct{}, len(reviews))
	for _, row := range rows {
		if row.httpFactory != "http" && row.httpFactory != "httpBinary" {
			continue
		}
		review := reviews[row.key]
		rpcRoutes[review.method+" "+review.pathTemplate] = struct{}{}
	}
	controlRoutes := make(map[string]struct{}, len(controls))
	for _, control := range controls {
		controlRoutes[control.method+" "+control.pathTemplate] = struct{}{}
	}
	overlap := sdkStringSetDifference(rpcRoutes, sdkStringSetSubtract(rpcRoutes, controlRoutes))
	assertSDKStringSetsEqual(t, "same-stream exact-run control overlaps", stringSet(
		"POST /v1/sessions/{id}/controls/cancel",
		"POST /v1/sessions/{id}/controls/cancel-steer",
		"POST /v1/sessions/{id}/controls/resolve-ask",
		"POST /v1/sessions/{id}/controls/steer",
	), stringSet(overlap...))
	registered := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		registered[route.key()] = struct{}{}
	}
	assertSDKStringSetsEqual(t, "server HTTP route/RPC-control partition", registered, unionSDKStringSets(rpcRoutes, controlRoutes))
}

func assertSDKHTTPPathCodec(t *testing.T, key string, review sdkHTTPReview, route sdkServerHTTPRoute, facts sdkHTTPHandlerFacts, input protoreflect.MessageDescriptor) {
	t.Helper()
	wantTargets := placeholderSet(route.path)
	assertSDKStringSetsEqual(t, key+" path placeholders", wantTargets, mappingTargetSet(review.pathParameters))
	for _, mapping := range review.pathParameters {
		wantSource := expectedSDKHTTPPathSource(t, key, mapping.target, facts, input)
		if mapping.source != wantSource {
			t.Errorf("catalog row %q path %q source = %q, want %q", key, mapping.target, mapping.source, wantSource)
		}
	}
	assertSDKStringSetsEqual(t, key+" handler path reads", facts.pathParameters, sdkStringSetSubtract(wantTargets, constantMappingTargetSet(review.pathParameters)))
}

func assertSDKHTTPQueryCodec(t *testing.T, key string, review sdkHTTPReview, facts sdkHTTPHandlerFacts, input protoreflect.MessageDescriptor) {
	t.Helper()
	assertSDKStringSetsEqual(t, key+" query parameters", facts.queryParameters, mappingTargetSet(review.queryParameters))
	for _, mapping := range review.queryParameters {
		wantSource := mapping.target
		if input.Fields().ByName(protoreflect.Name(wantSource)) == nil {
			if metadata := input.Fields().ByName("metadata"); metadata != nil && metadata.Message() != nil && metadata.Message().Fields().ByName(protoreflect.Name(wantSource)) != nil {
				wantSource = "metadata." + wantSource
			} else {
				candidate := mapping.target + "_version"
				if input.Fields().ByName(protoreflect.Name(candidate)) == nil {
					t.Errorf("catalog row %q query %q does not derive from its request descriptor", key, mapping.target)
					continue
				}
				wantSource = candidate
			}
		}
		if mapping.source != wantSource {
			t.Errorf("catalog row %q query %q source = %q, want %q", key, mapping.target, mapping.source, wantSource)
		}
	}
}

func assertSDKHTTPBodyCodec(t *testing.T, key string, review sdkHTTPReview, facts sdkHTTPHandlerFacts, input protoreflect.MessageDescriptor) {
	t.Helper()
	wantBodyField := ""
	if _, scheduleBody := facts.calls["decodeScheduleSpec"]; scheduleBody {
		wantBodyField = "spec"
	}
	if review.bodyField != wantBodyField {
		t.Errorf("catalog row %q bodyField = %q, handler derives %q", key, review.bodyField, wantBodyField)
	}

	consumed := make(map[string]struct{})
	for _, mapping := range append(append([]sdkHTTPMapping(nil), review.pathParameters...), review.queryParameters...) {
		if mapping.source == "" || strings.HasPrefix(mapping.source, "@") {
			continue
		}
		root := strings.Split(mapping.source, ".")[0]
		if input.Fields().ByName(protoreflect.Name(root)) == nil {
			t.Errorf("catalog row %q maps unknown request field %q", key, mapping.source)
			continue
		}
		consumed[root] = struct{}{}
	}
	remaining := make(map[string]struct{})
	for i := range input.Fields().Len() {
		name := string(input.Fields().Get(i).Name())
		if _, used := consumed[name]; !used || name == wantBodyField {
			remaining[name] = struct{}{}
		}
	}
	if facts.rejectsBody {
		if !facts.body {
			t.Errorf("catalog row %q rejects request bodies without inspecting the HTTP body", key)
		}
		if !sdkFieldsShareControlOneof(input, remaining) {
			t.Errorf("catalog row %q rejects a body, but remaining request fields %v are not one control oneof", key, sortedSDKSet(remaining))
		}
		if review.requestBody != "none" {
			t.Errorf("catalog row %q request body = %q, handler rejects all bodies", key, review.requestBody)
		}
		return
	}
	wantBody := "none"
	if len(remaining) != 0 {
		wantBody = "json"
		if facts.optionalBody {
			wantBody = "optional-json"
		}
		if !facts.body {
			t.Errorf("catalog row %q has body fields %v, but handler does not read a body", key, sortedSDKSet(remaining))
		}
	}
	if review.requestBody != wantBody {
		t.Errorf("catalog row %q request body = %q, handler/request derive %q (fields %v)", key, review.requestBody, wantBody, sortedSDKSet(remaining))
	}
}

func expectedSDKHTTPResponseField(facts sdkHTTPHandlerFacts, output protoreflect.MessageDescriptor) string {
	if _, rawSession := facts.calls["writeSession"]; rawSession {
		return "session"
	}
	if _, rawEvent := facts.calls["toProto"]; !facts.sse || !rawEvent || output.Fields().Len() != 1 {
		return ""
	}
	field := output.Fields().Get(0)
	if field.Name() == "event" && field.Message() != nil && field.Message().FullName() == "mecatl.v1.Event" {
		return "event"
	}
	return ""
}

func sdkFieldsShareControlOneof(input protoreflect.MessageDescriptor, fields map[string]struct{}) bool {
	if len(fields) == 0 {
		return true
	}
	var oneof protoreflect.FullName
	for name := range fields {
		field := input.Fields().ByName(protoreflect.Name(name))
		if field == nil || field.ContainingOneof() == nil || field.ContainingOneof().IsSynthetic() {
			return false
		}
		if oneof == "" {
			oneof = field.ContainingOneof().FullName()
			continue
		}
		if field.ContainingOneof().FullName() != oneof {
			return false
		}
	}
	return true
}

func expectedSDKHTTPPathSource(t *testing.T, key, target string, facts sdkHTTPHandlerFacts, input protoreflect.MessageDescriptor) string {
	t.Helper()
	if input.Fields().ByName(protoreflect.Name(target)) != nil {
		return target
	}
	if target == "name" {
		if spec := input.Fields().ByName("spec"); spec != nil && spec.Message() != nil && spec.Message().Fields().ByName("name") != nil {
			return "spec.name"
		}
		if input.Fields().ByName("schedule_name") != nil {
			return "schedule_name"
		}
		if _, read := facts.pathParameters[target]; !read {
			return "@unused"
		}
	}
	if target == "id" {
		if metadata := input.Fields().ByName("metadata"); metadata != nil && metadata.Message() != nil && metadata.Message().Fields().ByName("session_id") != nil {
			return "metadata.session_id"
		}
		for _, candidate := range []string{"session_id", "source_session_id", "team_id", "job_id", "fire_id"} {
			if input.Fields().ByName(protoreflect.Name(candidate)) != nil {
				return candidate
			}
		}
	}
	t.Errorf("catalog row %q path placeholder %q has no derivable request field", key, target)
	return ""
}

func parseSDKHTTPReviews(t *testing.T, source string, rows []sdkRPCCatalogRow) map[string]sdkHTTPReview {
	t.Helper()
	reviews := make(map[string]sdkHTTPReview)
	for _, row := range rows {
		if row.httpFactory != "http" && row.httpFactory != "httpBinary" {
			continue
		}
		rowStart := strings.Index(source, `key: "`+row.key+`"`)
		if rowStart < 0 {
			t.Fatalf("locate catalog row %q", row.key)
		}
		rowEnd := strings.Index(source[rowStart:], "\n  rpc({")
		if rowEnd < 0 {
			rowEnd = len(source) - rowStart
		}
		body := source[rowStart : rowStart+rowEnd]
		marker := regexp.MustCompile(`http:\s*http(?:Binary)?\(`).FindStringIndex(body)
		if marker == nil {
			t.Fatalf("locate HTTP review for catalog row %q", row.key)
		}
		open := rowStart + marker[1] - 1
		args := parseSDKTypeScriptCall(t, source, open)
		review := sdkHTTPReview{
			method:          sdkTSString(t, args[0]),
			pathTemplate:    sdkTSString(t, args[1]),
			pathParameters:  sdkTSMappings(t, args[2]),
			queryParameters: sdkTSMappings(t, args[3]),
			requestBody:     sdkTSString(t, args[4]),
			response:        sdkTSString(t, args[5]),
		}
		if len(args) > 6 {
			review.bodyField = sdkTSString(t, args[6])
		}
		if len(args) > 7 {
			review.responseField = sdkTSString(t, args[7])
		}
		reviews[row.key] = review
	}
	return reviews
}

func parseSDKHTTPControls(t *testing.T, source string) map[string]sdkHTTPReview {
	t.Helper()
	start := strings.Index(source, "export const HTTP_ONLY_CONTROLS = {")
	end := strings.Index(source[start:], "} as const;")
	if start < 0 || end < 0 {
		t.Fatal("locate TypeScript HTTP-only control inventory")
	}
	body := source[start : start+end]
	pattern := regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9]*):\s*http\(`)
	controls := make(map[string]sdkHTTPReview)
	for _, match := range pattern.FindAllStringSubmatchIndex(body, -1) {
		name := body[match[2]:match[3]]
		open := start + match[1] - 1
		args := parseSDKTypeScriptCall(t, source, open)
		controls[name] = sdkHTTPReview{
			method:          sdkTSString(t, args[0]),
			pathTemplate:    sdkTSString(t, args[1]),
			pathParameters:  sdkTSMappings(t, args[2]),
			queryParameters: sdkTSMappings(t, args[3]),
			requestBody:     sdkTSString(t, args[4]),
			response:        sdkTSString(t, args[5]),
		}
	}
	if len(controls) == 0 {
		t.Fatal("TypeScript HTTP-only control inventory has no parseable rows")
	}
	return controls
}

func parseSDKTypeScriptCall(t *testing.T, source string, open int) []string {
	t.Helper()
	if open < 0 || open >= len(source) || source[open] != '(' {
		t.Fatal("invalid TypeScript call boundary")
	}
	args := make([]string, 0, 8)
	start := open + 1
	depth := 1
	quote := byte(0)
	escaped := false
	for i := open + 1; i < len(source); i++ {
		char := source[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '\'', '"', '`':
			quote = char
		case '(', '[', '{':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if arg := strings.TrimSpace(source[start:i]); arg != "" {
					args = append(args, arg)
				}
				if len(args) < 6 {
					t.Fatalf("HTTP review has %d arguments, want at least 6", len(args))
				}
				return args
			}
		case ']', '}':
			depth--
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(source[start:i]))
				start = i + 1
			}
		}
	}
	t.Fatal("unterminated TypeScript HTTP review")
	return nil
}

func sdkTSString(t *testing.T, value string) string {
	t.Helper()
	decoded, err := strconv.Unquote(strings.TrimSpace(value))
	if err != nil {
		t.Fatalf("parse TypeScript string %q: %v", value, err)
	}
	return decoded
}

func sdkTSMappings(t *testing.T, source string) []sdkHTTPMapping {
	t.Helper()
	literals := regexp.MustCompile(`"(?:\\.|[^"\\])*"`).FindAllString(source, -1)
	mappings := make([]sdkHTTPMapping, 0, len(literals))
	for _, literal := range literals {
		value := sdkTSString(t, literal)
		target, mappingSource, found := strings.Cut(value, "=")
		if !found {
			mappingSource = target
		}
		mappings = append(mappings, sdkHTTPMapping{target: target, source: mappingSource})
	}
	return mappings
}

type sdkHTTPHandlerFacts struct {
	pathParameters  map[string]struct{}
	queryParameters map[string]struct{}
	services        map[string]struct{}
	calls           map[string]struct{}
	body            bool
	rejectsBody     bool
	optionalBody    bool
	sse             bool
}

type sdkHTTPSourceAnalysis struct {
	funcs map[string]*ast.FuncDecl
}

func parseSDKServerHTTP(t *testing.T) ([]sdkServerHTTPRoute, *sdkHTTPSourceAnalysis) {
	t.Helper()
	path := sdkServerHTTPSourcePath(t)
	fileset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*http.go"))
	if err != nil {
		t.Fatalf("find HTTP server sources: %v", err)
	}
	analysis := &sdkHTTPSourceAnalysis{funcs: make(map[string]*ast.FuncDecl)}
	var constructor *ast.FuncDecl
	for _, sourcePath := range paths {
		if strings.HasSuffix(sourcePath, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileset, sourcePath, nil, 0)
		if err != nil {
			t.Fatalf("parse HTTP server source %s: %v", sourcePath, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			analysis.funcs[function.Name.Name] = function
			if function.Name.Name == "NewHTTPHandler" {
				constructor = function
			}
		}
	}
	if constructor == nil {
		t.Fatal("HTTP server source has no NewHTTPHandler")
	}
	routes := make([]sdkServerHTTPRoute, 0, 80)
	ast.Inspect(constructor.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "HandleFunc" || len(value.Args) != 2 {
				return true
			}
			pattern, ok := sdkGoString(value.Args[0])
			if !ok {
				return true
			}
			handler, ok := sdkHandlerSelector(value.Args[1])
			if ok {
				routes = append(routes, splitSDKServerRoute(t, pattern, handler))
			}
		case *ast.CompositeLit:
			for _, element := range value.Elts {
				entry, ok := element.(*ast.CompositeLit)
				if !ok || len(entry.Elts) != 2 {
					continue
				}
				pattern, ok := sdkGoString(entry.Elts[0])
				if !ok || !regexp.MustCompile(`^(?:DELETE|GET|POST|PUT) /`).MatchString(pattern) {
					continue
				}
				handler, ok := sdkHandlerSelector(entry.Elts[1])
				if ok {
					routes = append(routes, splitSDKServerRoute(t, pattern, handler))
				}
			}
		}
		return true
	})
	if len(routes) == 0 {
		t.Fatal("HTTP server source has no parseable routes")
	}
	return routes, analysis
}

func (a *sdkHTTPSourceAnalysis) handlerFacts(handler string) sdkHTTPHandlerFacts {
	facts := sdkHTTPHandlerFacts{
		pathParameters:  make(map[string]struct{}),
		queryParameters: make(map[string]struct{}),
		services:        make(map[string]struct{}),
		calls:           make(map[string]struct{}),
	}
	visited := make(map[string]struct{})
	var visit func(string)
	visit = func(name string) {
		if _, seen := visited[name]; seen {
			return
		}
		visited[name] = struct{}{}
		function := a.funcs[name]
		if function == nil || function.Body == nil {
			return
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				if text, ok := sdkGoString(value); ok && text == "text/event-stream" {
					facts.sse = true
				}
			case *ast.SelectorExpr:
				if value.Sel.Name == "Body" {
					if ident, ok := value.X.(*ast.Ident); ok && ident.Name == "r" {
						facts.body = true
					}
				}
				if value.Sel.Name == "ContentLength" {
					facts.optionalBody = true
				}
				if service := sdkServiceSelector(value); service != "" {
					facts.services[service] = struct{}{}
				}
			case *ast.IndexExpr:
				if name, ok := sdkGoString(value.Index); ok && sdkIsQueryCall(value.X) {
					facts.queryParameters[name] = struct{}{}
				}
			case *ast.AssignStmt:
				if sdkDiscardedBodyDecode(value) {
					facts.optionalBody = true
				}
			case *ast.CallExpr:
				if name, ok := sdkStringCallArgument(value, "PathValue"); ok {
					facts.pathParameters[name] = struct{}{}
				}
				if name, ok := sdkStringCallArgument(value, "Get"); ok && !sdkIsHeaderGetCall(value) {
					facts.queryParameters[name] = struct{}{}
				}
				called := sdkLocalCallName(value.Fun)
				if called == "" {
					return true
				}
				facts.calls[called] = struct{}{}
				if called == "controlRequestBodyEmpty" {
					facts.rejectsBody = true
				}
				if called == "decodeOptionalStrictJSON" {
					facts.optionalBody = true
				}
				if target := a.funcs[called]; target != nil && sdkFunctionAcceptsRequest(target) {
					visit(called)
				}
			}
			return true
		})
	}
	visit(handler)
	if operation, ok := a.learnedSkillOperation(handler); ok {
		facts.services = operation
	}
	return facts
}

func (a *sdkHTTPSourceAnalysis) learnedSkillOperation(handler string) (map[string]struct{}, bool) {
	function := a.funcs[handler]
	if function == nil {
		return nil, false
	}
	operation := ""
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || sdkLocalCallName(call.Fun) != "learnedSkillMutation" || len(call.Args) < 3 {
			return true
		}
		operation, _ = sdkGoString(call.Args[2])
		return false
	})
	if operation == "" {
		return nil, false
	}
	services := make(map[string]struct{})
	helper := a.funcs["learnedSkillMutation"]
	ast.Inspect(helper.Body, func(node ast.Node) bool {
		switchStatement, ok := node.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, statement := range switchStatement.Body.List {
			clause := statement.(*ast.CaseClause)
			matches := false
			for _, expression := range clause.List {
				value, _ := sdkGoString(expression)
				matches = matches || value == operation
			}
			if !matches {
				continue
			}
			for _, bodyStatement := range clause.Body {
				ast.Inspect(bodyStatement, func(child ast.Node) bool {
					if selector, ok := child.(*ast.SelectorExpr); ok {
						if service := sdkServiceSelector(selector); service != "" {
							services[service] = struct{}{}
						}
					}
					return true
				})
			}
		}
		return false
	})
	return services, true
}

func sdkServerHTTPSourcePath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate HTTP server source")
	}
	return filepath.Join(filepath.Dir(filename), "http.go")
}

func indexSDKServerHTTPRoutes(t *testing.T, routes []sdkServerHTTPRoute) map[string]sdkServerHTTPRoute {
	t.Helper()
	indexed := make(map[string]sdkServerHTTPRoute, len(routes))
	for _, route := range routes {
		if prior, duplicate := indexed[route.key()]; duplicate {
			t.Fatalf("duplicate server HTTP registration %s (%s and %s)", route.key(), prior.handler, route.handler)
		}
		indexed[route.key()] = route
	}
	return indexed
}

func splitSDKServerRoute(t *testing.T, pattern, handler string) sdkServerHTTPRoute {
	t.Helper()
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || method == "" || path == "" {
		t.Fatalf("invalid server HTTP registration %q", pattern)
	}
	return sdkServerHTTPRoute{method: method, path: path, handler: handler}
}

func sdkHandlerSelector(expression ast.Expr) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := selector.X.(*ast.Ident)
	return selector.Sel.Name, ok && ident.Name == "h"
}

func sdkServiceSelector(selector *ast.SelectorExpr) string {
	service, ok := selector.X.(*ast.SelectorExpr)
	if !ok || service.Sel.Name != "svc" {
		return ""
	}
	receiver, ok := service.X.(*ast.Ident)
	if !ok || receiver.Name != "h" {
		return ""
	}
	return selector.Sel.Name
}

func sdkLocalCallName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		if receiver, ok := value.X.(*ast.Ident); ok && receiver.Name == "h" {
			return value.Sel.Name
		}
	}
	return ""
}

func sdkStringCallArgument(call *ast.CallExpr, name string) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name || len(call.Args) != 1 {
		return "", false
	}
	return sdkGoString(call.Args[0])
}

func sdkIsHeaderGetCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Get" {
		return false
	}
	receiver, ok := selector.X.(*ast.SelectorExpr)
	return ok && receiver.Sel.Name == "Header"
}

func sdkGoString(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func sdkIsQueryCall(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Query"
}

func sdkDiscardedBodyDecode(statement *ast.AssignStmt) bool {
	if len(statement.Lhs) != 1 {
		return false
	}
	ident, ok := statement.Lhs[0].(*ast.Ident)
	if !ok || ident.Name != "_" {
		return false
	}
	found := false
	for _, expression := range statement.Rhs {
		ast.Inspect(expression, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "Body" {
				found = true
			}
			return true
		})
	}
	return found
}

func sdkFunctionAcceptsRequest(function *ast.FuncDecl) bool {
	if function.Type.Params == nil {
		return false
	}
	for _, field := range function.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := star.X.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Request" {
			return true
		}
	}
	return false
}

func placeholderSet(path string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, match := range regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(path, -1) {
		set[match[1]] = struct{}{}
	}
	return set
}

func mappingTargetSet(mappings []sdkHTTPMapping) map[string]struct{} {
	set := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		set[mapping.target] = struct{}{}
	}
	return set
}

func constantMappingTargetSet(mappings []sdkHTTPMapping) map[string]struct{} {
	set := make(map[string]struct{})
	for _, mapping := range mappings {
		if strings.HasPrefix(mapping.source, "@") {
			set[mapping.target] = struct{}{}
		}
	}
	return set
}

func sortedSDKSet(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
