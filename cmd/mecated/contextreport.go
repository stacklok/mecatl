package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/stacklok/mecatl/engine/session"
)

func readContextRow(path string, stdin io.Reader, row int) (contextDocument, error) {
	var d contextDocument
	r := stdin
	if path != "-" {
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G703 -- explicit input uses O_NOFOLLOW and descriptor validation.
		if err != nil {
			return d, errors.New("context: cannot open regular non-symlink input")
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > contextMaxInput {
			return d, errors.New("context: input must be a bounded regular file")
		}
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, contextMaxInput+1))
	if err != nil || len(data) > contextMaxInput {
		return d, errors.New("context: input exceeds limit or cannot be read")
	}
	if err := contextUniqueJSON(data); err != nil {
		return d, err
	}
	return readContextData(data, row)
}
func readContextData(data []byte, row int) (contextDocument, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return contextDocument{}, errors.New("context: expected JSON object")
	}
	if _, ok := fields["view"]; ok {
		return readContextProjection(data, row)
	}
	if _, ok := fields["Type"]; ok {
		return readContextEvent(fields, row)
	}
	if row != -1 {
		return contextDocument{}, errors.New("context: row index only applies to manifest projections")
	}
	return parseContextDocument(data, fields)
}

func readContextProjection(data []byte, row int) (contextDocument, error) {
	var p struct {
		View               string            `json:"view"`
		Source             string            `json:"source"`
		Available          bool              `json:"available"`
		Authoritative      bool              `json:"authoritative"`
		ProjectionComplete bool              `json:"projection_complete"`
		ScanComplete       bool              `json:"scan_complete"`
		RetentionComplete  bool              `json:"retention_complete"`
		Offset             int               `json:"offset"`
		Limit              int               `json:"limit"`
		NextOffset         *int              `json:"next_offset"`
		Error              string            `json:"error"`
		Rows               []json.RawMessage `json:"rows"`
	}
	if err := decodeContext(data, &p); err != nil {
		return contextDocument{}, err
	}
	if !validContextProjection(p.View, p.Available, p.Authoritative, p.Offset, p.Limit, p.NextOffset, len(p.Rows), row) {
		return contextDocument{}, errors.New("context: manifest projection requires an available row and explicit --row index")
	}
	encoded, err := contextProjectionRow(p.Rows[row])
	if err != nil {
		return contextDocument{}, err
	}
	d, err := readContextData(encoded, -1)
	if err != nil {
		return d, err
	}
	d.Scope, d.RowIndex = "imported projection row", contextPtr(p.Offset+row)
	d.Coverage = projectionCoverage(p.ProjectionComplete, p.ScanComplete, p.RetentionComplete, p.NextOffset != nil, p.Error != "")
	return d, nil
}

func validContextProjection(view string, available, authoritative bool, offset, limit int, next *int, rows, row int) bool {
	return view == "manifest" && available && authoritative && offset >= 0 && limit >= 1 && limit <= 50 && rows <= limit && row >= 0 && row < rows && offset <= int(^uint(0)>>1)-row && (next == nil || *next >= offset)
}

func contextProjectionRow(row json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(row, &fields); err != nil || fields == nil {
		return nil, errors.New("context: invalid projection row")
	}
	for from, to := range map[string]string{"tools": "tool_names", "decisions": "tool_decisions", "components": "prompt"} {
		value, ok := fields[from]
		if !ok || fields[to] != nil {
			return nil, errors.New("context: invalid projection row fields")
		}
		fields[to] = value
		delete(fields, from)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.New("context: invalid projection row")
	}
	return encoded, nil
}

