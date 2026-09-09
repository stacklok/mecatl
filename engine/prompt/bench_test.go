package prompt_test

import (
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// sinkLayered is a package-level sink that defeats dead-code elimination: the
// compiler cannot prove prompt.Build's result is unused, so the work happens.
var sinkLayered prompt.Layered

// BenchmarkBuild measures the per-turn prompt assembly cost over the small,
// representative catalog the unit tests use (Read/Edit/Shell). This is the hot
// path: prompt.Build runs once per turn inside Engine.buildRequest. allocs/op is
// the gated KPI (see docs/adr/0019-perf-tracking.md).
func BenchmarkBuild(b *testing.B) {
	cfg := prompt.Config{
		Tools: sampleTools(),
		Env: prompt.Env{
			Cwd: "/home/dev/proj", OS: "linux", Model: "model-x",
			Date: "2026-06-14", Mode: "default",
			Shell: "/bin/bash", GitStatus: "branch: main\nstatus:\n(clean)",
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		sinkLayered = prompt.Build(cfg)
	}
}

// BenchmarkBuildLargeCatalog measures prompt assembly with a large tool catalog,
// the regime a fully-loaded session (core + MCP + delegation + memory tools)
// reaches. It surfaces any super-linear cost in the tool-spec rendering / the
// tool-discipline-hint generation as the catalog grows.
func BenchmarkBuildLargeCatalog(b *testing.B) {
	base := sampleTools()
	tools := make([]tool.ToolSpec, 0, 64)
	tools = append(tools, base...)
	for n := len(tools); n < 64; n++ {
		tools = append(tools, tool.ToolSpec{
			Name:        fmt.Sprintf("Tool%02d", n),
			Description: fmt.Sprintf("Synthetic tool %02d for the large-catalog benchmark.\nSecond line of detail.", n),
		})
	}
	cfg := prompt.Config{
		Tools: tools,
		Env: prompt.Env{
			Cwd: "/home/dev/proj", OS: "linux", Model: "model-x",
			Date: "2026-06-14", Mode: "default",
			Shell: "/bin/bash", GitStatus: "branch: main\nstatus:\n(clean)",
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		sinkLayered = prompt.Build(cfg)
	}
}
