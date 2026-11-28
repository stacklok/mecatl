package mcpbroker

import "testing"

// TestVMCPAuditRecordsNoContent pins the vMCP's audit to record no request
// or response content and no JSON-RPC error text, which ToolHive copies into
// the record when it detects application errors.
func TestVMCPAuditRecordsNoContent(t *testing.T) {
	c := vmcpAuditConfig(ToolHiveAudit{LogFile: "/var/log/mecatl/audit.jsonl"})
	if !c.Enabled || c.Component != "vmcp-server" || c.LogFile != "/var/log/mecatl/audit.jsonl" {
		t.Fatalf("audit config %+v", c)
	}
	if c.IncludeRequestData || c.IncludeResponseData || c.ShouldDetectApplicationErrors() {
		t.Fatalf("audit config records content: request %v, response %v, error text %v",
			c.IncludeRequestData, c.IncludeResponseData, c.ShouldDetectApplicationErrors())
	}
}
