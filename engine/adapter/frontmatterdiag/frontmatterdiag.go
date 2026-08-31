// Package frontmatterdiag formats safe YAML frontmatter diagnostics.
package frontmatterdiag

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml/token"
)

// FrontmatterParseError returns a value-free frontmatter parse diagnostic.
// Parser error text can include YAML-derived content, so only typed locations
// are retained.
func FrontmatterParseError(err error) string {
	var located interface{ GetToken() *token.Token }
	if errors.As(err, &located) {
		if parserToken := located.GetToken(); parserToken != nil && parserToken.Position != nil && parserToken.Position.Line > 0 && parserToken.Position.Column > 0 {
			return fmt.Sprintf("malformed YAML frontmatter at line %d, column %d", parserToken.Position.Line, parserToken.Position.Column)
		}
	}
	return "malformed YAML frontmatter"
}