func readContextEvent(fields map[string]json.RawMessage, row int) (contextDocument, error) {
	if err := validateContextEvent(fields); err != nil {
		return contextDocument{}, err
	}
	var typ string
	if err := json.Unmarshal(fields["Type"], &typ); err != nil || typ != "request.manifest" || row != -1 {
		return contextDocument{}, errors.New("context: expected single request.manifest event")
	}
	payload, ok := fields["RequestManifest"]
	if !ok || string(payload) == "null" {
		return contextDocument{}, errors.New("context: missing event manifest")
	}
	d, err := readContextData(payload, -1)
	if err != nil {
		return d, err
	}
	if d.Kind != contextKindReport || d.Scope != "single request" {
		return d, errors.New("context: event payload must be a bare manifest")
	}
	d.Scope = "imported single event"
	return d, nil
}
func validateContextEvent(fields map[string]json.RawMessage) error {
	for key, value := range fields {
		switch key {
		case "Type", "RequestManifest", "Actor": // Actor is ignored, never treated as proof of caller authority.
		case "Seq", "Turn":
			var n int64
			if json.Unmarshal(value, &n) != nil || n < 0 {
				return errors.New("context: invalid event metadata")
			}
		case "RunID", "Text":
			var s string
			if json.Unmarshal(value, &s) != nil || len(s) > 256 || key == "Text" && s != "" {
				return errors.New("context: invalid event metadata")
			}
		case "Title", "ToolCall", "ToolResult", "Ask", "ModelRetry", "NetworkAttempt", "Authorization", "PlanContinuationFailure", "Result", "TurnEnd", "Hook", "Approval", "CompactionArchive", "UserPrompt", "Subagent", "Team", "Parallel", "Schedule", "Steer":
			if string(bytes.TrimSpace(value)) != "null" {
				return errors.New("context: conflicting event payload")
			}
		default:
			return errors.New("context: unknown event field")
		}
	}
	return nil
}
func projectionCoverage(projected, scanned, retained, paged, failed bool) string {
	return fmt.Sprintf("imported projection flags (unverified): projection=%t scan=%t retention=%t paged=%t error=%t", projected, scanned, retained, paged, failed)
}
func contextValidProjectionCoverage(s string) bool {
	for n := 0; n < 32; n++ {
		if s == projectionCoverage(n&1 != 0, n&2 != 0, n&4 != 0, n&8 != 0, n&16 != 0) {
			return true
		}
	}
	return false
}
func parseContextDocument(data []byte, fields map[string]json.RawMessage) (contextDocument, error) {
	var d contextDocument
	var header struct {
		Version string `json:"version"`
		Kind    string `json:"kind"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return d, errors.New("context: invalid JSON")
	}
	if header.Version != "" || header.Kind != "" {
		return parseVersionedContextDocument(data)
	}
	return parseManifestContextDocument(data, fields)
}
func parseVersionedContextDocument(data []byte) (contextDocument, error) {
	var d contextDocument
	if err := decodeContext(data, &d); err != nil {
		return d, err
	}
	if d.Version != contextVersion || len(d.Occurrences) > contextMaxEntries || !validContextDocumentKind(d) || !validContextDocumentMetadata(d) || !validContextInventory(d) {
		return d, errors.New("context: unsupported document version, methodology, coverage, or metadata")
	}
	seen := map[string]bool{}
	for _, occurrence := range d.Occurrences {
		if err := validateOccurrence(d.Kind, occurrence); err != nil {
			return d, err
		}
		key := occurrence.Source + "/" + occurrence.Name
		if seen[key] {
			return d, errors.New("context: duplicate occurrence")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, candidate := range d.Candidates {
		if !contextLabel.MatchString(candidate.Name) {
			return d, errors.New("context: invalid inventory candidate name")
		}
		if err := validateScanOccurrence(candidate); err != nil {
			return d, err
		}
		key := candidate.Source + "/" + candidate.Name
		if seen[key] {
			return d, errors.New("context: duplicate inventory candidate")
		}
		seen[key] = true
	}
	if d.Kind == contextKindReport {
		sortContext(&d)
		sort.Slice(d.Candidates, func(i, j int) bool {
			a, b := d.Candidates[i], d.Candidates[j]
			return a.Source < b.Source || a.Source == b.Source && a.Name < b.Name
		})
	} else {
		sort.Slice(d.Occurrences, func(i, j int) bool {
			return d.Occurrences[i].Source < d.Occurrences[j].Source || d.Occurrences[i].Source == d.Occurrences[j].Source && d.Occurrences[i].Name < d.Occurrences[j].Name
		})
	}
	return d, nil
}

func validContextDocumentKind(d contextDocument) bool {
	switch d.Kind {
	case contextKindReport:
		return (d.Method == contextUnknown || d.Method == "local_estimate") && (d.Coverage == "request manifest" || contextValidProjectionCoverage(d.Coverage)) && (d.Scope == "single request" || d.Scope == "imported single event" || d.Scope == "imported projection row" || d.Scope == "")
	case contextKindScan:
		return d.Method == "o200k_base-local-estimate" && d.Coverage == "explicit scan candidates; trust, runtime admission, and activation unknown" && (d.Scope == "explicit project" || d.Scope == "explicit project + user root" || d.Scope == "explicit project + MCP snapshot" || d.Scope == "explicit project + user root + MCP snapshot" || d.Scope == "")
	default:
		return false
	}
}

func validContextDocumentMetadata(d contextDocument) bool {
	return (d.Model == "" || contextLabel.MatchString(d.Model)) && (d.ContextWindow == nil || *d.ContextWindow > 0) && (d.MessageCount == nil || *d.MessageCount >= 0) && (d.RowIndex == nil || *d.RowIndex >= 0 && d.Scope == "imported projection row") && (d.Scope != "imported projection row" || d.RowIndex != nil)
}

func validContextInventory(d contextDocument) bool {
	if d.Kind == contextKindScan {
		return d.Candidates == nil && d.InventoryMethod == "" && d.InventoryScope == "" && d.InventoryCoverage == ""
	}
	if d.InventoryMethod == "" || d.InventoryScope == "" || d.InventoryCoverage == "" {
		return d.Candidates == nil && d.InventoryMethod == "" && d.InventoryScope == "" && d.InventoryCoverage == ""
	}
	return len(d.Candidates) <= contextMaxEntries && validContextDocumentKind(contextDocument{Kind: contextKindScan, Method: d.InventoryMethod, Scope: d.InventoryScope, Coverage: d.InventoryCoverage})
}
func parseManifestContextDocument(data []byte, fields map[string]json.RawMessage) (contextDocument, error) {
	if fields["message_count"] == nil {
		return contextDocument{}, errors.New("context: missing manifest message_count")
	}
	if fields["prompt"] == nil {
		return contextDocument{}, errors.New("context: missing manifest prompt")
	}
	var manifest session.RequestManifestPayload
	if err := decodeContext(data, &manifest); err != nil {
		return contextDocument{}, err
	}
	if err := validateContextManifest(manifest); err != nil {
		return contextDocument{}, err
	}
	d := newContextManifestDocument(manifest)
	if err := addContextPromptOccurrences(&d, manifest); err != nil {
		return d, err
	}
	if err := addContextToolOccurrences(&d, manifest); err != nil {
		return d, err
	}
	if err := validateContextManifestNames(manifest); err != nil {
		return d, err
	}
	sortContext(&d)
	return d, nil
}

func validateContextManifest(m session.RequestManifestPayload) error {
	if m.MessageCount < 0 || m.MessageBytes < 0 || m.AdvertisedToolSchemaBytes < 0 || m.ContextWindow < 0 || len(m.Prompt)+len(m.AdvertisedTools)+len(m.ToolDecisions)+len(m.ToolNames) > contextMaxEntries {
		return errors.New("context: invalid manifest counts")
	}
	if m.TokenEstimateMethod != "" && m.TokenEstimateMethod != "local_estimate" {
		return errors.New("context: unsupported estimate method")
	}
	for _, n := range []*int{m.EstimatedRequestTokens, m.EstimatedSystemTokens, m.EstimatedEphemeralFragmentTokens, m.EstimatedPersistedHistoryTokens, m.EstimatedAdvertisedToolTokens, m.EstimatedSystemBytes, m.EstimatedEphemeralFragmentBytes, m.EstimatedPersistedHistoryBytes, m.EstimatedAdvertisedToolBytes} {
		if n != nil && *n < 0 {
			return errors.New("context: negative estimate")
		}
	}
	return nil
}

func newContextManifestDocument(m session.RequestManifestPayload) contextDocument {
	d := contextDocument{Version: contextVersion, Kind: contextKindReport, Method: contextUnknown, Coverage: "request manifest", Scope: "single request", MessageCount: contextPtr(m.MessageCount), Occurrences: []contextOccurrence{}}
	if m.Model != "" {
		d.Model = m.Model
		if !contextLabel.MatchString(m.Model) {
			d.Model = "model-000"
		}
	}
	if m.ContextWindow > 0 {
		d.ContextWindow = contextPtr(m.ContextWindow)
	}
	if m.ContextWindow > 0 && m.EstimatedRequestTokens != nil {
		d.Headroom = contextPtr(m.ContextWindow - *m.EstimatedRequestTokens)
	}
	if m.TokenEstimateMethod != "" {
		d.Method = m.TokenEstimateMethod
	}
	for _, metric := range []struct {
		source, name  string
		bytes, tokens *int
	}{{"total", contextRequestSource, nil, m.EstimatedRequestTokens}, {contextRequestSource, "messages", contextPtr(m.MessageBytes), nil}, {contextRequestSource, "system", m.EstimatedSystemBytes, m.EstimatedSystemTokens}, {contextRequestSource, "ephemeral", m.EstimatedEphemeralFragmentBytes, m.EstimatedEphemeralFragmentTokens}, {contextRequestSource, "history", m.EstimatedPersistedHistoryBytes, m.EstimatedPersistedHistoryTokens}, {contextRequestSource, "tools", m.EstimatedAdvertisedToolBytes, m.EstimatedAdvertisedToolTokens}} {
		d.Occurrences = append(d.Occurrences, contextOccurrence{Source: metric.source, Name: metric.name, Bytes: metric.bytes, EstimatedTokens: metric.tokens, Status: contextObservedRequest})
	}
	return d
}

func addContextPromptOccurrences(d *contextDocument, m session.RequestManifestPayload) error {
	reserved := contextPromptRuleNames(m)
	used := map[string]bool{}
	ordinal := 0
	for i, component := range m.Prompt {
		var err error
		ordinal, err = addContextPromptComponent(d, component, i, ordinal, reserved, used)
		if err != nil {
			return err
		}
	}
	return nil
}

func contextPromptRuleNames(m session.RequestManifestPayload) map[string]bool {
	reserved := map[string]bool{}
	for _, component := range m.Prompt {
		for _, rule := range component.Rules {
			if contextLabel.MatchString(rule.Name) {
				reserved[rule.Name] = true
			}
		}
	}
	return reserved
}

func addContextPromptComponent(d *contextDocument, component session.RequestPromptComponent, index, ordinal int, reserved, used map[string]bool) (int, error) {
	if component.Bytes < 0 || component.EstimatedTokens != nil && *component.EstimatedTokens < 0 || component.OmittedRules < 0 || !contextLabel.MatchString(component.Kind) || !contextLabel.MatchString(component.Provenance) {
		return ordinal, errors.New("context: invalid prompt component")
	}
	if (len(component.Rules) > 0 || component.OmittedRules > 0) && (component.Kind != session.RequestPromptInstruction || component.Provenance != session.RequestProvenanceRules) {
		return ordinal, errors.New("context: rules metadata outside rules fragment")
	}
	label := fmt.Sprintf("%03d-%s-%s", index, component.Kind, component.Provenance)
	if !contextLabel.MatchString(label) {
		return ordinal, errors.New("context: prompt component label too long")
	}
	d.Occurrences = append(d.Occurrences, contextOccurrence{Source: "fragment", Name: label, Bytes: contextPtr(component.Bytes), EstimatedTokens: component.EstimatedTokens, Status: contextObservedRequest})
	for _, rule := range component.Rules {
		if err := addContextRuleOccurrence(d, rule, ordinal, reserved, used); err != nil {
			return ordinal, err
		}
		ordinal++
	}
	if component.OmittedRules > 0 {
		d.Occurrences = append(d.Occurrences, contextOccurrence{Source: "rule-omitted", Name: fmt.Sprintf("fragment-%03d", index), Count: contextPtr(component.OmittedRules), Status: "observed omitted count; cost unknown"})
	}
	if len(d.Occurrences) > contextMaxEntries {
		return ordinal, errors.New("context: too many report occurrences")
	}
	return ordinal, nil
}

func addContextRuleOccurrence(d *contextDocument, rule session.RequestRuleMetric, ordinal int, reserved, used map[string]bool) error {
	if rule.RenderedBytes < 0 || rule.EstimatedTokens < 0 {
		return errors.New("context: invalid rule metric")
	}
	name := rule.Name
	if !contextLabel.MatchString(name) || name == contextUnknown || used[name] {
		name = fmt.Sprintf("rule-%03d", ordinal)
		for reserved[name] || used[name] {
			name += "x"
		}
	}
	used[name] = true
	source := "rule-unknown"
	if rule.Origin == "project" || rule.Origin == "user" {
		source = "rule-" + rule.Origin
	}
	d.Occurrences = append(d.Occurrences, contextOccurrence{Source: source, Name: name, Bytes: contextPtr(rule.RenderedBytes), EstimatedTokens: contextPtr(rule.EstimatedTokens), Status: "observed rendered rule; included in rules fragment"})
	return nil
}

func addContextToolOccurrences(d *contextDocument, m session.RequestManifestPayload) error {
	decisions := contextToolDecisions(m)
	reserved := contextToolNames(m)
	used, rawUsed := map[string]bool{}, map[string]bool{}
	for i, tool := range m.AdvertisedTools {
		if err := addContextToolOccurrence(d, tool, i, decisions, reserved, used, rawUsed); err != nil {
			return err
		}
	}
	return addContextDecisionOccurrences(d, m, rawUsed)
}

func contextToolDecisions(m session.RequestManifestPayload) map[string]session.RequestToolDecision {
	decisions := map[string]session.RequestToolDecision{}
	for _, decision := range m.ToolDecisions {
		previous, found := decisions[decision.Name]
		if !found || previous.Source != contextMCP && decision.Source == contextMCP {
			decisions[decision.Name] = decision
		}
	}
	return decisions
}

func contextToolNames(m session.RequestManifestPayload) map[string]bool {
	reserved := map[string]bool{}
	for _, metric := range m.AdvertisedTools {
		if contextLabel.MatchString(metric.Name) {
			reserved[metric.Name] = true
		}
	}
	return reserved
}

func addContextToolOccurrence(d *contextDocument, tool session.RequestToolMetric, index int, decisions map[string]session.RequestToolDecision, reserved, used, rawUsed map[string]bool) error {
	if err := validateContextToolMetric(tool.Name, tool.NameBytes, tool.DescriptionBytes, tool.SchemaBytes, tool.EstimatedTokens, tool.EstimatedNameTokens, tool.EstimatedDescriptionTokens, tool.EstimatedSchemaTokens, rawUsed); err != nil {
		return err
	}
	name := tool.Name
	if !contextLabel.MatchString(name) {
		name = fmt.Sprintf("tool-%03d", index)
		for reserved[name] || used[name] {
			name += "x"
		}
	}
	if used[name] {
		return errors.New("context: duplicate tool identifier")
	}
	used[name] = true
	decision, recorded := decisions[tool.Name]
	status := contextObservedRequest
	if recorded && decision.Decision == session.RequestToolDisclosureHidden {
		status = "observed lightweight spec; disclosure hidden"
	}
	provenance := contextToolProvenance(tool.Name, decision.Source, recorded)
	if recorded && decision.Decision != session.RequestToolAdvertised && decision.Decision != session.RequestToolDisclosureHidden {
		provenance = contextUnknown
	}
	o := contextOccurrence{Source: "tool", Name: name, Bytes: contextPtr(tool.NameBytes + tool.DescriptionBytes + tool.SchemaBytes), DocBytes: contextPtr(tool.DescriptionBytes), SchemaBytes: contextPtr(tool.SchemaBytes), EstimatedTokens: contextPtr(tool.EstimatedTokens), EstimatedDocTokens: contextPtr(tool.EstimatedDescriptionTokens), EstimatedSchemaTokens: contextPtr(tool.EstimatedSchemaTokens), Status: status, Provenance: provenance}
	if provenance == contextMCP || provenance == contextMCPInferred {
		o.MCPServer = contextMCPServer(tool.Name)
	}
	d.Occurrences = append(d.Occurrences, o)
	if len(d.Occurrences) > contextMaxEntries {
		return errors.New("context: too many report occurrences")
	}
	return nil
}
func contextToolProvenance(name, source string, recorded bool) string {
	if recorded {
		switch source {
		case contextMCP:
			// The request manifest currently classifies MCP by its name prefix,
			// not by the server registration that supplied the tool.
			return contextMCPInferred
		case "catalog", "overlay":
			return source
		default:
			return contextUnknown
		}
	}
	if strings.HasPrefix(name, "mcp__") {
		return contextMCPInferred
	}
	return contextUnknown
}
func contextMCPServer(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return ""
	}
	server, tool, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
	if !ok || !contextLabel.MatchString(server) || !contextLabel.MatchString(tool) {
		return ""
	}
	return server
}
func addContextDecisionOccurrences(d *contextDocument, m session.RequestManifestPayload, advertised map[string]bool) error {
	reserved, used := map[string]bool{}, map[string]bool{}
	for _, decision := range m.ToolDecisions {
		if contextLabel.MatchString(decision.Name) {
			reserved[decision.Name] = true
		}
	}
	for i, decision := range m.ToolDecisions {
		if decision.Decision == session.RequestToolAdvertised {
			continue
		}
		if decision.Decision == session.RequestToolDisclosureHidden && advertised[decision.Name] {
			continue
		}
		status := ""
		source := "tool-candidate"
		switch decision.Decision {
		case session.RequestToolModeFiltered, session.RequestToolAuthorityFiltered, session.RequestToolMountUnavailable, session.RequestToolShadowed:
			status = "decision " + decision.Decision + "; cost unknown"
		case session.RequestToolDisclosureHidden:
			// Older manifests have no per-tool measurements, but the lightweight
			// specification was still advertised to the model.
			source = "tool"
			status = "observed lightweight spec; cost unknown"
		default:
			return errors.New("context: unsupported tool decision")
		}
		name := decision.Name
		if !contextLabel.MatchString(name) || used[name] {
			name = fmt.Sprintf("candidate-%03d", i)
			for reserved[name] || used[name] {
				name += "x"
			}
		}
		used[name] = true
		provenance := contextToolProvenance(decision.Name, decision.Source, decision.Source != "")
		o := contextOccurrence{Source: source, Name: name, Status: status, Provenance: provenance}
		if provenance == contextMCP || provenance == contextMCPInferred {
			o.MCPServer = contextMCPServer(decision.Name)
		}
		d.Occurrences = append(d.Occurrences, o)
		if len(d.Occurrences) > contextMaxEntries {
			return errors.New("context: too many report occurrences")
		}
	}
	return nil
}

func validateContextToolMetric(name string, nameBytes, descriptionBytes, schemaBytes, tokens, nameTokens, descriptionTokens, schemaTokens int, rawUsed map[string]bool) error {
	if nameBytes < 0 || descriptionBytes < 0 || schemaBytes < 0 || tokens < 0 || nameTokens < 0 || descriptionTokens < 0 || schemaTokens < 0 {
		return errors.New("context: invalid tool metric")
	}
	if nameBytes > int(^uint(0)>>1)-descriptionBytes || nameBytes+descriptionBytes > int(^uint(0)>>1)-schemaBytes {
		return errors.New("context: tool metric overflow")
	}
	if name == "" || rawUsed[name] {
		return errors.New("context: empty or duplicate tool name")
	}
	rawUsed[name] = true
	return nil
}

func validateContextManifestNames(m session.RequestManifestPayload) error {
	for _, name := range m.ToolNames {
		if name == "" {
			return errors.New("context: empty tool name")
		}
	}
	for _, decision := range m.ToolDecisions {
		if decision.Name == "" || !contextLabel.MatchString(decision.Source) || !contextLabel.MatchString(decision.Decision) {
			return errors.New("context: invalid tool decision")
		}
	}
	return nil
}

func decodeContext(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("context: malformed or unsupported input")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("context: multiple JSON values")
	}
	return nil
}
func validateOccurrence(kind string, occurrence contextOccurrence) error {
	if !contextLabel.MatchString(occurrence.Name) {
		return errors.New("context: invalid occurrence name")
	}
	if kind == contextKindScan {
		return validateScanOccurrence(occurrence)
	}
	return validateReportOccurrence(occurrence)
}

func validateScanOccurrence(occurrence contextOccurrence) error {
	if !validScanSource(occurrence.Source) || !validScanStatus(occurrence.Status) || occurrence.Count != nil || occurrence.Provenance != "" || occurrence.MCPServer != "" {
		return errors.New("context: invalid scan source or status")
	}
	if occurrence.Source == "mcp-snapshot" {
		return validateMCPSnapshotOccurrence(occurrence)
	}
	if occurrence.FileBytes == nil || occurrence.DocBytes != nil || occurrence.SchemaBytes != nil || occurrence.EstimatedDocTokens != nil || occurrence.EstimatedSchemaTokens != nil || *occurrence.FileBytes < 0 || *occurrence.FileBytes > contextMaxFile || !validOptionalScanMetric(occurrence.Bytes) || !validOptionalTokenMetric(occurrence.EstimatedTokens) {
		return errors.New("context: invalid scan metrics")
	}
	return nil
}

func validScanSource(source string) bool {
	for _, candidate := range []string{"project-instructions", ".mecatl/rules", ".claude/rules", ".mecatl/agents", ".claude/agents", ".mecatl/skills", ".claude/skills", "user:.config/mecatl/rules", "user:.claude/rules", "user:.config/mecatl/agents", "user:.claude/agents", "user:.config/mecatl/skills", "user:.claude/skills", "user:.config/mecatl", "mcp-snapshot"} {
		if source == candidate {
			return true
		}
	}
	return false
}

func validScanStatus(status string) bool {
	for _, candidate := range []string{"selected candidate; trust unknown", "shadowed by AGENTS.md", "eligible eager rule; trust unknown", "omitted by eager rules cap", "invalid rule frontmatter", "snapshot candidate; runtime admission unknown", "metadata-only estimate; runtime admission unknown", "invalid metadata frontmatter", "shadowed by higher-precedence project rule", "candidate; selection unknown"} {
		if status == candidate {
			return true
		}
	}
	return false
}

func validateMCPSnapshotOccurrence(occurrence contextOccurrence) error {
	if occurrence.FileBytes != nil || occurrence.Bytes == nil || occurrence.DocBytes == nil || occurrence.SchemaBytes == nil || occurrence.EstimatedTokens == nil || occurrence.EstimatedDocTokens == nil || occurrence.EstimatedSchemaTokens == nil || *occurrence.Bytes < 0 || *occurrence.DocBytes < 0 || *occurrence.SchemaBytes < 0 || *occurrence.EstimatedTokens < 0 || *occurrence.EstimatedDocTokens < 0 || *occurrence.EstimatedSchemaTokens < 0 {
		return errors.New("context: invalid MCP snapshot metrics")
	}
	return nil
}

func validOptionalScanMetric(metric *int) bool {
	return metric == nil || *metric >= 0 && *metric <= contextMaxFile
}
func validOptionalTokenMetric(metric *int) bool { return metric == nil || *metric >= 0 }

func validateReportOccurrence(occurrence contextOccurrence) error {
	if !contextLabel.MatchString(occurrence.Name) || occurrence.FileBytes != nil {
		return errors.New("context: invalid report occurrence")
	}
	if err := validateReportMetrics(occurrence); err != nil {
		return err
	}
	if err := validateReportSource(occurrence); err != nil {
		return err
	}
	return validateReportProvenance(occurrence)
}

func validateReportMetrics(occurrence contextOccurrence) error {
	for _, metric := range []*int{occurrence.Bytes, occurrence.EstimatedTokens, occurrence.DocBytes, occurrence.SchemaBytes, occurrence.EstimatedDocTokens, occurrence.EstimatedSchemaTokens} {
		if metric != nil && *metric < 0 {
			return errors.New("context: negative report metric")
		}
	}
	if occurrence.Count != nil && *occurrence.Count <= 0 {
		return errors.New("context: invalid omitted count")
	}
	return nil
}

func validateReportSource(occurrence contextOccurrence) error {
	switch occurrence.Source {
	case "total", contextRequestSource, "fragment":
		return validateRequestOccurrence(occurrence)
	case "rule-project", "rule-user", "rule-unknown":
		return validateRuleOccurrence(occurrence)
	case "rule-omitted":
		return validateOmittedRuleOccurrence(occurrence)
	case "tool":
		return validateObservedToolOccurrence(occurrence)
	case "tool-candidate":
		return validateToolCandidateOccurrence(occurrence)
	default:
		return errors.New("context: unsupported report source")
	}
}

func validateRequestOccurrence(occurrence contextOccurrence) error {
	if occurrence.Status != contextObservedRequest || occurrence.Count != nil || occurrence.Provenance != "" || occurrence.MCPServer != "" {
		return errors.New("context: invalid request occurrence")
	}
	return nil
}

func validateRuleOccurrence(occurrence contextOccurrence) error {
	if occurrence.Status != "observed rendered rule; included in rules fragment" || occurrence.Bytes == nil || occurrence.EstimatedTokens == nil || occurrence.Count != nil || occurrence.Provenance != "" || occurrence.MCPServer != "" || occurrence.DocBytes != nil || occurrence.SchemaBytes != nil || occurrence.EstimatedDocTokens != nil || occurrence.EstimatedSchemaTokens != nil {
		return errors.New("context: invalid rule occurrence")
	}
	return nil
}

func validateOmittedRuleOccurrence(occurrence contextOccurrence) error {
	if occurrence.Status != "observed omitted count; cost unknown" || occurrence.Count == nil || occurrence.Bytes != nil || occurrence.EstimatedTokens != nil || occurrence.DocBytes != nil || occurrence.SchemaBytes != nil || occurrence.EstimatedDocTokens != nil || occurrence.EstimatedSchemaTokens != nil || occurrence.Provenance != "" || occurrence.MCPServer != "" {
		return errors.New("context: invalid omitted rule occurrence")
	}
	return nil
}

func validateObservedToolOccurrence(occurrence contextOccurrence) error {
	if !validObservedToolStatus(occurrence.Status) || occurrence.Count != nil {
		return errors.New("context: invalid observed tool")
	}
	return nil
}

func validObservedToolStatus(status string) bool {
	return status == contextObservedRequest || status == "observed lightweight spec; disclosure hidden" || status == "observed lightweight spec; cost unknown"
}

func validateToolCandidateOccurrence(occurrence contextOccurrence) error {
	if !validToolCandidateStatus(occurrence.Status) || occurrence.Count != nil || occurrence.Bytes != nil || occurrence.EstimatedTokens != nil || occurrence.DocBytes != nil || occurrence.SchemaBytes != nil || occurrence.EstimatedDocTokens != nil || occurrence.EstimatedSchemaTokens != nil {
		return errors.New("context: invalid tool candidate")
	}
	return nil
}

func validToolCandidateStatus(status string) bool {
	for _, candidate := range []string{session.RequestToolModeFiltered, session.RequestToolAuthorityFiltered, session.RequestToolMountUnavailable, session.RequestToolDisclosureHidden, session.RequestToolShadowed} {
		if status == "decision "+candidate+"; cost unknown" {
			return true
		}
	}
	return false
}

func validateReportProvenance(occurrence contextOccurrence) error {
	if occurrence.Source != "tool" && occurrence.Source != "tool-candidate" {
		if occurrence.Provenance != "" || occurrence.MCPServer != "" {
			return errors.New("context: invalid report provenance")
		}
		return nil
	}
	if !validToolProvenance(occurrence.Provenance) {
		return errors.New("context: invalid tool provenance")
	}
	if occurrence.MCPServer != "" && (!contextLabel.MatchString(occurrence.MCPServer) || occurrence.Provenance != contextMCP && occurrence.Provenance != contextMCPInferred) {
		return errors.New("context: invalid MCP group")
	}
	return nil
}

func validToolProvenance(provenance string) bool {
	switch provenance {
	case "", contextUnknown, "catalog", "overlay", contextMCP, contextMCPInferred:
		return true
	default:
		return false
	}
}
func sortContext(d *contextDocument) {
	sort.SliceStable(d.Occurrences, func(i, j int) bool {
		a, b := d.Occurrences[i], d.Occurrences[j]
		if a.EstimatedTokens != nil && b.EstimatedTokens != nil && *a.EstimatedTokens != *b.EstimatedTokens {
			return *a.EstimatedTokens > *b.EstimatedTokens
		}
		if (a.EstimatedTokens == nil) != (b.EstimatedTokens == nil) {
			return a.EstimatedTokens != nil
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Name < b.Name
	})
}
func diffContext(a, b contextDocument) (contextDiff, error) {
	d := contextDiff{Version: contextVersion, Kind: a.Kind, Method: a.Method, Coverage: a.Coverage, Model: a.Model, ContextWindow: a.ContextWindow, BeforeMessageCount: a.MessageCount, AfterMessageCount: b.MessageCount, Changes: []contextChange{}, InventoryMethod: a.InventoryMethod, InventoryScope: a.InventoryScope, InventoryCoverage: a.InventoryCoverage}
	if !compatibleContextDiff(a, b) {
		return d, errors.New("context diff: incompatible kind, methodology, or coverage")
	}
	left, err := contextOccurrenceIndex(a.Occurrences)
	if err != nil {
		return d, err
	}
	right, err := contextOccurrenceIndex(b.Occurrences)
	if err != nil {
		return d, err
	}
	d.Changes = compareContextOccurrences(left, right)
	if a.InventoryMethod == "" {
		return d, nil
	}
	leftCandidates, err := contextOccurrenceIndex(a.Candidates)
	if err != nil {
		return d, err
	}
	rightCandidates, err := contextOccurrenceIndex(b.Candidates)
	if err != nil {
		return d, err
	}
	d.CandidateChanges = compareContextOccurrences(leftCandidates, rightCandidates)
	return d, nil
}

func compatibleContextDiff(a, b contextDocument) bool {
	return a.Kind == b.Kind && a.Method == b.Method && a.Coverage == b.Coverage && a.Scope == b.Scope && a.Model == b.Model && reflect.DeepEqual(a.ContextWindow, b.ContextWindow) && a.InventoryMethod == b.InventoryMethod && a.InventoryScope == b.InventoryScope && a.InventoryCoverage == b.InventoryCoverage
}

func contextOccurrenceIndex(occurrences []contextOccurrence) (map[string]contextOccurrence, error) {
	index := map[string]contextOccurrence{}
	for _, occurrence := range occurrences {
		key := occurrence.Source + "/" + occurrence.Name
		if _, found := index[key]; found {
			return nil, errors.New("context diff: duplicate occurrence labels")
		}
		index[key] = occurrence
	}
	return index, nil
}

func compareContextOccurrences(left, right map[string]contextOccurrence) []contextChange {
	keys := map[string]bool{}
	for key := range left {
		keys[key] = true
	}
	for key := range right {
		keys[key] = true
	}
	changes := []contextChange{}
	for key := range keys {
		before, hasBefore := left[key]
		after, hasAfter := right[key]
		if hasBefore && hasAfter && reflect.DeepEqual(before, after) {
			continue
		}
		changes = append(changes, newContextChange(before, after, hasBefore, hasAfter))
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		return a.Source < b.Source || a.Source == b.Source && a.Name < b.Name
	})
	return changes
}

func newContextChange(before, after contextOccurrence, hasBefore, hasAfter bool) contextChange {
	change := contextChange{}
	if hasBefore {
		change.Before = &before
		change.Source, change.Name = before.Source, before.Name
	}
	if hasAfter {
		change.After = &after
		change.Source, change.Name = after.Source, after.Name
	}
	return change
}
