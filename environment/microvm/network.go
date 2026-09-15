package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	gomicrovmnet "github.com/stacklok/go-microvm/net"
	"github.com/stacklok/go-microvm/net/firewall"
	"github.com/stacklok/go-microvm/net/hosted"
)

// EgressMode is the closed guest-network policy mode.
type EgressMode string

const (
	// EgressPermissive permits unrestricted IPv4 guest egress. The guest IPv6
	// stack remains enabled, but go-microvm's hosted topology does not route
	// external IPv6. It is also the zero-value and built-in profile default.
	EgressPermissive EgressMode = "permissive"
	// EgressDenyAll permits no guest destination.
	EgressDenyAll EgressMode = "deny-all"
	// EgressAllowlist permits only explicitly listed destinations.
	EgressAllowlist EgressMode = "allowlist"
)

// EgressProtocol is an IP transport protocol accepted by go-microvm's filter.
type EgressProtocol uint8

const (
	// ProtocolTCP permits only TCP for a destination.
	ProtocolTCP EgressProtocol = 6
	// ProtocolUDP permits only UDP for a destination.
	ProtocolUDP EgressProtocol = 17
)

// EgressDestination identifies one guest-visible hostname, port and protocol.
type EgressDestination struct {
	Hostname string
	Port     uint16
	Protocol EgressProtocol
}

// GuestEgressPolicy is operator-resolved policy for guest processes only.
type GuestEgressPolicy struct {
	Mode  EgressMode
	Allow []EgressDestination
}

// GuestNetworkConfigurator applies guest-side settings required by tightening
// modes because go-microvm v0.0.40's frame filter is IPv4-only.
type GuestNetworkConfigurator interface {
	DisableIPv6(context.Context) error
}

// NetworkProviderFactory selects the hosted provider used by an environment.
type NetworkProviderFactory func() gomicrovmnet.Provider

// NetworkHandle is the configured provider endpoint handed to VM creation.
type NetworkHandle struct {
	SocketPath  string
	Provider    gomicrovmnet.Provider
	GuestEgress string
}

// NetworkController configures the selected provider. Permissive mode leaves
// the guest IPv6 stack enabled, although hosted external IPv6 is unrouted and
// unsupported. Selected tightening additionally closes the provider's IPv6
// filtering gap before readiness succeeds.
type NetworkController struct {
	provider          NetworkProviderFactory
	guest             GuestNetworkConfigurator
	guestEnforcedBoot bool
}

// NewNetworkController builds a controller around an explicit provider
// selection. A nil or failed provider is an error; there is no implicit path.
func NewNetworkController(provider NetworkProviderFactory, guest GuestNetworkConfigurator) *NetworkController {
	return &NetworkController{provider: provider, guest: guest}
}

// NewHostedNetworkController selects go-microvm's in-process hosted provider.
func NewHostedNetworkController(guest GuestNetworkConfigurator) *NetworkController {
	return NewNetworkController(func() gomicrovmnet.Provider {
		provider := hosted.NewProvider()
		return &observedHostedProvider{Provider: provider}
	}, guest)
}

// NewHostedBootNetworkController selects hosted IPv4 filtering when the guest
// image applies IPv6 policy before starting its authenticated control service.
// The owning runtime must verify that service before reporting readiness.
func NewHostedBootNetworkController() *NetworkController {
	controller := NewHostedNetworkController(nil)
	controller.guestEnforcedBoot = true
	return controller
}

type egressDenialSource interface{ EgressDenials() uint64 }

type observedHostedProvider struct{ *hosted.Provider }

func (p *observedHostedProvider) EgressDenials() uint64 {
	if p == nil || p.Provider == nil || p.Relay() == nil {
		return 0
	}
	return p.Relay().Metrics().FramesDropped.Load()
}

// Start validates and starts the selected provider with deny-default filtering,
// then requires either immediate guest IPv6 disablement or an explicit boot-time
// enforcement contract. Any immediate enforcement failure stops the provider.
func (c *NetworkController) Start(ctx context.Context, policy GuestEgressPolicy) (NetworkHandle, error) {
	return c.start(ctx, policy, "")
}

// StartForDoctor exercises the production provider in a caller-owned private
// runtime directory, then returns the live handle for immediate teardown.
func (c *NetworkController) StartForDoctor(ctx context.Context, policy GuestEgressPolicy, runtimeDir string) (NetworkHandle, error) {
	if !filepath.IsAbs(runtimeDir) {
		return NetworkHandle{}, errors.New("microvm doctor network runtime directory must be absolute")
	}
	return c.start(ctx, policy, runtimeDir)
}

