package frontmatterdiag

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/token"
)

func TestFrontmatterParseErrorUsesGoccyTokenLocation(t *testing.T) {
	t.Parallel()

	const source = "name: leaked-secret\ndescription: [unterminated"
	var header map[string]any
	err := yaml.Unmarshal([]byte(source), &header)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want malformed YAML error")
	}

	var located interface{ GetToken() *token.Token }
	if !errors.As(err, &located) || located.GetToken() == nil || located.GetToken().Position == nil {
		t.Fatalf("Unmarshal() error = %T %q, want goccy token location", err, err)
	}
	position := located.GetToken().Position
	want := fmt.Sprintf("malformed YAML frontmatter at line %d, column %d", position.Line, position.Column)
	if got := FrontmatterParseError(err); got != want {
		t.Errorf("FrontmatterParseError() = %q, want %q", got, want)
	}

	got := FrontmatterParseError(err)
	if got == err.Error() || strings.Contains(got, "leaked-secret") || strings.Contains(got, "unterminated") {
		t.Errorf("FrontmatterParseError() = %q, leaked parser text or YAML source", got)
	}
}

func TestFrontmatterParseErrorFallbackIsValueFree(t *testing.T) {
	t.Parallel()

	err := errors.New("parser raw text includes source: super-secret-token")
	const want = "malformed YAML frontmatter"
	if got := FrontmatterParseError(err); got != want {
		t.Errorf("FrontmatterParseError() = %q, want %q", got, want)
	}
}
