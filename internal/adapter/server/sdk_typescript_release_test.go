package server

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
)

type sdkRPCCatalogRow struct {
	key               string
	service           string
	method            string
	shape             string
	backingService    string
	descriptorService string
	descriptorMethod  string
	httpFactory       string
}

var sdkRPCCatalogRowPattern = regexp.MustCompile(`(?s)rpc\(\{\s*` +
	`key:\s*"([^"]+)",\s*` +
	`service:\s*"([^"]+)",\s*` +
	`method:\s*"([^"]+)",\s*` +
	`shape:\s*"([^"]+)",\s*` +
	`backingService:\s*"([^"]+)",\s*` +
	`grpc:\s*grpc\(([A-Za-z0-9]+)\.method\.([A-Za-z0-9]+)\),\s*` +
	`http:\s*(http|grpcOnly|routeFamily)\(`)

func TestSDKTypescriptRelease_Scenario1_RPCTransportCatalogParity(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)

	targetServices := stringSet("HarnessService", "ScheduleService")
	excludedServices := stringSet("LocalSessionContextService")
	assertSDKRPCManifest(t, source, "MECATL_RPC_CATALOG_SERVICES", targetServices)
	assertSDKRPCManifest(t, source, "MECATL_RPC_CATALOG_EXCLUDED_SERVICES", excludedServices)

	descriptorsByService, allServices := mecatlV1ServiceDescriptors(t)
	if extra := sdkStringSetDifference(allServices, unionSDKStringSets(targetServices, excludedServices)); len(extra) != 0 {
		t.Fatalf("generated mecatl.v1 services lack a reviewed catalog/exclusion decision: %v", extra)
	}
	if missing := sdkStringSetDifference(unionSDKStringSets(targetServices, excludedServices), allServices); len(missing) != 0 {
		t.Fatalf("stale generated mecatl.v1 service catalog/exclusion decision: %v", missing)
	}

	wantCounts := map[string]int{"HarnessService": 76, "ScheduleService": 10}
	wantKeys := make(map[string]struct{}, 90)
	for service := range targetServices {
		methods := descriptorsByService[service]
		if len(methods) != wantCounts[service] {
			t.Fatalf("%s descriptor count = %d, want pinned %d", service, len(methods), wantCounts[service])
		}
		for method := range methods {
			key := service + "." + method
			wantKeys[key] = struct{}{}
		}
	}

	gotKeys := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, duplicate := gotKeys[row.key]; duplicate {
			t.Fatalf("duplicate TypeScript RPC catalog key %q", row.key)
		}
		gotKeys[row.key] = struct{}{}
		if row.key != row.service+"."+row.method {
			t.Errorf("catalog key %q does not match service/method %s.%s", row.key, row.service, row.method)
		}
		if _, ok := targetServices[row.service]; !ok {
			t.Errorf("catalog row %q uses non-catalog service %q", row.key, row.service)
		}
		if row.descriptorService != row.service {
			t.Errorf("catalog row %q dispatches through generated service %q", row.key, row.descriptorService)
		}
		if row.descriptorMethod != lowerFirstASCII(row.method) {
			t.Errorf("catalog row %q dispatches through generated method key %q", row.key, row.descriptorMethod)
		}
		if row.httpFactory != "http" && row.httpFactory != "grpcOnly" && row.httpFactory != "routeFamily" {
			t.Errorf("catalog row %q has no closed HTTP-side classification", row.key)
		}
	}
	assertSDKStringSetsEqual(t, "generated descriptors/TypeScript RPC catalog", wantKeys, gotKeys)
}

func TestADR_0304_ExactGRPCOnlySet(t *testing.T) {
	t.Parallel()

	source := sdkRPCCatalogSource(t)
	rows := parseSDKRPCCatalog(t, source)
	grpcOnly := make(map[string]struct{})
	routeFamily := make(map[string]struct{})
	for _, row := range rows {
		switch row.httpFactory {
		case "grpcOnly":
			grpcOnly[row.method] = struct{}{}
		case "routeFamily":
			routeFamily[row.method] = struct{}{}
		}
	}
	assertSDKStringSetsEqual(t, "ADR 0304 gRPC-only methods", stringSet("StreamSessionLive", "ListGuardrailCoverage", "GetGuardrailReviewDetail"), grpcOnly)
	assertSDKStringSetsEqual(t, "ADR 0304 route-family methods", stringSet("Converse"), routeFamily)
	assertSDKRPCManifest(t, source, "MECATL_RPC_ROUTE_FAMILIES", stringSet("Converse"))
	assertSDKRPCManifest(
		t,
		source,
		"MECATL_RPC_TRANSPORT_KINDS",
		stringSet("grpc", "http", "route-family"),
	)
	assertSDKRPCTypeUnion(
		t,
		source,
		"MECATL_RPC_TRANSPORT_CLASSIFICATIONS",
		stringSet(
			"GRPCTransportClassification",
			"HTTPTransportClassification",
			"RouteFamilyTransportClassification",
		),
	)
}

