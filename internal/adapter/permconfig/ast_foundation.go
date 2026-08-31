package permconfig

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// permconfigLocation is the source position a schema error may use. It retains
// coordinates only, never YAML-derived token text.
type permconfigLocation struct {
	Line        int
	Column      int
	HasLocation bool
}

func permconfigMapping(node ast.Node) (*ast.MappingNode, bool) {
	mapping, ok := node.(*ast.MappingNode)
	return mapping, ok
}

func permconfigNodeLocation(node ast.Node) permconfigLocation {
	if node == nil || node.GetToken() == nil || node.GetToken().Position == nil {
		return permconfigLocation{}
	}
	position := node.GetToken().Position
	if position.Line <= 0 || position.Column <= 0 {
		return permconfigLocation{}
	}
	return permconfigLocation{Line: position.Line, Column: position.Column, HasLocation: true}
}

func permconfigMappingEntryLocation(entry *ast.MappingValueNode) permconfigLocation {
	if entry == nil {
		return permconfigLocation{}
	}
	if location := permconfigNodeLocation(entry.Key); location.HasLocation {
		return location
	}
	return permconfigNodeLocation(entry.Value)
}

func permconfigMappingKey(node ast.MapKeyNode) (string, bool) {
	key, ok := node.(*ast.StringNode)
	if !ok {
		return "", false
	}
	return key.Value, true
}

func permconfigNull(node ast.Node) bool {
	_, ok := node.(*ast.NullNode)
	return ok
}

type permconfigNodeValue struct{ Node ast.Node }

func (v *permconfigNodeValue) UnmarshalYAML(node ast.Node) error {
	if node == nil {
		return fmt.Errorf("missing YAML node")
	}
	v.Node = node
	return nil
}

// permconfigNodePointer preserves nil-for-null semantics while allowing goccy's
// public NodeUnmarshaler hook to decode a custom schema value through a pointer.
type permconfigNodePointer[T any] struct{ target **T }

func newPermconfigNodePointer[T any](target **T) *permconfigNodePointer[T] {
	return &permconfigNodePointer[T]{target: target}
}

func (p *permconfigNodePointer[T]) UnmarshalYAML(node ast.Node) error {
	if permconfigNull(node) {
		*p.target = nil
		return nil
	}
	value := new(T)
	if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(node, value); err != nil {
		return err
	}
	*p.target = value
	return nil
}

type permconfigTokenError interface {
	GetToken() *token.Token
}

func permconfigErrorLocation(err error) permconfigLocation {
	var located permconfigTokenError
	if !errors.As(err, &located) {
		return permconfigLocation{}
	}
	parserToken := located.GetToken()
	if parserToken == nil || parserToken.Position == nil || parserToken.Position.Line <= 0 || parserToken.Position.Column <= 0 {
		return permconfigLocation{}
	}
	return permconfigLocation{Line: parserToken.Position.Line, Column: parserToken.Position.Column, HasLocation: true}
}
