package permconfig

import (
	"reflect"
	"sort"
	"testing"
)

// TestStrictFieldsMatchYAMLTags pins the invariant the configgen drift guard relies on:
// for every *Section type, the keys of its strictFields() map (the REAL parse authority
// — an unknown key is a decodeStrictMapping error) are EXACTLY its yaml struct tags. So
// the configgen guard may derive its key inventory from the exported struct yaml tags
// and KNOW it equals the strict-decode key set (the QA cross-check, done here at the
// source rather than duplicated in configgen). A tag/parse-map divergence fails here.
func TestStrictFieldsMatchYAMLTags(t *testing.T) {
	cases := []struct {
		name   string
		strict map[string]any
		val    any
	}{
		{"Permissions", (&Permissions{}).strictFields(), Permissions{}},
		{"SubagentPermissions", (&SubagentPermissions{}).strictFields(), SubagentPermissions{}},
		{"GuardrailsSection", (&GuardrailsSection{}).strictFields(), GuardrailsSection{}},
		{"GuardrailRuleSpec", (&GuardrailRuleSpec{}).strictFields(), GuardrailRuleSpec{}},
		{"ModelsSection", (&ModelsSection{}).strictFields(), ModelsSection{}},
		{"RouterSection", (&RouterSection{}).strictFields(), RouterSection{}},
		{"RouterCategory", (&RouterCategory{}).strictFields(), RouterCategory{}},
		{"OpenRouterSection", (&OpenRouterSection{}).strictFields(), OpenRouterSection{}},
		{"OpenRouterModelRoute", (&OpenRouterModelRoute{}).strictFields(), OpenRouterModelRoute{}},
		{"MCPSection", (&MCPSection{}).strictFields(), MCPSection{}},
		{"MCPServerProfile", (&MCPServerProfile{}).strictFields(), MCPServerProfile{}},
		{"MCPAuthProfile", (&MCPAuthProfile{}).strictFields(), MCPAuthProfile{}},
		{"MCPStaticBearerProfile", (&MCPStaticBearerProfile{}).strictFields(), MCPStaticBearerProfile{}},
		{"MCPOAuthProfile", (&MCPOAuthProfile{}).strictFields(), MCPOAuthProfile{}},
		{"MCPOAuthClientProfile", (&MCPOAuthClientProfile{}).strictFields(), MCPOAuthClientProfile{}},
		{"MCPPreregisteredClientProfile", (&MCPPreregisteredClientProfile{}).strictFields(), MCPPreregisteredClientProfile{}},
		{"MCPCIMDClientProfile", (&MCPCIMDClientProfile{}).strictFields(), MCPCIMDClientProfile{}},
		{"MCPOAuthCredentialProfile", (&MCPOAuthCredentialProfile{}).strictFields(), MCPOAuthCredentialProfile{}},
		{"MCPLocalCredentialProfile", (&MCPLocalCredentialProfile{}).strictFields(), MCPLocalCredentialProfile{}},
		{"MCPEnvironmentCredentialProfile", (&MCPEnvironmentCredentialProfile{}).strictFields(), MCPEnvironmentCredentialProfile{}},
		{"MCPOAuthNetworkProfile", (&MCPOAuthNetworkProfile{}).strictFields(), MCPOAuthNetworkProfile{}},
	}
	for _, tc := range cases {
		strictKeys := mapKeys(tc.strict)
		tagKeys := yamlTagKeys(tc.val)
		if !reflect.DeepEqual(strictKeys, tagKeys) {
			t.Errorf("%s: strictFields keys %v != yaml struct tags %v — the parse authority diverged from the tags the configgen guard reflects over",
				tc.name, strictKeys, tagKeys)
		}
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func yamlTagKeys(v any) []string {
	t := reflect.TypeOf(v)
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, tag)
	}
	sort.Strings(keys)
	return keys
}