func TestADR_0304_PlanApprovalContractParity(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript plan contract")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "..", "sdk", "typescript", "src", "plan.ts")
	typescript := readParitySource(t, path)
	match := regexp.MustCompile(`(?s)// BEGIN MECATL_PLAN_APPROVED_PROCEED_TEXT\s+` +
		`export const PLAN_APPROVED_PROCEED_TEXT =\s*"([^"]+)";\s*` +
		`// END MECATL_PLAN_APPROVED_PROCEED_TEXT`).FindStringSubmatch(typescript)
	if len(match) != 2 {
		t.Fatal("TypeScript plan contract has no parseable proceed-message pin")
	}
	if match[1] != agent.PlanApprovedProceedText {
		t.Fatalf("Go/TypeScript plan proceed message drift: TypeScript=%q Go=%q", match[1], agent.PlanApprovedProceedText)
	}
	toolMatch := regexp.MustCompile(`(?m)^export const PLAN_APPROVAL_TOOL = "([^"]+)";$`).FindStringSubmatch(typescript)
	if len(toolMatch) != 2 {
		t.Fatal("TypeScript plan contract has no parseable plan-ask discriminator")
	}
	if want := agent.NewPresentPlanTool().Spec().Name; toolMatch[1] != want {
		t.Fatalf("Go/TypeScript plan-ask discriminator drift: TypeScript=%q Go=%q", toolMatch[1], want)
	}
}

