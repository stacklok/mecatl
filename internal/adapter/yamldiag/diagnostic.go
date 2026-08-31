// Package yamldiag classifies goccy YAML parser errors without exposing YAML content.
package yamldiag

import (
	"errors"

	"github.com/goccy/go-yaml/token"
)

// Diagnostic is the value-free portion of a YAML parse failure.
type Diagnostic struct {
	Operation   string
	Category    string
	Line        int
	Column      int
	HasLocation bool
}

type tokenError interface {
	GetToken() *token.Token
}

// Classify returns a stable parse diagnostic using only goccy's typed token
// location. It deliberately does not inspect the error string: parser-rendered
// errors can include YAML-derived content.
func Classify(operation string, err error) Diagnostic {
	diagnostic := Diagnostic{Operation: operation, Category: "parse"}

	var located tokenError
	if !errors.As(err, &located) {
		return diagnostic
	}
	parserToken := located.GetToken()
	if parserToken == nil || parserToken.Position == nil || parserToken.Position.Line <= 0 || parserToken.Position.Column <= 0 {
		return diagnostic
	}

	diagnostic.Line = parserToken.Position.Line
	diagnostic.Column = parserToken.Position.Column
	diagnostic.HasLocation = true
	return diagnostic
}
