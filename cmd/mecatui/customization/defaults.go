package customization

// defaultTemplateSet contains the status and terminal-title templates shipped
// with mecatui. Keep these defaults together so every presentation surface has
// one canonical source.
func defaultTemplateSet() TemplateSet {
	return TemplateSet{
		Header: SurfaceTemplates{
			Full:    `<header><primary>mecatui{{if .Session.Title}} · {{elide 32 .Session.Title}}{{end}} · {{if .Model.ProviderID}}{{.Model.ProviderID}}/{{end}}{{.Model.DisplayName}}{{if .Model.Route}}/{{.Model.Route}}{{end}}</primary>{{if .Session.Mode}}<warning> · mode {{.Session.Mode}}</warning>{{end}}{{if and (eq .Server.ConnectionMode "connect") .Server.DisplayTarget}}<text> · {{.Server.DisplayTarget}}</text>{{end}}</header>`,
			Compact: `<header><primary>mecatui{{if .Session.Title}} · {{elide 24 .Session.Title}}{{end}} · {{.Model.DisplayName}}</primary>{{if .Session.Mode}}<warning> · mode {{.Session.Mode}}</warning>{{end}}</header>`,
			Minimal: `<header><primary>mecatui{{if .Session.Title}} · {{elide 12 .Session.Title}}{{end}}</primary></header>`,
		},
		Footer: SurfaceTemplates{
			Full:    `<footer>{{if or .Delegation.Parallel.Running .Delegation.Parallel.Finished}}<accent>⑂ parallel {{.Delegation.Parallel.Running}}◐ {{.Delegation.Parallel.Finished}}✓</accent>  {{end}}{{if or .Delegation.Subagents.Running .Delegation.Subagents.Finished}}<accent>⛭ subagents {{.Delegation.Subagents.Running}}◐ {{.Delegation.Subagents.Finished}}✓</accent>  {{end}}{{if .Delegation.Team.Total}}<accent>⟳ team-{{.Delegation.Team.ID}} · {{.Delegation.Team.Working}}/{{.Delegation.Team.Total}} working</accent>  {{end}}{{contextMeter .Context}}<text> · ↑{{.Usage.Input.Human}} ↓{{.Usage.Output.Human}}{{if .Usage.CacheWrite.Raw}} ⊕{{.Usage.CacheWrite.Human}}{{end}} cache {{.Usage.CacheReadPercent}}%</text></footer>`,
			Compact: `<footer>{{if or .Delegation.Parallel.Running .Delegation.Parallel.Finished}}<accent>⑂ {{.Delegation.Parallel.Running}}◐ {{.Delegation.Parallel.Finished}}✓</accent>  {{end}}{{if or .Delegation.Subagents.Running .Delegation.Subagents.Finished}}<accent>⛭ {{.Delegation.Subagents.Running}}◐ {{.Delegation.Subagents.Finished}}✓</accent>  {{end}}{{if .Delegation.Team.Total}}<accent>⟳ {{.Delegation.Team.Working}}/{{.Delegation.Team.Total}}</accent>  {{end}}{{contextMeterCompact .Context}}</footer>`,
			Minimal: `<footer>{{contextMeterMinimal .Context}}</footer>`,
		},
	}
}

// DefaultTitleTemplate returns the terminal-title template shipped with mecatui.
func DefaultTitleTemplate() string {
	return `{{$state := lookup .MainAgent.State "idle" "○ Ready" "connecting" "◌ Connecting" "thinking" "✦ Thinking" "running_tool" "⚙ Working" "awaiting_approval" "⚠ Approval needed" "completed" "✓ Complete" "failed" "✗ Failed" "cancelled" "■ Cancelled"}}{{if .Session.Title}}{{if $state}}{{$state}} · {{end}}{{elide 40 .Session.Title}} · mecatui{{else if and .Session.Handle $state}}{{$state}} · mecatui{{else}}mecatui{{end}}`
}
