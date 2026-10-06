package mcpbroker_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/google/cel-go/cel"
	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Adapted from the candidate contract matrix; Go outcome tests remain separate.
func TestSessionWireDescriptorFields(t *testing.T) {
	file := brokerv1.File_mecatl_broker_v1_session_proto
	table := map[string]string{
		"InspectConnectorsRequest":    "session_ref:1:string catalogue_ref:2:string",
		"InspectConnectorsResponse":   "availability:1:string enrollment_state:2:string connectors:3:message total_connectors:4:uint32 truncated:5:bool",
		"ConnectorStatus":             "name:1:string catalogue_state:2:string tool_count:3:uint32",
		"ToolDescriptor":              "name:1:string description:2:string schema:3:bytes read_only:4:bool dispatch_serial:5:bool authorization_capable:6:bool",
		"ToolResult":                  "call_id:1:string content:2:string is_error:3:bool parts:4:message",
		"ResultPart":                  "block_kind:1:string media_kind:2:string mime_type:3:string data:4:bytes url:5:string text:6:string name:7:string title:8:string description:9:string size:10:int64 audience:11:string priority:12:double last_modified:13:string",
		"OpenSessionRequest":          "saved_ref:1:string",
		"SessionSnapshot":             "ref:1:string expires_at:2:message catalogue:3:message",
		"Catalogue":                   "ref:1:string tools:2:message",
		"Call":                        "id:1:string name:2:string arguments:3:bytes",
		"InvokeToolRequest":           "session_ref:1:string catalogue_ref:2:string call:3:message",
		"CheckAuthorizationRequest":   "session_ref:1:string catalogue_ref:2:string call:3:message authorization_ref:4:string",
		"FlowRef":                     "ref:1:string expires_at:2:message",
		"InvocationOutcome":           "completed:1:message authorization_required:2:string not_dispatched:3:message outcome_unknown:4:message",
		"CheckAuthorizationResponse":  "ready:1:message authorization_required:2:message not_dispatched:3:message",
		"BeginAuthorizationRequest":   "session_ref:1:string authorization_ref:2:string",
		"ObserveAuthorizationRequest": "session_ref:1:string authorization_ref:2:string",
		"CancelAuthorizationRequest":  "session_ref:1:string authorization_ref:2:string",
		"BrowserPrompt":               "url:1:string expires_at:2:message",
		"FlowStatus":                  "pending:1:message completed:2:message cancelled:3:message expired:4:message failed:5:enum",
		"CancelOutcome":               "outcome:1:enum",
		"ResumeToolRequest":           "session_ref:1:string authorization_ref:2:string adopted_catalogue:3:string",
		"BeginEnrollmentRequest":      "session_ref:1:string",
		"EnrollmentStarted":           "ref:1:string prompt:2:message",
		"BeginEnrollmentResponse":     "started:1:message already_connected:2:message completed:3:message",
		"ObserveEnrollmentRequest":    "session_ref:1:string enrollment_ref:2:string",
		"CancelEnrollmentRequest":     "session_ref:1:string enrollment_ref:2:string",
		"DisconnectToolsRequest":      "session_ref:1:string expected_catalogue:2:string",
		"DisconnectOutcome":           "outcome:1:enum",
		"DeleteSessionRequest":        "session_ref:1:string",
		"DeleteOutcome":               "outcome:1:enum",
		"NonDispatch":                 "reason:1:enum",
	}
	if file.Messages().Len() != len(table) {
		t.Fatalf("message count = %d", file.Messages().Len())
	}
	for name, fields := range table {
		assertSessionWireFields(t, file.Messages().ByName(protoreflect.Name(name)), fields)
	}
	for _, binding := range []struct{ message, field, shared string }{{"Catalogue", "tools", "ToolDescriptor"}, {"InvocationOutcome", "completed", "ToolResult"}} {
		if file.Messages().ByName(protoreflect.Name(binding.message)).Fields().ByName(protoreflect.Name(binding.field)).Message() != file.Messages().ByName(protoreflect.Name(binding.shared)) {
			t.Fatal("shared descriptor not reused")
		}
	}
	for name, want := range map[string]string{
		"SessionSnapshot.expires_at": "google.protobuf.Timestamp", "SessionSnapshot.catalogue": "mecatl.broker.v1.Catalogue",
		"InvokeToolRequest.call": "mecatl.broker.v1.Call", "CheckAuthorizationRequest.call": "mecatl.broker.v1.Call",
		"FlowRef.expires_at": "google.protobuf.Timestamp", "BrowserPrompt.expires_at": "google.protobuf.Timestamp",
		"InvocationOutcome.not_dispatched": "mecatl.broker.v1.NonDispatch", "InvocationOutcome.outcome_unknown": "google.protobuf.Empty",
		"CheckAuthorizationResponse.ready": "google.protobuf.Empty", "CheckAuthorizationResponse.authorization_required": "mecatl.broker.v1.FlowRef", "CheckAuthorizationResponse.not_dispatched": "mecatl.broker.v1.NonDispatch",
		"FlowStatus.pending": "google.protobuf.Empty", "FlowStatus.completed": "mecatl.broker.v1.Catalogue", "FlowStatus.cancelled": "google.protobuf.Empty", "FlowStatus.expired": "google.protobuf.Empty",
		"EnrollmentStarted.prompt": "mecatl.broker.v1.BrowserPrompt", "BeginEnrollmentResponse.started": "mecatl.broker.v1.EnrollmentStarted", "BeginEnrollmentResponse.already_connected": "google.protobuf.Empty", "BeginEnrollmentResponse.completed": "mecatl.broker.v1.Catalogue",
	} {
		parts := strings.Split(name, ".")
		field := file.Messages().ByName(protoreflect.Name(parts[0])).Fields().ByName(protoreflect.Name(parts[1]))
		if string(field.Message().FullName()) != want {
			t.Fatalf("%s type = %s, want %s", name, field.Message().FullName(), want)
		}
	}
	if file.Messages().ByName("ToolResult").Fields().ByName("parts").Message() != file.Messages().ByName("ResultPart") {
		t.Fatal("shared result part type changed")
	}
	if !file.Messages().ByName("OpenSessionRequest").Fields().ByName("saved_ref").HasPresence() {
		t.Fatal("saved_ref lost optional presence")
	}
	for _, field := range []protoreflect.FieldDescriptor{file.Messages().ByName("Catalogue").Fields().ByName("tools"), file.Messages().ByName("ToolResult").Fields().ByName("parts"), file.Messages().ByName("ResultPart").Fields().ByName("audience")} {
		if !field.IsList() {
			t.Fatalf("%s not repeated", field.FullName())
		}
	}
	service := file.Services().ByName("SessionService")
	bindings := map[string]string{
		"InspectConnectors": "InspectConnectorsRequest:InspectConnectorsResponse",
		"OpenSession":       "OpenSessionRequest:SessionSnapshot", "InvokeTool": "InvokeToolRequest:InvocationOutcome", "CheckAuthorization": "CheckAuthorizationRequest:CheckAuthorizationResponse",
		"BeginAuthorization": "BeginAuthorizationRequest:BrowserPrompt", "ObserveAuthorization": "ObserveAuthorizationRequest:FlowStatus", "CancelAuthorization": "CancelAuthorizationRequest:CancelOutcome",
		"ResumeTool": "ResumeToolRequest:InvocationOutcome", "BeginEnrollment": "BeginEnrollmentRequest:BeginEnrollmentResponse", "ObserveEnrollment": "ObserveEnrollmentRequest:FlowStatus",
		"CancelEnrollment": "CancelEnrollmentRequest:CancelOutcome", "DisconnectTools": "DisconnectToolsRequest:DisconnectOutcome", "DeleteSession": "DeleteSessionRequest:DeleteOutcome",
	}
	if service.Methods().Len() != len(bindings) {
		t.Fatal("unexpected session operation count")
	}
	for name, want := range bindings {
		method := service.Methods().ByName(protoreflect.Name(name))
		if method == nil || string(method.Input().Name())+":"+string(method.Output().Name()) != want || method.IsStreamingClient() || method.IsStreamingServer() {
			t.Fatalf("unexpected %s binding", name)
		}
	}
}