func (c *NetworkController) start(ctx context.Context, policy GuestEgressPolicy, logDir string) (NetworkHandle, error) {
	if c == nil || c.provider == nil {
		return NetworkHandle{}, errors.New("microvm network provider is not configured")
	}
	mode := policy.normalizedMode()
	if mode != EgressPermissive && c.guest == nil && !c.guestEnforcedBoot {
		return NetworkHandle{}, errors.New("microvm guest IPv6 enforcement is not configured")
	}
	allowed, err := policy.goMicroVMHosts()
	if err != nil {
		return NetworkHandle{}, err
	}
	provider := c.provider()
	if provider == nil {
		return NetworkHandle{}, errors.New("selected microvm network provider is nil")
	}
	cfg := gomicrovmnet.Config{LogDir: logDir, FirewallDefaultAction: firewall.Allow}
	if mode != EgressPermissive {
		cfg.EgressPolicy = &gomicrovmnet.EgressPolicy{AllowedHosts: allowed}
		cfg.FirewallDefaultAction = firewall.Deny
	}
	if err := provider.Start(ctx, cfg); err != nil {
		return NetworkHandle{}, fmt.Errorf("start selected microvm network provider: %w", err)
	}
	fail := func(err error) (NetworkHandle, error) {
		provider.Stop()
		return NetworkHandle{}, err
	}
	if strings.TrimSpace(provider.SocketPath()) == "" || !filepath.IsAbs(provider.SocketPath()) {
		return fail(errors.New("selected microvm network provider returned no absolute endpoint"))
	}
	if mode != EgressPermissive && c.guest != nil {
		if err := c.guest.DisableIPv6(ctx); err != nil {
			return fail(fmt.Errorf("disable guest IPv6 for selected tightening: %w", err))
		}
	}
	return NetworkHandle{
		SocketPath: provider.SocketPath(), Provider: provider,
		GuestEgress: policy.status(),
	}, nil
}

// Status returns the daemon-authored response-safe summary of enforced guest egress.
func (p GuestEgressPolicy) Status() string { return p.status() }

func (p GuestEgressPolicy) normalizedMode() EgressMode {
	if p.Mode == "" {
		return EgressPermissive
	}
	return p.Mode
}

func (p GuestEgressPolicy) tightened() bool { return p.normalizedMode() != EgressPermissive }

func (p GuestEgressPolicy) status() string {
	switch p.normalizedMode() {
	case EgressPermissive:
		return "permissive IPv4 (IPv6 stack enabled; external IPv6 unrouted/unsupported)"
	case EgressDenyAll:
		return "deny-all (IPv4 filtered; IPv6 disabled)"
	default:
		return fmt.Sprintf("allowlist (%d destinations; IPv4 filtered; IPv6 disabled)", len(p.Allow))
	}
}

func (p GuestEgressPolicy) goMicroVMHosts() ([]gomicrovmnet.EgressHost, error) {
	switch p.normalizedMode() {
	case EgressPermissive:
		if len(p.Allow) != 0 {
			return nil, errors.New("permissive guest egress policy cannot contain destinations")
		}
		return nil, nil
	case EgressDenyAll:
		if len(p.Allow) != 0 {
			return nil, errors.New("deny-all guest egress policy cannot contain destinations")
		}
		return []gomicrovmnet.EgressHost{}, nil
	case EgressAllowlist:
		if len(p.Allow) == 0 {
			return nil, errors.New("guest egress allowlist is empty; use deny-all explicitly")
		}
	default:
		return nil, fmt.Errorf("unknown guest egress policy mode %q", p.Mode)
	}

	hosts := make([]gomicrovmnet.EgressHost, 0, len(p.Allow))
	for i, destination := range p.Allow {
		hostname := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(destination.Hostname), "."))
		if !validEgressHostname(hostname) {
			return nil, fmt.Errorf("guest egress destination %d has invalid hostname %q", i, destination.Hostname)
		}
		if destination.Port == 0 {
			return nil, fmt.Errorf("guest egress destination %d has empty port", i)
		}
		if destination.Protocol != ProtocolTCP && destination.Protocol != ProtocolUDP {
			return nil, fmt.Errorf("guest egress destination %d has unsupported protocol %d", i, destination.Protocol)
		}
		hosts = append(hosts, gomicrovmnet.EgressHost{
			Name: hostname, Ports: []uint16{destination.Port}, Protocol: uint8(destination.Protocol),
		})
	}
	return hosts, nil
}

func validEgressHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	if strings.HasPrefix(host, "*.") {
		host = host[2:]
		if strings.Count(host, ".") < 1 {
			return false
		}
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

// SysctlWriter is the guest-side sysctl write seam.
type SysctlWriter func(key, value string) error

// SysctlGuestNetwork disables IPv6 on every current and future guest interface.
type SysctlGuestNetwork struct {
	write SysctlWriter
}

// NewSysctlGuestNetwork builds the guest-side IPv6 enforcer. The guest agent
// supplies its privileged sysctl implementation (for example harden.Set).
func NewSysctlGuestNetwork(write SysctlWriter) *SysctlGuestNetwork {
	return &SysctlGuestNetwork{write: write}
}

// DisableIPv6 applies all/default/interface-wide settings and fails if any
// setting is unavailable; partial disablement is not accepted.
func (g *SysctlGuestNetwork) DisableIPv6(_ context.Context) error {
	if g == nil || g.write == nil {
		return errors.New("guest sysctl writer is not configured")
	}
	for _, key := range []string{
		"net.ipv6.conf.all.disable_ipv6",
		"net.ipv6.conf.default.disable_ipv6",
		"net.ipv6.conf.eth0.disable_ipv6",
	} {
		if err := g.write(key, "1"); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}
