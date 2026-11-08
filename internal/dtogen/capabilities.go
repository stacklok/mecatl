package dtogen

import (
	"fmt"
	"strings"
)

const (
	methodClone = "Clone"
	methodEqual = "Equal"
)

// capabilities selects independent operations over the same typed value graph.
type capabilities struct {
	cbor, clone, equal, view bool
}

func parseEmit(text string) (capabilities, error) {
	var c capabilities
	for _, name := range strings.Split(text, ",") {
		var selected *bool
		switch name {
		case "cbor":
			selected = &c.cbor
		case "clone":
			selected = &c.clone
		case "view":
			selected = &c.view
		case "equal":
			selected = &c.equal
		default:
			return c, fmt.Errorf("-emit requires explicit comma-separated cbor,clone,equal,view capabilities; unknown capability %q", name)
		}
		if *selected {
			return c, fmt.Errorf("duplicate -emit capability %q", name)
		}
		*selected = true
	}
	return c, nil
}

func (c capabilities) method(name string) bool {
	switch name {
	case "CheckCBOR", "AppendCBOR", "ReadCBOR":
		return c.cbor
	case methodClone:
		return c.clone
	case methodEqual:
		return c.equal
	default:
		return false
	}
}
