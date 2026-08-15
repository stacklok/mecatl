package lint

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCheckADRNumberUniqueness(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  []ADRNumberDuplicate
	}{
		{
			name:  "unique",
			files: []string{"0001-first.md", "0002-second.md", "README.md", "template.md"},
		},
		{
			name:  "multiple duplicates are deterministic",
			files: []string{"0010-zulu.md", "0002-beta.md", "0010-alpha.md", "0002-alpha.md"},
			want: []ADRNumberDuplicate{
				{Number: 2, Files: []string{"0002-alpha.md", "0002-beta.md"}},
				{Number: 10, Files: []string{"0010-alpha.md", "0010-zulu.md"}},
			},
		},
		{
			name:  "numeric normalization",
			files: []string{"0108-leading.md", "108-plain.md"},
			want: []ADRNumberDuplicate{
				{Number: 108, Files: []string{"0108-leading.md", "108-plain.md"}},
			},
		},
		{
			name: "near misses ignored",
			files: []string{
				"README.md", "template.md", "notes.md", "-missing-number.md",
				"0012-.md", "0012-no-extension", "adr-0012-example.md", "0012-example.txt",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CheckADRNumberUniqueness(test.files)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("CheckADRNumberUniqueness() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRealADRNumberUniqueness(t *testing.T) {
	root := repoRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "docs", "adr", "*.md"))
	if err != nil {
		t.Fatalf("glob ADRs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no ADRs matched (wrong repo root?)")
	}

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	if duplicates := CheckADRNumberUniqueness(names); len(duplicates) > 0 {
		for _, duplicate := range duplicates {
			t.Errorf("ADR number %04d is used by %v", duplicate.Number, duplicate.Files)
		}
	}
}