func TestSDKTypescriptRelease_Scenario1_PublicServiceProjectionParity(t *testing.T) {
	t.Parallel()

	rows := parseSDKRPCCatalog(t, sdkRPCCatalogSource(t))
	serviceType := reflect.TypeOf((*Service)(nil))
	mapped := make(map[string]struct{}, len(rows))
	mappedAccess := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, duplicate := mapped[row.backingService]; duplicate {
			t.Fatalf("Service.%s is projected by more than one descriptor row", row.backingService)
		}
		mapped[row.backingService] = struct{}{}
		if row.backingService == "serverInfoResponse" {
			serverInfoSource, err := os.ReadFile(filepath.Join(filepath.Dir(sdkServerHTTPSourcePath(t)), "serverinfo.go"))
			if err != nil {
				t.Fatalf("read server-info projection source: %v", err)
			}
			if !regexp.MustCompile(`func \(s \*Service\) serverInfoResponse\(`).Match(serverInfoSource) {
				t.Errorf("catalog row %q names missing private *Service method %q", row.key, row.backingService)
			}
			continue
		}
		if _, ok := serviceType.MethodByName(row.backingService); !ok {
			t.Errorf("catalog row %q names missing exported *Service method %q", row.key, row.backingService)
		}
		entry, ok := serviceAccessTable[row.backingService]
		if !ok {
			t.Errorf("catalog row %q names unclassified *Service method %q", row.key, row.backingService)
			continue
		}
		mappedAccess[row.backingService] = struct{}{}
		if err := entry.validate(); err != nil {
			t.Errorf("catalog row %q names invalid classification for *Service.%s: %v", row.key, row.backingService, err)
		}
	}

	nonRPCServiceMethods := stringSet(
		"ActiveRuns",
		"Approve",
		"ApproveRun",
		// HTTP approve and gRPC Converse controls call these contextual successors to
		// ApproveRun inside the aggregate Converse catalog row.
		"ResolveApprovalRun",
		"ResolveScopedRunAsk",
		"BindPlacement",
		"CanProcessSchedule",
		"Cancel",
		"CancelChild",
		"CancelSteer",
		"ClientMCPFromWire",
		"Close",
		"CloseSession",
		"CreateACPSession",
		"CreateSession",
		"CreateSessionWithMCP",
		"CreateSessionWithProvider",
		"CreateTeamOnDefaultPlacement",
		"DeleteSessionForRetention",
		"DeleteSessionForRetentionCandidate",
		"Diagnostics",
		"Drain",
		"EmitScheduleEvent",
		"FinishRun",
		"GetUserModelDetail",
		"GracefulDrain",
		"HasScheduler",
		"IsDraining",
		"IsLive",
		"LeaseSweepDisabled",
		"ListModels", // models-only Go projection; wire handlers capture ListModelSnapshot
		"ListSessions",
		"LoadACPSession",
		"LoadSession",
		"LoadSessionWithMCP",
		"LookupRun",
		"LostOwnershipCandidates",
		"MaintenanceMutationAvailable",
		"ManualDreamCapabilities",
		"MaybeAutoApprovePlan",
		"OwnershipEnforced",
		"Persist",
		"ProviderCapabilities",
		"ProviderStatuses",
		"PublishSessionEvent",
		"ReattachPlacement",
		"ReattachPlacementInScope",
		"ReconcileLeaseLossTombstone",
		"RecoverNotice",
		"ResolvedModel",
		"RetryFailedRun",
		"ScheduleManager",
		"SessionCapabilities",
		"SessionStale",
		"SetModels",
		"SetModelsRefresher",
		"SetProviderStatus",
		"SetScheduleMinInterval",
		"SetScheduler",
		"SetSessionEnvironment",
		"SettleIfStale",
		"StaleRunningCandidates",
		"StartInteractiveRunContent",
		"StartRun",
		"StartScheduledRunContent",
		"Steer",
		"StorageReady",
		"WithAuthorizedSession",
	)
	classified := make(map[string]struct{}, len(serviceAccessTable))
	for name := range serviceAccessTable {
		classified[name] = struct{}{}
	}
	assertSDKStringSetsEqual(
		t,
		"classified non-RPC Service complement",
		nonRPCServiceMethods,
		sdkStringSetSubtract(classified, mappedAccess),
	)
	assertSDKStringSetsEqual(
		t,
		"classified RPC-reachable Service projection",
		mappedAccess,
		sdkStringSetSubtract(classified, nonRPCServiceMethods),
	)
}

func TestSDKTypescriptRelease_Scenario1_StreamingShapeParity(t *testing.T) {
	t.Parallel()

	rows := parseSDKRPCCatalog(t, sdkRPCCatalogSource(t))
	descriptorsByService, _ := mecatlV1ServiceDescriptors(t)
	for _, row := range rows {
		descriptor := descriptorsByService[row.service][row.method]
		if descriptor == nil {
			t.Errorf("catalog row %q has no generated Go descriptor", row.key)
			continue
		}
		want := sdkRPCStreamingShape(descriptor)
		if row.shape != want {
			t.Errorf("catalog row %q shape = %q, generated descriptor = %q", row.key, row.shape, want)
		}
	}

	wantSentinels := map[string]string{
		"HarnessService.ApprovePlan":         "server_streaming",
		"HarnessService.Converse":            "bidi_streaming",
		"HarnessService.RunTeam":             "server_streaming",
		"HarnessService.StreamSessionEvents": "server_streaming",
		"HarnessService.StreamSessionLive":   "server_streaming",
		"HarnessService.WatchSessionEvents":  "server_streaming",
	}
	byKey := make(map[string]sdkRPCCatalogRow, len(rows))
	for _, row := range rows {
		byKey[row.key] = row
	}
	for key, want := range wantSentinels {
		if got := byKey[key].shape; got != want {
			t.Errorf("streaming sentinel %s = %q, want %q", key, got, want)
		}
	}
}

func sdkRPCCatalogSource(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript RPC catalog")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "..", "sdk", "typescript", "src", "rpc-catalog.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read TypeScript RPC catalog: %v", err)
	}
	return string(data)
}