func assertSessionWireFields(t *testing.T, message protoreflect.MessageDescriptor, want string) {
	t.Helper()
	if message == nil {
		t.Fatal("missing message")
	}
	var fields []string
	for i := 0; i < message.Fields().Len(); i++ {
		f := message.Fields().Get(i)
		fields = append(fields, fmt.Sprintf("%s:%d:%s", f.Name(), f.Number(), f.Kind()))
	}
	if strings.Join(fields, " ") != want {
		t.Fatalf("%s fields = %v, want %s", message.Name(), fields, want)
	}
}

func TestSessionWireUnionRoundTrip(t *testing.T) {
	file := brokerv1.File_mecatl_broker_v1_session_proto
	for name, arms := range map[string]int{"InvocationOutcome": 4, "CheckAuthorizationResponse": 3, "FlowStatus": 5, "BeginEnrollmentResponse": 3, "CheckAuthorizationRequest": 2} {
		message := file.Messages().ByName(protoreflect.Name(name))
		union := message.Oneofs().Get(0)
		rules := proto.GetExtension(union.Options(), validate.E_Oneof).(*validate.OneofRules)
		if union.Fields().Len() != arms || !rules.GetRequired() {
			t.Fatalf("%s union changed", name)
		}
		for i := 0; i < arms; i++ {
			field := union.Fields().Get(i)
			payload := dynamicpb.NewMessage(message)
			switch field.Kind() {
			case protoreflect.MessageKind:
				payload.Set(field, protoreflect.ValueOfMessage(dynamicpb.NewMessage(field.Message())))
			case protoreflect.StringKind:
				payload.Set(field, protoreflect.ValueOfString("value"))
			case protoreflect.EnumKind:
				payload.Set(field, protoreflect.ValueOfEnum(1))
			default:
				t.Fatalf("unexpected arm kind %s", field.Kind())
			}
			data, err := proto.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			restored := dynamicpb.NewMessage(message)
			if err := proto.Unmarshal(data, restored); err != nil || restored.WhichOneof(union) != field || !proto.Equal(payload, restored) {
				t.Fatalf("%s.%s roundtrip: %v", name, field.Name(), err)
			}
		}
	}
	for _, payload := range []proto.Message{
		&brokerv1.OpenSessionRequest{}, &brokerv1.OpenSessionRequest{SavedRef: proto.String("")},
		&brokerv1.Call{Id: "call", Name: "search", Arguments: []byte(" {\"query\":\"hello\"} \n")},
		&brokerv1.ToolDescriptor{Name: "search", Schema: []byte(" {\"type\":\"object\"} \n"), ReadOnly: true, DispatchSerial: true, AuthorizationCapable: true},
		&brokerv1.ToolResult{CallId: "call", Content: "result", IsError: true, Parts: []*brokerv1.ResultPart{{BlockKind: "embedded_resource", MimeType: "application/octet-stream", Data: []byte{0, 255, 1}, Url: "urn:resource", Name: "name", Title: "title", Description: "description", Size: 3, Audience: []string{"user", "assistant"}, Priority: 0.5, LastModified: "2026-10-05T00:00:00Z"}}},
	} {
		data, err := proto.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		restored := payload.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(data, restored); err != nil || !proto.Equal(payload, restored) {
			t.Fatalf("%T roundtrip: %v", payload, err)
		}
	}
}

