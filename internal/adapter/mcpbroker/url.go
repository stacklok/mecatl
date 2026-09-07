package mcpbroker

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ValidateProtectedURL admits an operator-selected HTTPS endpoint without
// making assumptions about whether its host is public or private. The caller
// owns the trust boundary for private infrastructure; transport code must
// still pin and verify it.
func ValidateProtectedURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || (u.RawPath != "" && u.RawPath != u.Path) {
		return fmt.Errorf("%s must be an exact HTTPS URL", label)
	}
	if strings.ContainsAny(u.Host, "\r\n") {
		return errors.New("URL host contains a control character")
	}
	return nil
}