func parseSDKRPCCatalog(t *testing.T, source string) []sdkRPCCatalogRow {
	t.Helper()
	matches := sdkRPCCatalogRowPattern.FindAllStringSubmatch(source, -1)
	rows := make([]sdkRPCCatalogRow, 0, len(matches))
	for _, match := range matches {
		rows = append(rows, sdkRPCCatalogRow{
			key:               match[1],
			service:           match[2],
			method:            match[3],
			shape:             match[4],
			backingService:    match[5],
			descriptorService: match[6],
			descriptorMethod:  match[7],
			httpFactory:       match[8],
		})
	}
	if len(rows) == 0 {
		t.Fatal("TypeScript RPC catalog has no parseable rows")
	}
	return rows
}

func mecatlV1ServiceDescriptors(t *testing.T) (map[string]map[string]protoreflect.MethodDescriptor, map[string]struct{}) {
	t.Helper()
	services := make(map[string]map[string]protoreflect.MethodDescriptor)
	all := make(map[string]struct{})
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != "mecatl.v1" {
			return true
		}
		fileServices := file.Services()
		for i := 0; i < fileServices.Len(); i++ {
			service := fileServices.Get(i)
			name := string(service.Name())
			if _, duplicate := all[name]; duplicate {
				t.Fatalf("duplicate generated mecatl.v1 service %q", name)
			}
			all[name] = struct{}{}
			methods := make(map[string]protoreflect.MethodDescriptor, service.Methods().Len())
			for j := 0; j < service.Methods().Len(); j++ {
				method := service.Methods().Get(j)
				methodName := string(method.Name())
				if _, duplicate := methods[methodName]; duplicate {
					t.Fatalf("duplicate generated descriptor %s.%s", name, methodName)
				}
				methods[methodName] = method
			}
			services[name] = methods
		}
		return true
	})
	return services, all
}

func sdkRPCStreamingShape(method protoreflect.MethodDescriptor) string {
	switch {
	case method.IsStreamingClient() && method.IsStreamingServer():
		return "bidi_streaming"
	case method.IsStreamingServer():
		return "server_streaming"
	case method.IsStreamingClient():
		return "client_streaming"
	default:
		return "unary"
	}
}

func assertSDKRPCManifest(t *testing.T, source, name string, want map[string]struct{}) {
	t.Helper()
	blockPattern := regexp.MustCompile(`(?s)// BEGIN ` + regexp.QuoteMeta(name) + `\n(.*?)// END ` + regexp.QuoteMeta(name))
	match := blockPattern.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("TypeScript RPC catalog is missing %s manifest", name)
	}
	literals := regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_-]*)"`).FindAllStringSubmatch(match[1], -1)
	got := make(map[string]struct{}, len(literals))
	for _, literal := range literals {
		got[literal[1]] = struct{}{}
	}
	assertSDKStringSetsEqual(t, name, want, got)
}

func assertSDKRPCTypeUnion(t *testing.T, source, name string, want map[string]struct{}) {
	t.Helper()
	blockPattern := regexp.MustCompile(`(?s)// BEGIN ` + regexp.QuoteMeta(name) + `\n(.*?)// END ` + regexp.QuoteMeta(name))
	match := blockPattern.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("TypeScript RPC catalog is missing %s type-union guard", name)
	}
	members := regexp.MustCompile(`\|\s*([A-Za-z][A-Za-z0-9]+TransportClassification)`).FindAllStringSubmatch(match[1], -1)
	got := make(map[string]struct{}, len(members))
	for _, member := range members {
		got[member[1]] = struct{}{}
	}
	assertSDKStringSetsEqual(t, name, want, got)
}

func assertSDKStringSetsEqual(t *testing.T, label string, want, got map[string]struct{}) {
	t.Helper()
	missing := sdkStringSetDifference(want, got)
	extra := sdkStringSetDifference(got, want)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("%s drift: missing=%v extra=%v", label, missing, extra)
	}
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func unionSDKStringSets(sets ...map[string]struct{}) map[string]struct{} {
	union := make(map[string]struct{})
	for _, set := range sets {
		for value := range set {
			union[value] = struct{}{}
		}
	}
	return union
}

func sdkStringSetSubtract(left, right map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for value := range left {
		if _, found := right[value]; !found {
			result[value] = struct{}{}
		}
	}
	return result
}

func sdkStringSetDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for value := range left {
		if _, found := right[value]; !found {
			difference = append(difference, value)
		}
	}
	sort.Strings(difference)
	return difference
}

func lowerFirstASCII(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToLower(value[:1]) + value[1:]
}
