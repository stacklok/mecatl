package skillfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func call(t *testing.T, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return session.NewToolCall("id-skill", ToolName, raw)
}

func exec(t *testing.T, tl tool.Tool, in session.ToolCall) session.ToolResult {
	t.Helper()
	env, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test"}, stubWS{}, stubLedger{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Execute(context.Background(), in, env)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

type stubWS struct{}

func (stubWS) Root() string                                 { return "/" }
func (stubWS) Read(context.Context, string) ([]byte, error) { return nil, os.ErrNotExist }
func (stubWS) ReadVersion(context.Context, string) ([]byte, tool.FileVersion, error) {
	return nil, tool.FileVersion{}, os.ErrNotExist
}
func (stubWS) CreateFile(context.Context, string, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, os.ErrPermission
}
func (stubWS) ReplaceFile(context.Context, string, tool.FileVersion, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, os.ErrPermission
}
func (stubWS) Stat(context.Context, string) (tool.FileInfo, error) {
	return tool.FileInfo{}, os.ErrNotExist
}
func (stubWS) Glob(context.Context, string) ([]string, error)                 { return nil, nil }
func (stubWS) Grep(context.Context, string, string) ([]tool.GrepMatch, error) { return nil, nil }

type stubLedger struct{}

func (stubLedger) RecordRead(context.Context, string, tool.FileVersion) error { return nil }
func (stubLedger) RecordedVersion(context.Context, string) (tool.FileVersion, bool, error) {
	return tool.FileVersion{}, false, nil
}

func newSourceTool(source tool.SkillSource) Tool {
	return NewTool([]tool.SkillMeta{{Name: "review", Description: "Review code", Compatibility: "mecatl >= 1", AllowedTools: []string{"Grep"}}}, source)
}

func TestToolSpecSupportsOptionalAssetAndStaysReadOnly(t *testing.T) {
	tl := newSourceTool(&stubSkillSource{})
	spec := tl.Spec()
	if !tl.ReadOnly() {
		t.Fatal("Skill must remain read-only")
	}
	if !strings.Contains(string(spec.Schema), `"asset"`) || !strings.Contains(spec.Description, "{name, asset}") {
		t.Errorf("spec does not advertise optional asset: %+v", spec)
	}
	for _, forbidden := range []string{"Base directory", "absolute path", "use Read", "via Bash"} {
		if strings.Contains(spec.Description, forbidden) {
			t.Errorf("description contains forbidden %q", forbidden)
		}
	}
}

func TestActivationReturnsBodyAndLogicalInventoryWithoutReadingAsset(t *testing.T) {
	source := &stubSkillSource{
		bodies: map[string]string{"review": "Follow the review procedure."},
		assets: map[string][]tool.SkillAsset{"review": {{Name: "references/checklist.md", Size: 12}}},
	}
	res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review"}))
	if res.IsError {
		t.Fatalf("activation failed: %s", res.Content)
	}
	if source.reads != 0 {
		t.Fatalf("activation called ReadSkillAsset %d times, want zero", source.reads)
	}
	for _, want := range []string{"Skill: review", "Compatibility: mecatl >= 1", "references/checklist.md (12 bytes)", "Follow the review procedure."} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("activation missing %q: %q", want, res.Content)
		}
	}
	for _, forbidden := range []string{"Base directory", "absolute path", "Read tool", "via Bash"} {
		if strings.Contains(res.Content, forbidden) {
			t.Errorf("activation contains forbidden %q: %q", forbidden, res.Content)
		}
	}
}

func TestActivationOversizedBodyAndInventoryFitsCompleteOutputEnvelope(t *testing.T) {
	assets := []tool.SkillAsset{{Name: "references/checklist.md", Size: 12}}
	body := strings.Repeat("body-", MaxOutputBytes)
	source := &stubSkillSource{
		bodies: map[string]string{"review": body},
		assets: map[string][]tool.SkillAsset{"review": assets},
	}

	res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review"}))
	if res.IsError {
		t.Fatalf("activation failed: %s", res.Content)
	}
	raw := "Skill: review\n" +
		"Compatibility: mecatl >= 1\n" +
		"This skill declares allowed-tools: Grep. These are the tools the skill expects to use; each call still follows normal permission rules.\n" +
		renderBundledAssetInventory(assets) + "\n" + body
	want := Truncate(raw, MaxOutputBytes-len(TruncationMarker))
	if res.Content != want {
		t.Fatalf("oversized activation differs from exact reserved-marker result: got %d bytes, want %d", len(res.Content), len(want))
	}
	if len(res.Content) != MaxOutputBytes {
		t.Fatalf("complete activation result = %d bytes, want exact cap %d", len(res.Content), MaxOutputBytes)
	}
	if !strings.HasSuffix(res.Content, TruncationMarker) {
		t.Fatalf("oversized activation is missing truncation marker: %q", res.Content[len(res.Content)-100:])
	}
	if source.reads != 0 {
		t.Fatalf("activation called ReadSkillAsset %d times, want zero", source.reads)
	}
}

