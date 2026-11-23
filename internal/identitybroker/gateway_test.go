package identitybroker_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

func TestValidateGatewayEntries(t *testing.T) {
	embedded := func(edit func(*identitybroker.GatewayEntry)) identitybroker.GatewayEntry {
		e := identitybroker.GatewayEntry{
			Kind: identitybroker.GatewayEmbedded, AS2Issuer: as2Issuer, TokenEndpoint: as2Issuer + "/oauth/token",
			Resource: as2Resource, Servers: []string{"calendar", "github"},
		}
		if edit != nil {
			edit(&e)
		}
		return e
	}
	for name, entries := range map[string][]identitybroker.GatewayEntry{
		"none":             nil,
		"an embedded one":  {embedded(nil)},
		"one routing none": {embedded(func(e *identitybroker.GatewayEntry) { e.Servers = nil })},
	} {
		if err := identitybroker.ValidateGatewayEntries(entries); err != nil {
			t.Errorf("%s: ValidateGatewayEntries = %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		entries []identitybroker.GatewayEntry
		want    string
	}{
		"two entries":         {[]identitybroker.GatewayEntry{embedded(nil), embedded(nil)}, "only one gateway entry"},
		"an external one":     {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Kind = identitybroker.GatewayExternal })}, "external gateways are not supported yet (#2812)"},
		"an unknown kind":     {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Kind = "remote" })}, `kind "remote"`},
		"no kind":             {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Kind = "" })}, `kind ""`},
		"no issuer":           {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.AS2Issuer = "" })}, "AS-2 issuer"},
		"a spaced issuer":     {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.AS2Issuer = "https://as 2" })}, "AS-2 issuer"},
		"no token endpoint":   {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.TokenEndpoint = "" })}, "token endpoint"},
		"no resource":         {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Resource = "" })}, "resource"},
		"a control character": {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Resource = as2Resource + "\x00" })}, "resource"},
		"an empty server":     {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Servers = []string{""} })}, "gateway server"},
		"a repeated server":   {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Servers = []string{"github", "github"} })}, "listed twice"},
		// An entry's servers follow the gateway's server-name rule.
		"a dotted server":     {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Servers = []string{"github.ent"} })}, "single underscores"},
		"a double underscore": {[]identitybroker.GatewayEntry{embedded(func(e *identitybroker.GatewayEntry) { e.Servers = []string{"github__ent"} })}, "single underscores"},
	} {
		err := identitybroker.ValidateGatewayEntries(tc.entries)
		if !errors.Is(err, identitybroker.ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ValidateGatewayEntries = %v, want ErrInvalidConfig naming %q", name, err, tc.want)
		}
	}
}
