package server

import (
	"reflect"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestSDKRunControls_Scenario1_ProtobufContract(t *testing.T) {
	t.Parallel()

	file := mecatlv1.File_mecatl_v1_harness_proto
	service := file.Services().ByName("HarnessService")
	if service == nil {
		t.Fatal("HarnessService descriptor is missing")
	}

	methods := []struct {
		name   protoreflect.Name
		input  protoreflect.FullName
		output protoreflect.FullName
	}{
		{"ResolveRunAsk", "mecatl.v1.ResolveRunAskRequest", "mecatl.v1.ResolveRunAskResponse"},
		{"CancelRun", "mecatl.v1.CancelRunRequest", "mecatl.v1.CancelRunResponse"},
		{"SteerRun", "mecatl.v1.SteerRunRequest", "mecatl.v1.SteerRunResponse"},
		{"CancelRunSteer", "mecatl.v1.CancelRunSteerRequest", "mecatl.v1.CancelRunSteerResponse"},
	}
	for _, want := range methods {
		method := service.Methods().ByName(want.name)
		if method == nil {
			t.Errorf("HarnessService.%s is missing", want.name)
			continue
		}
		if method.Input().FullName() != want.input || method.Output().FullName() != want.output {
			t.Errorf("HarnessService.%s signature = %s -> %s, want %s -> %s", want.name, method.Input().FullName(), method.Output().FullName(), want.input, want.output)
		}
		if method.IsStreamingClient() || method.IsStreamingServer() {
			t.Errorf("HarnessService.%s is streaming, want unary", want.name)
		}
	}

	messages := file.Messages()
	assertRunControlMessageOrder(t, messages,
		"SteerCancel",
		"ResolveRunAskRequest", "ResolveRunAskResponse",
		"CancelRunRequest", "CancelRunResponse",
		"SteerRunRequest", "SteerRunResponse",
		"CancelRunSteerRequest", "CancelRunSteerResponse",
	)

	resolveRequest := requireRunControlMessage(t, messages, "ResolveRunAskRequest")
	assertRunControlFields(t, resolveRequest, map[protoreflect.Name]runControlField{
		"session_id":      {number: 1, kind: protoreflect.StringKind},
		"expected_run_id": {number: 2, kind: protoreflect.StringKind},
		"ask_id":          {number: 3, kind: protoreflect.StringKind},
		"verdict":         {number: 4, kind: protoreflect.EnumKind, typeName: "mecatl.v1.ApprovalVerdict"},
	})
	assertStringMinLen(t, resolveRequest, "session_id", 1)
	assertStringMinLen(t, resolveRequest, "expected_run_id", 1)
	assertStringMinLen(t, resolveRequest, "ask_id", 1)
	assertEnumIn(t, resolveRequest, "verdict", 1, 2, 3)

	assertRunControlFields(t, requireRunControlMessage(t, messages, "ResolveRunAskResponse"), map[protoreflect.Name]runControlField{
		"run_id": {number: 1, kind: protoreflect.StringKind},
		"ask_id": {number: 2, kind: protoreflect.StringKind},
	})

	cancelRequest := requireRunControlMessage(t, messages, "CancelRunRequest")
	assertRunControlFields(t, cancelRequest, map[protoreflect.Name]runControlField{
		"session_id":      {number: 1, kind: protoreflect.StringKind},
		"expected_run_id": {number: 2, kind: protoreflect.StringKind},
	})
	assertStringMinLen(t, cancelRequest, "session_id", 1)
	assertStringMinLen(t, cancelRequest, "expected_run_id", 1)
	assertRunControlFields(t, requireRunControlMessage(t, messages, "CancelRunResponse"), map[protoreflect.Name]runControlField{
		"run_id": {number: 1, kind: protoreflect.StringKind},
	})

	steerRequest := requireRunControlMessage(t, messages, "SteerRunRequest")
	assertRunControlFields(t, steerRequest, map[protoreflect.Name]runControlField{
		"session_id":      {number: 1, kind: protoreflect.StringKind},
		"expected_run_id": {number: 2, kind: protoreflect.StringKind},
		"text":            {number: 3, kind: protoreflect.StringKind},
		"parts":           {number: 4, kind: protoreflect.MessageKind, cardinality: protoreflect.Repeated, typeName: "mecatl.v1.Content"},
		"message_id":      {number: 5, kind: protoreflect.StringKind},
	})
	assertStringMinLen(t, steerRequest, "session_id", 1)
	assertStringMinLen(t, steerRequest, "expected_run_id", 1)
	assertRepeatedMaxItems(t, steerRequest, "parts", 16)
	assertStringMaxLen(t, steerRequest, "message_id", 64)
	assertMessageCEL(t, steerRequest, "steer_run.content", "this.text != '' || size(this.parts) > 0")
	assertSteerResponse(t, requireRunControlMessage(t, messages, "SteerRunResponse"))

	cancelSteerRequest := requireRunControlMessage(t, messages, "CancelRunSteerRequest")
	assertRunControlFields(t, cancelSteerRequest, map[protoreflect.Name]runControlField{
		"session_id":      {number: 1, kind: protoreflect.StringKind},
		"expected_run_id": {number: 2, kind: protoreflect.StringKind},
		"message_id":      {number: 3, kind: protoreflect.StringKind},
	})
	assertStringMinLen(t, cancelSteerRequest, "session_id", 1)
	assertStringMinLen(t, cancelSteerRequest, "expected_run_id", 1)
	assertStringMaxLen(t, cancelSteerRequest, "message_id", 64)
	assertSteerResponse(t, requireRunControlMessage(t, messages, "CancelRunSteerResponse"))
}

type runControlField struct {
	number      protoreflect.FieldNumber
	kind        protoreflect.Kind
	cardinality protoreflect.Cardinality
	typeName    protoreflect.FullName
}

func requireRunControlMessage(t *testing.T, messages protoreflect.MessageDescriptors, name protoreflect.Name) protoreflect.MessageDescriptor {
	t.Helper()
	message := messages.ByName(name)
	if message == nil {
		t.Fatalf("message %s is missing", name)
	}
	return message
}

func assertRunControlMessageOrder(t *testing.T, messages protoreflect.MessageDescriptors, names ...protoreflect.Name) {
	t.Helper()
	positions := make(map[protoreflect.Name]int, len(names))
	for i := 0; i < messages.Len(); i++ {
		positions[messages.Get(i).Name()] = i
	}
	for i, name := range names {
		position, ok := positions[name]
		if !ok {
			t.Errorf("message %s is missing", name)
			continue
		}
		if i > 0 && position != positions[names[i-1]]+1 {
			t.Errorf("message %s position = %d, want immediately after %s", name, position, names[i-1])
		}
	}
}

func assertRunControlFields(t *testing.T, message protoreflect.MessageDescriptor, want map[protoreflect.Name]runControlField) {
	t.Helper()
	fields := message.Fields()
	if fields.Len() != len(want) {
		t.Errorf("%s field count = %d, want %d", message.FullName(), fields.Len(), len(want))
	}
	for name, expected := range want {
		field := fields.ByName(name)
		if field == nil {
			t.Errorf("%s.%s is missing", message.FullName(), name)
			continue
		}
		if field.Number() != expected.number || field.Kind() != expected.kind {
			t.Errorf("%s.%s = field %d %s, want field %d %s", message.FullName(), name, field.Number(), field.Kind(), expected.number, expected.kind)
		}
		if expected.cardinality != 0 && field.Cardinality() != expected.cardinality {
			t.Errorf("%s.%s cardinality = %s, want %s", message.FullName(), name, field.Cardinality(), expected.cardinality)
		}
		if expected.typeName != "" && runControlFieldTypeName(field) != expected.typeName {
			t.Errorf("%s.%s type = %s, want %s", message.FullName(), name, runControlFieldTypeName(field), expected.typeName)
		}
	}
}

func runControlFieldTypeName(field protoreflect.FieldDescriptor) protoreflect.FullName {
	switch field.Kind() {
	case protoreflect.EnumKind:
		return field.Enum().FullName()
	case protoreflect.MessageKind:
		return field.Message().FullName()
	default:
		return ""
	}
}

func assertSteerResponse(t *testing.T, message protoreflect.MessageDescriptor) {
	t.Helper()
	assertRunControlFields(t, message, map[protoreflect.Name]runControlField{
		"outcome":    {number: 1, kind: protoreflect.EnumKind, typeName: "mecatl.v1.SteerOutcome"},
		"run_id":     {number: 2, kind: protoreflect.StringKind},
		"message_id": {number: 3, kind: protoreflect.StringKind},
	})
}

func runControlFieldRules(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name) *validate.FieldRules {
	t.Helper()
	field := message.Fields().ByName(name)
	if field == nil {
		t.Fatalf("%s.%s is missing", message.FullName(), name)
	}
	options, ok := field.Options().(*descriptorpb.FieldOptions)
	if !ok {
		t.Fatalf("%s.%s options type = %T", message.FullName(), name, field.Options())
	}
	rules, _ := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	if rules == nil {
		t.Fatalf("%s.%s has no buf.validate field rules", message.FullName(), name)
	}
	return rules
}

func assertStringMinLen(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name, want uint64) {
	t.Helper()
	rules := runControlFieldRules(t, message, name).GetString()
	if rules == nil || !rules.HasMinLen() || rules.GetMinLen() != want {
		t.Errorf("%s.%s min_len = %v, want %d", message.FullName(), name, rules, want)
	}
}

func assertStringMaxLen(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name, want uint64) {
	t.Helper()
	rules := runControlFieldRules(t, message, name).GetString()
	if rules == nil || !rules.HasMaxLen() || rules.GetMaxLen() != want {
		t.Errorf("%s.%s max_len = %v, want %d", message.FullName(), name, rules, want)
	}
}

func assertRepeatedMaxItems(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name, want uint64) {
	t.Helper()
	rules := runControlFieldRules(t, message, name).GetRepeated()
	if rules == nil || !rules.HasMaxItems() || rules.GetMaxItems() != want {
		t.Errorf("%s.%s max_items = %v, want %d", message.FullName(), name, rules, want)
	}
}

func assertEnumIn(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name, want ...int32) {
	t.Helper()
	rules := runControlFieldRules(t, message, name).GetEnum()
	if rules == nil || !reflect.DeepEqual(rules.GetIn(), want) {
		t.Errorf("%s.%s enum.in = %v, want %v", message.FullName(), name, rules.GetIn(), want)
	}
}

func assertMessageCEL(t *testing.T, message protoreflect.MessageDescriptor, wantID, wantExpression string) {
	t.Helper()
	options, ok := message.Options().(*descriptorpb.MessageOptions)
	if !ok {
		t.Fatalf("%s options type = %T", message.FullName(), message.Options())
	}
	rules, _ := proto.GetExtension(options, validate.E_Message).(*validate.MessageRules)
	if rules == nil || len(rules.GetCel()) != 1 {
		t.Fatalf("%s message CEL rules = %v, want exactly one", message.FullName(), rules)
	}
	rule := rules.GetCel()[0]
	if rule.GetId() != wantID || rule.GetExpression() != wantExpression {
		t.Errorf("%s message CEL = id %q expression %q, want id %q expression %q", message.FullName(), rule.GetId(), rule.GetExpression(), wantID, wantExpression)
	}
}