func TestSessionWireValidationAnnotations(t *testing.T) {
	file := brokerv1.File_mecatl_broker_v1_session_proto
	for i := 0; i < file.Messages().Len(); i++ {
		message := file.Messages().Get(i)
		for j := 0; j < message.Fields().Len(); j++ {
			field := message.Fields().Get(j)
			rules, _ := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
			name := string(field.Name())
			if name == "ref" || strings.HasSuffix(name, "_ref") || name == "adopted_catalogue" || name == "expected_catalogue" || name == "authorization_required" && field.Kind() == protoreflect.StringKind {
				if rules.GetString().GetLen() != 43 || rules.GetString().GetPattern() != "^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$" {
					t.Fatalf("%s reference bounds missing", field.FullName())
				}
				pattern := regexp.MustCompile(rules.GetString().GetPattern())
				if !pattern.MatchString(strings.Repeat("A", 43)) || pattern.MatchString(strings.Repeat("A", 42)+"B") || pattern.MatchString("bad") {
					t.Fatal("reference pattern no longer canonical")
				}
			}
			if field.Kind() == protoreflect.EnumKind && (!rules.GetEnum().GetDefinedOnly() || len(rules.GetEnum().GetNotIn()) != 1 || rules.GetEnum().GetNotIn()[0] != 0) {
				t.Fatalf("%s not closed", field.FullName())
			}
			if field.Kind() == protoreflect.MessageKind && field.ContainingOneof() == nil && !field.IsList() && !rules.GetRequired() {
				t.Fatalf("%s no longer required", field.FullName())
			}
		}
	}
	for _, name := range []protoreflect.Name{"id", "name"} {
		rules := proto.GetExtension(file.Messages().ByName("Call").Fields().ByName(name).Options(), validate.E_Field).(*validate.FieldRules)
		if rules.GetString().GetMinBytes() != 1 || rules.GetString().GetMaxBytes() != 256 {
			t.Fatalf("Call.%s bounds", name)
		}
	}
	args := proto.GetExtension(file.Messages().ByName("Call").Fields().ByName("arguments").Options(), validate.E_Field).(*validate.FieldRules)
	if args.GetBytes().GetMinLen() != 1 || args.GetBytes().GetMaxLen() != 262144 {
		t.Fatal("argument bounds")
	}
	url := proto.GetExtension(file.Messages().ByName("BrowserPrompt").Fields().ByName("url").Options(), validate.E_Field).(*validate.FieldRules).GetString()
	if url.GetMinBytes() != 1 || url.GetMaxBytes() != 8192 || !url.GetUri() || url.GetPattern() != "^https://[^/@]+([/?#]|$)" {
		t.Fatal("browser URL constraints changed")
	}
	for name, values := range map[string][]string{
		"FailureReason":     {"FAILURE_REASON_UNSPECIFIED", "FAILURE_REASON_CATALOGUE_CHANGED", "FAILURE_REASON_AUTHORITY_WITHDRAWN", "FAILURE_REASON_CAPACITY", "FAILURE_REASON_INTERRUPTED", "FAILURE_REASON_AUTHORIZATION_FAILED", "FAILURE_REASON_CALL_CHANGED", "FAILURE_REASON_EXPIRED"},
		"CancelOutcome":     {"UNSPECIFIED", "CANCELLED", "ALREADY_RESOLVED"},
		"DisconnectOutcome": {"UNSPECIFIED", "DISCONNECTED", "ALREADY_DISCONNECTED", "CATALOGUE_CHANGED"},
		"DeleteOutcome":     {"UNSPECIFIED", "DELETED", "ALREADY_ABSENT"},
	} {
		enum := file.Enums().ByName("FailureReason")
		if name != "FailureReason" {
			enum = file.Messages().ByName(protoreflect.Name(name)).Enums().ByName("Value")
		}
		if enum.Values().Len() != len(values) {
			t.Fatalf("%s enum count", name)
		}
		for i, value := range values {
			if enum.Values().Get(i).Number() != protoreflect.EnumNumber(i) || string(enum.Values().Get(i).Name()) != value {
				t.Fatalf("%s enum tags", name)
			}
		}
	}
}