func TestAssetRequestReadsExactlyOneListedTextAsset(t *testing.T) {
	source := &stubSkillSource{
		assets: map[string][]tool.SkillAsset{"review": {{Name: "references/checklist.md", Size: 5}}},
		data:   map[string][]byte{"review/references/checklist.md": []byte("hello")},
	}
	res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review", "asset": "references/checklist.md"}))
	if res.IsError || source.reads != 1 {
		t.Fatalf("asset result = %#v, reads=%d", res, source.reads)
	}
	if res.Content != "Skill asset: review / references/checklist.md\n\nhello" {
		t.Errorf("asset output = %q", res.Content)
	}
}

func TestAssetRequestExactRenderedOutputBoundary(t *testing.T) {
	const asset = "references/checklist.md"
	header := fmt.Sprintf("Skill asset: review / %s\n\n", asset)
	payload := strings.Repeat("x", MaxOutputBytes-len(header))
	source := &stubSkillSource{
		assets: map[string][]tool.SkillAsset{"review": {{Name: asset, Size: int64(len(payload))}}},
		data:   map[string][]byte{"review/" + asset: []byte(payload)},
	}
	res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review", "asset": asset}))
	if res.IsError {
		t.Fatalf("exact-bound asset rejected: %s", res.Content)
	}
	if got := len(res.Content); got != MaxOutputBytes {
		t.Fatalf("rendered output length = %d, want exact cap %d", got, MaxOutputBytes)
	}
	if !strings.HasSuffix(res.Content, payload) {
		t.Fatal("successful exact-bound asset was truncated")
	}

	source.assets["review"][0].Size++
	res = exec(t, newSourceTool(source), call(t, map[string]any{"name": "review", "asset": asset}))
	if !res.IsError || source.reads != 1 {
		t.Fatalf("advertised cap+1 result = %#v, total reads=%d; want pre-read rejection", res, source.reads)
	}
}

func TestAssetRequestRejectsBeforeRead(t *testing.T) {
	tests := []struct {
		name   string
		asset  string
		listed []tool.SkillAsset
		want   string
	}{
		{"invalid", "../secret", nil, "invalid logical asset"},
		{"unknown", "references/missing.md", nil, "unknown asset"},
		{"negative advertised size", "references/bad.md", []tool.SkillAsset{{Name: "references/bad.md", Size: -1}}, "invalid advertised size"},
		{"advertised oversize", "references/big.md", []tool.SkillAsset{{Name: "references/big.md", Size: maxSkillAssetBytes + 1}}, "too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := &stubSkillSource{assets: map[string][]tool.SkillAsset{"review": tc.listed}}
			res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review", "asset": tc.asset}))
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Fatalf("result = %#v", res)
			}
			if source.reads != 0 {
				t.Fatalf("ReadSkillAsset called %d times", source.reads)
			}
		})
	}
}

func TestAssetRequestRejectsWholeInvalidPayload(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"actual oversize", []byte(strings.Repeat("x", maxSkillAssetBytes+1)), "no content returned"},
		{"invalid UTF-8", []byte{0xff}, "not valid UTF-8"},
		{"NUL", []byte("a\x00b"), "contains NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := &stubSkillSource{
				assets: map[string][]tool.SkillAsset{"review": {{Name: "references/a.txt", Size: 1}}},
				data:   map[string][]byte{"review/references/a.txt": tc.data},
			}
			res := exec(t, newSourceTool(source), call(t, map[string]any{"name": "review", "asset": "references/a.txt"}))
			if !res.IsError || !strings.Contains(res.Content, tc.want) || source.reads != 1 {
				t.Fatalf("result = %#v, reads=%d", res, source.reads)
			}
			if strings.Contains(res.Content, strings.Repeat("x", 100)) {
				t.Fatal("rejected payload leaked into error")
			}
		})
	}
}

func TestSourceFailuresAreBoundedModelVisibleErrors(t *testing.T) {
	longErr := errors.New(strings.Repeat("backend failure ", 500))
	cases := []struct {
		name string
		src  *stubSkillSource
		args map[string]any
	}{
		{"body", &stubSkillSource{bodyErr: longErr}, map[string]any{"name": "review"}},
		{"list", &stubSkillSource{bodies: map[string]string{"review": "body"}, assetsErr: longErr}, map[string]any{"name": "review"}},
		{"read", &stubSkillSource{assets: map[string][]tool.SkillAsset{"review": {{Name: "a.txt", Size: 1}}}, readErr: longErr}, map[string]any{"name": "review", "asset": "a.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := exec(t, newSourceTool(tc.src), call(t, tc.args))
			if !res.IsError || len(res.Content) > maxSourceErrorBytes+len(TruncationMarker) {
				t.Fatalf("unbounded/non-model error: len=%d result=%#v", len(res.Content), res)
			}
		})
	}
}

func TestToolRejectsUnknownSkillAndInvalidArgs(t *testing.T) {
	tl := newSourceTool(&stubSkillSource{})
	for _, in := range []session.ToolCall{
		call(t, map[string]any{"name": "missing"}),
		call(t, map[string]any{}),
		session.NewToolCall("bad", ToolName, []byte("{")),
	} {
		if res := exec(t, tl, in); !res.IsError {
			t.Errorf("result = %#v, want model-visible error", res)
		}
	}
}
