package yamldiag

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
)

var (
	// ErrParseDocument reports malformed YAML without exposing parser-rendered text.
	ErrParseDocument = errors.New("invalid YAML document")
	// ErrMultipleDocuments reports input with more than one YAML document.
	ErrMultipleDocuments = errors.New("YAML input must contain one document")
	// ErrNonMappingRoot reports a document whose root is not a mapping.
	ErrNonMappingRoot = errors.New("YAML document root must be a mapping")
	// ErrAnchor reports an anchor in a document that requires unambiguous input.
	ErrAnchor = errors.New("YAML document must not contain anchors")
	// ErrAlias reports an alias in a document that requires unambiguous input.
	ErrAlias = errors.New("YAML document must not contain aliases")
	// ErrDecodeNode reports a selected-node decode failure without decoder text.
	ErrDecodeNode = errors.New("YAML selected-node decode failed")
)

// Location is a parser token location. It never carries YAML-derived text.
type Location struct {
	Line        int
	Column      int
	HasLocation bool
}

// DocumentError classifies a document boundary failure without retaining parser text.
type DocumentError struct {
	Location Location
	kind     error
}

func (e *DocumentError) Error() string { return e.kind.Error() }

func (e *DocumentError) Unwrap() error { return e.kind }

// FormatDocumentError returns a value-free external diagnostic for a document
// error. The supplied operation must be harness-authored; only the typed parser
// location may be appended.
func FormatDocumentError(operation string, err error) string {
	var documentErr *DocumentError
	if errors.As(err, &documentErr) && documentErr.Location.HasLocation {
		return fmt.Sprintf("%s at line %d, column %d", operation, documentErr.Location.Line, documentErr.Location.Column)
	}
	return operation
}

// Document is one validated YAML mapping document. Its mapping is intentionally the
// only AST surface: callers select nodes from it explicitly before decoding them.
type Document struct {
	file    *ast.File
	mapping *ast.MappingNode
}

// ParseSettingsDocument parses exactly one mapping settings document. Parser failures
// retain only goccy's typed token location; no parser-rendered error or YAML-derived
// text escapes. Settings editors use the resulting AST to preserve comments and
// unrelated content, and deliberately reject anchors and aliases as ambiguous edits.
func ParseSettingsDocument(data []byte) (*Document, error) {
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, newDocumentError(ErrParseDocument, tokenFromError(err))
	}
	if len(file.Docs) != 1 {
		return nil, newDocumentError(ErrMultipleDocuments, documentToken(file))
	}

	root := file.Docs[0]
	if root == nil || root.Body == nil {
		return nil, newDocumentError(ErrNonMappingRoot, nil)
	}
	mapping, ok := root.Body.(*ast.MappingNode)
	if !ok {
		return nil, newDocumentError(ErrNonMappingRoot, root.GetToken())
	}
	if nodes := ast.Filter(ast.AnchorType, mapping); len(nodes) > 0 {
		return nil, newDocumentError(ErrAnchor, nodes[0].GetToken())
	}
	if nodes := ast.Filter(ast.AliasType, mapping); len(nodes) > 0 {
		return nil, newDocumentError(ErrAlias, nodes[0].GetToken())
	}
	return &Document{file: file, mapping: mapping}, nil
}

// Mapping returns the validated document root for AST-preserving callers.
func (d *Document) Mapping() *ast.MappingNode {
	if d == nil {
		return nil
	}
	return d.mapping
}

// String renders the complete parsed document, including comments and document style.
func (d *Document) String() string {
	if d == nil || d.file == nil {
		return ""
	}
	return d.file.String()
}

// Decode decodes only the AST node selected by the caller. It never decodes the
// complete document implicitly and never returns a decoder-rendered error.
func (d *Document) Decode(node ast.Node, dst any) error {
	if d == nil || node == nil || dst == nil {
		return ErrDecodeNode
	}
	if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(node, dst); err != nil {
		return ErrDecodeNode
	}
	return nil
}

func newDocumentError(kind error, parserToken *token.Token) *DocumentError {
	return &DocumentError{Location: locationFromToken(parserToken), kind: kind}
}

func tokenFromError(err error) *token.Token {
	var located tokenError
	if errors.As(err, &located) {
		return located.GetToken()
	}
	return nil
}

func documentToken(file *ast.File) *token.Token {
	if file == nil || len(file.Docs) == 0 {
		return nil
	}
	return file.Docs[0].GetToken()
}

func locationFromToken(parserToken *token.Token) Location {
	if parserToken == nil || parserToken.Position == nil || parserToken.Position.Line <= 0 || parserToken.Position.Column <= 0 {
		return Location{}
	}
	return Location{Line: parserToken.Position.Line, Column: parserToken.Position.Column, HasLocation: true}
}
