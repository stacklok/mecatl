package sessnap

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceEnrollmentAuthority_Scenario2_ProvenancePresenceAndBounds(t *testing.T) {
	s, pending := enrollmentSnapshotSession(t)
	if err := s.AbortWorkspaceEnrollment(pending.ID); err != nil {
		t.Fatalf("AbortWorkspaceEnrollment: %v", err)
	}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker-old"}, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}
	encoded, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["workspace_enrollment_broker_keys_present"]) != "true" {
		t.Fatalf("presence JSON = %s, want true", fields["workspace_enrollment_broker_keys_present"])
	}
	restored, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	keys, present := restored.WorkspaceEnrollmentBrokerKeys()
	if !present || !reflect.DeepEqual(keys, []string{"broker-old"}) {
		t.Fatalf("restored ledger = %q, %t", keys, present)
	}

	base := `{"id":"legacy","state":"idle","mode":"default","limits":{},"counters":{},"environment_ref":{"Kind":"local","ID":".","Revision":"in-tree-v1"},"created_at":"1970-01-01T00:00:01Z","authority":{"capability_set":{"tools":["Read"]},"provenance":"test"}`
	for name, suffix := range map[string]string{
		"legacy absent":         `}`,
		"present null":          `,"workspace_enrollment_broker_keys":null,"workspace_enrollment_broker_keys_present":true}`,
		"keys without presence": `,"workspace_enrollment_broker_keys":["broker"]}`,
		"null presence":         `,"workspace_enrollment_broker_keys_present":null}`,
		"non-array keys":        `,"workspace_enrollment_broker_keys":"broker","workspace_enrollment_broker_keys_present":true}`,
		"duplicate keys":        `,"workspace_enrollment_broker_keys":["broker","broker"],"workspace_enrollment_broker_keys_present":true}`,
		"lone surrogate":        `,"workspace_enrollment_broker_keys":["\ud800"],"workspace_enrollment_broker_keys_present":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			restored, err := Unmarshal([]byte(base + suffix))
			if name == "legacy absent" || name == "present null" {
				if err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				keys, present := restored.WorkspaceEnrollmentBrokerKeys()
				wantPresent := name == "present null"
				if present != wantPresent || len(keys) != 0 {
					t.Fatalf("ledger = %q, %t; want empty, %t", keys, present, wantPresent)
				}
				return
			}
			if err == nil {
				t.Fatal("Unmarshal accepted invalid broker ledger JSON")
			}
		})
	}

	// Raw wire validation runs before JSON decoding can replace malformed bytes or
	// lone escapes. Exercise every presence state and every documented boundary.
	valid256 := make([]string, 256)
	for i := range valid256 {
		valid256[i] = fmt.Sprintf("k%03d", i)
	}
	tooMany := append(append([]string(nil), valid256...), "extra")
	tooLong := []string{strings.Repeat("x", 257)}
	// 128 distinct 256-byte keys are exactly 32 KiB; append one byte to exceed it.
	exactTotal := make([]string, 128)
	for i := range exactTotal {
		exactTotal[i] = strings.Repeat("x", 252) + fmt.Sprintf("%04d", i)
	}
	overTotal := append(append([]string(nil), exactTotal...), "z")
	encodeKeys := func(keys []string) string {
		encoded, err := json.Marshal(keys)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for _, tc := range []struct {
		name, suffix string
		wantPresent  bool
		wantKeys     []string
		valid        bool
	}{
		{"missing", `}`, false, nil, true},
		{"false omitted", `,"workspace_enrollment_broker_keys_present":false}`, false, nil, true},
		{"false empty", `,"workspace_enrollment_broker_keys":[],"workspace_enrollment_broker_keys_present":false}`, false, nil, true},
		{"false null", `,"workspace_enrollment_broker_keys":null,"workspace_enrollment_broker_keys_present":false}`, false, nil, true},
		{"true omitted", `,"workspace_enrollment_broker_keys_present":true}`, true, nil, true},
		{"true empty", `,"workspace_enrollment_broker_keys":[],"workspace_enrollment_broker_keys_present":true}`, true, nil, true},
		{"true null", `,"workspace_enrollment_broker_keys":null,"workspace_enrollment_broker_keys_present":true}`, true, nil, true},
		{"true keys", `,"workspace_enrollment_broker_keys":["broker"],"workspace_enrollment_broker_keys_present":true}`, true, []string{"broker"}, true},
		{"null presence", `,"workspace_enrollment_broker_keys_present":null}`, false, nil, false},
		{"nonboolean presence", `,"workspace_enrollment_broker_keys_present":"true"}`, false, nil, false},
		{"nonarray keys", `,"workspace_enrollment_broker_keys":"broker","workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"nonstring key", `,"workspace_enrollment_broker_keys":[1],"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"empty key", `,"workspace_enrollment_broker_keys":[""],"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"control key", `,"workspace_enrollment_broker_keys":["bad\u0000"],"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"raw invalid utf8", ",\"workspace_enrollment_broker_keys\":[\"\xff\"],\"workspace_enrollment_broker_keys_present\":true}", false, nil, false},
		{"256 keys", `,"workspace_enrollment_broker_keys":` + encodeKeys(valid256) + `,"workspace_enrollment_broker_keys_present":true}`, true, valid256, true},
		{"257 keys", `,"workspace_enrollment_broker_keys":` + encodeKeys(tooMany) + `,"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"256 bytes", `,"workspace_enrollment_broker_keys":` + encodeKeys([]string{strings.Repeat("x", 256)}) + `,"workspace_enrollment_broker_keys_present":true}`, true, []string{strings.Repeat("x", 256)}, true},
		{"257 bytes", `,"workspace_enrollment_broker_keys":` + encodeKeys(tooLong) + `,"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
		{"32 KiB", `,"workspace_enrollment_broker_keys":` + encodeKeys(exactTotal) + `,"workspace_enrollment_broker_keys_present":true}`, true, exactTotal, true},
		{"over 32 KiB", `,"workspace_enrollment_broker_keys":` + encodeKeys(overTotal) + `,"workspace_enrollment_broker_keys_present":true}`, false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restored, err := Unmarshal([]byte(base + tc.suffix))
			if !tc.valid {
				if err == nil {
					t.Fatal("Unmarshal accepted invalid raw ledger")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			keys, present := restored.WorkspaceEnrollmentBrokerKeys()
			if present != tc.wantPresent || !reflect.DeepEqual(keys, tc.wantKeys) {
				t.Fatalf("ledger = %q, %t; want %q, %t", keys, present, tc.wantKeys, tc.wantPresent)
			}
		})
	}
}