func TestSessionWireSharedBoundsCEL(t *testing.T) {
	for _, tc := range []struct {
		message, field string
		inputType      *cel.Type
		payload        proto.Message
		valid          bool
	}{
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: "x", Schema: []byte("{}")}, true},
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: "", Schema: []byte("{}")}, false},
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: strings.Repeat("é", 129), Schema: []byte("{}")}, false},
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: strings.Repeat("é", 128), Schema: make([]byte, 262144)}, true},
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: "x"}, false},
		{"Catalogue", "tools", cel.ListType(cel.ObjectType("mecatl.broker.v1.ToolDescriptor")), &brokerv1.ToolDescriptor{Name: "x", Schema: make([]byte, 262145)}, false},
		{"InvocationOutcome", "completed", cel.ObjectType("mecatl.broker.v1.ToolResult"), &brokerv1.ToolResult{CallId: "call"}, true},
		{"InvocationOutcome", "completed", cel.ObjectType("mecatl.broker.v1.ToolResult"), &brokerv1.ToolResult{}, false},
		{"InvocationOutcome", "completed", cel.ObjectType("mecatl.broker.v1.ToolResult"), &brokerv1.ToolResult{CallId: strings.Repeat("é", 128)}, true},
		{"InvocationOutcome", "completed", cel.ObjectType("mecatl.broker.v1.ToolResult"), &brokerv1.ToolResult{CallId: strings.Repeat("é", 129)}, false},
	} {
		field := brokerv1.File_mecatl_broker_v1_session_proto.Messages().ByName(protoreflect.Name(tc.message)).Fields().ByName(protoreflect.Name(tc.field))
		rules := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
		if len(rules.GetCel()) != 1 {
			t.Fatal("shared bounds CEL missing")
		}
		if tc.field == "tools" && rules.GetRepeated().GetMaxItems() != 1024 {
			t.Fatal("catalogue count bound missing")
		}
		env, err := cel.NewEnv(cel.Types(&brokerv1.ToolDescriptor{}, &brokerv1.ToolResult{}), cel.Variable("this", tc.inputType))
		if err != nil {
			t.Fatal(err)
		}
		ast, issues := env.Compile(rules.GetCel()[0].GetExpression())
		if issues.Err() != nil {
			t.Fatal(issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		var input any = tc.payload
		if tc.field == "tools" {
			input = []*brokerv1.ToolDescriptor{tc.payload.(*brokerv1.ToolDescriptor)}
		}
		result, _, err := program.Eval(map[string]any{"this": input})
		if err != nil || result.Value() != tc.valid {
			t.Fatalf("%s CEL result=%v err=%v want=%v", tc.message, result, err, tc.valid)
		}
	}
}
