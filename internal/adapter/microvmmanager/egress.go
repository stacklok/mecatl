package microvmmanager

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"
)

const (
	// GuestEgressPermissive allows unrestricted guest IPv4 egress.
	GuestEgressPermissive = "permissive"
	// GuestEgressDenyAll blocks all guest network destinations.
	GuestEgressDenyAll = "deny-all"
	// GuestEgressAllowlist permits only explicitly selected destinations.
	GuestEgressAllowlist = "allowlist"
)

// GuestEgressSelection is the host-operator CLI projection of guest egress.
// It is intentionally not part of any API or project configuration surface.
type GuestEgressSelection struct {
	Mode  string
	Allow []EgressRule
}

// NewGuestEgressSelection returns the byte-compatible default policy selection.
func NewGuestEgressSelection() GuestEgressSelection {
	return GuestEgressSelection{Mode: GuestEgressPermissive}
}

// ModeValue returns a flag.Value for --microvm-guest-egress.
func (s *GuestEgressSelection) ModeValue() flag.Value { return guestEgressModeValue{s} }

// AllowValue returns a repeatable flag.Value for --microvm-guest-allow.
func (s *GuestEgressSelection) AllowValue() flag.Value { return guestEgressAllowValue{s} }

// Validate rejects incomplete or contradictory selections.
func (s GuestEgressSelection) Validate() error {
	switch s.Mode {
	case GuestEgressPermissive, GuestEgressDenyAll:
		if len(s.Allow) != 0 {
			return fmt.Errorf("--microvm-guest-allow is valid only with --microvm-guest-egress=%s", GuestEgressAllowlist)
		}
	case GuestEgressAllowlist:
		if len(s.Allow) == 0 {
			return errors.New("--microvm-guest-egress=allowlist requires at least one --microvm-guest-allow rule")
		}
	default:
		return fmt.Errorf("invalid --microvm-guest-egress %q (want permissive|deny-all|allowlist)", s.Mode)
	}
	seen := make(map[EgressRule]struct{}, len(s.Allow))
	for _, rule := range s.Allow {
		if err := validateEgressRule(rule); err != nil {
			return err
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("duplicate --microvm-guest-allow rule %s", formatEgressRule(rule))
		}
		seen[rule] = struct{}{}
	}
	return nil
}

type guestEgressModeValue struct{ selection *GuestEgressSelection }

func (v guestEgressModeValue) String() string {
	if v.selection == nil {
		return GuestEgressPermissive
	}
	return v.selection.Mode
}

func (v guestEgressModeValue) Set(value string) error {
	if v.selection == nil {
		return errors.New("guest egress selection is nil")
	}
	if value != GuestEgressPermissive && value != GuestEgressDenyAll && value != GuestEgressAllowlist {
		return fmt.Errorf("invalid guest egress mode %q (want permissive|deny-all|allowlist)", value)
	}
	v.selection.Mode = value
	return nil
}

type guestEgressAllowValue struct{ selection *GuestEgressSelection }

func (guestEgressAllowValue) String() string { return "" }

func (v guestEgressAllowValue) Set(value string) error {
	if v.selection == nil {
		return errors.New("guest egress selection is nil")
	}
	rule, err := parseEgressRule(value)
	if err != nil {
		return err
	}
	for _, existing := range v.selection.Allow {
		if existing == rule {
			return fmt.Errorf("duplicate guest egress rule %q", value)
		}
	}
	v.selection.Allow = append(v.selection.Allow, rule)
	return nil
}

func parseEgressRule(value string) (EgressRule, error) {
	if value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return EgressRule{}, fmt.Errorf("invalid guest egress rule %q: whitespace and control characters are not allowed", value)
	}
	authority, protocol, ok := strings.Cut(value, "/")
	if !ok || strings.Contains(protocol, "/") {
		return EgressRule{}, fmt.Errorf("invalid guest egress rule %q (want HOST:PORT/tcp|udp)", value)
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		return EgressRule{}, fmt.Errorf("invalid guest egress rule %q (want HOST:PORT/tcp|udp): %w", value, err)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return EgressRule{}, fmt.Errorf("invalid guest egress port %q: want 1..65535", portText)
	}
	var protocolNumber uint8
	switch protocol {
	case "tcp":
		protocolNumber = 6
	case "udp":
		protocolNumber = 17
	default:
		return EgressRule{}, fmt.Errorf("unsupported guest egress protocol %q (want tcp or udp)", protocol)
	}
	rule := EgressRule{Hostname: host, Port: uint16(port), Protocol: protocolNumber}
	if err := validateEgressRule(rule); err != nil {
		return EgressRule{}, err
	}
	return rule, nil
}

func validateEgressRule(rule EgressRule) error {
	if !validEgressHostname(rule.Hostname) || rule.Port == 0 || (rule.Protocol != 6 && rule.Protocol != 17) {
		return fmt.Errorf("invalid guest egress destination %q", formatEgressRule(rule))
	}
	return nil
}

func validEgressHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || strings.HasPrefix(host, "*.") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func formatEgressRule(rule EgressRule) string {
	protocol := strconv.Itoa(int(rule.Protocol))
	switch rule.Protocol {
	case 6:
		protocol = "tcp"
	case 17:
		protocol = "udp"
	}
	return fmt.Sprintf("%s:%d/%s", rule.Hostname, rule.Port, protocol)
}

// GuestEgressSummary returns the safe operator-facing configured policy summary.
func GuestEgressSummary(selection GuestEgressSelection) string {
	switch selection.Mode {
	case GuestEgressAllowlist:
		return fmt.Sprintf("allowlist (%d destinations)", len(selection.Allow))
	case GuestEgressDenyAll:
		return GuestEgressDenyAll
	default:
		return GuestEgressPermissive
	}
}

func preserveGuestEgressPolicy(path string, policy *Policy) error {
	selection, err := readGuestEgressPolicy(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("refusing to replace existing microvmd guest egress policy: %w", err)
	}
	policy.GuestEgressMode = selection.Mode
	policy.GuestAllow = append([]EgressRule(nil), selection.Allow...)
	return nil
}

func readGuestEgressPolicy(path string) (GuestEgressSelection, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return GuestEgressSelection{}, err
	}
	stat, ownerOK := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownerOK || int(stat.Uid) != os.Getuid() {
		return GuestEgressSelection{}, errors.New("existing microvmd config is not an owner-only regular file")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- manager-owned validated absolute config path.
	if err != nil {
		return GuestEgressSelection{}, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return GuestEgressSelection{}, errors.New("existing microvmd config is corrupt")
	}
	raw, ok := top["guest_egress"]
	if !ok {
		return GuestEgressSelection{}, errors.New("existing microvmd config omits guest egress policy")
	}
	var stored struct {
		Mode  string
		Allow []EgressRule
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return GuestEgressSelection{}, errors.New("existing microvmd guest egress policy is corrupt")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return GuestEgressSelection{}, errors.New("existing microvmd guest egress policy is corrupt")
	}
	selection := GuestEgressSelection{Mode: stored.Mode, Allow: stored.Allow}
	if err := selection.Validate(); err != nil {
		return GuestEgressSelection{}, errors.New("existing microvmd guest egress policy is unsafe")
	}
	return selection, nil
}
