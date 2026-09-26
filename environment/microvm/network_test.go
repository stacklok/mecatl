package microvm

import (
	"context"
	"errors"
	"testing"

	gomicrovmnet "github.com/stacklok/go-microvm/net"
	"github.com/stacklok/go-microvm/net/firewall"
)

func TestMicroVMMVP_Scenario6_PermissiveIPv4WithOptionalFailClosedTightening(t *testing.T) {
	t.Parallel()

	t.Run("built-in default is unrestricted IPv4 with external IPv6 unsupported", func(t *testing.T) {
		provider := &fakeNetworkProvider{socket: "/network.sock"}
		guest := &fakeGuestNetwork{ipv6Enabled: true}
		controller := NewNetworkController(func() gomicrovmnet.Provider { return provider }, guest)
		handle, err := controller.Start(context.Background(), GuestEgressPolicy{})
		if err != nil {
			t.Fatalf("Start(default): %v", err)
		}
		if handle.GuestEgress != "permissive IPv4 (IPv6 stack enabled; external IPv6 unrouted/unsupported)" {
			t.Fatalf("default status = %q", handle.GuestEgress)
		}
		if provider.cfg.FirewallDefaultAction != firewall.Allow || provider.cfg.EgressPolicy != nil {
			t.Fatalf("default provider config = %+v, want unfiltered egress", provider.cfg)
		}
		if guest.disableCalls != 0 || !guest.ipv6Enabled {
			t.Fatalf("default changed IPv6: calls=%d enabled=%v", guest.disableCalls, guest.ipv6Enabled)
		}
	})

	for _, policy := range []GuestEgressPolicy{
		{Mode: EgressDenyAll},
		{Mode: EgressAllowlist, Allow: []EgressDestination{{Hostname: "example.com", Port: 443, Protocol: ProtocolTCP}}},
	} {
		t.Run(string(policy.Mode)+" covers both stacks", func(t *testing.T) {
			provider := &fakeNetworkProvider{socket: "/network.sock"}
			guest := &fakeGuestNetwork{ipv6Enabled: true}
			controller := NewNetworkController(func() gomicrovmnet.Provider { return provider }, guest)
			if _, err := controller.Start(context.Background(), policy); err != nil {
				t.Fatal(err)
			}
			if provider.cfg.FirewallDefaultAction != firewall.Deny || provider.cfg.EgressPolicy == nil || guest.disableCalls != 1 || guest.ipv6Enabled {
				t.Fatalf("tightening did not cover IPv4 and IPv6: cfg=%+v guest=%+v", provider.cfg, guest)
			}
		})
	}

	t.Run("selected enforcement failure aborts without permissive fallback", func(t *testing.T) {
		provider := &fakeNetworkProvider{socket: "/network.sock"}
		guest := &fakeGuestNetwork{ipv6Enabled: true, disableErr: errors.New("sysctl refused")}
		controller := NewNetworkController(func() gomicrovmnet.Provider { return provider }, guest)
		if _, err := controller.Start(context.Background(), GuestEgressPolicy{Mode: EgressDenyAll}); err == nil {
			t.Fatal("Start(tightening) succeeded after enforcement failure")
		}
		if provider.stops != 1 {
			t.Fatalf("failed tightening provider stops = %d, want 1", provider.stops)
		}
	})
}

func TestADR_0224_ExplicitNetworkProviderNeverDegrades(t *testing.T) {
	t.Parallel()

	startFailure := errors.New("hosted network unavailable")
	tests := []struct {
		name       string
		provider   *fakeNetworkProvider
		guest      *fakeGuestNetwork
		wantErr    bool
		wantStops  int
		wantStarts int
	}{
		{name: "provider starts with deny-all enforcement", provider: &fakeNetworkProvider{socket: "/network.sock"}, guest: &fakeGuestNetwork{}, wantStarts: 1},
		{name: "provider start failure aborts", provider: &fakeNetworkProvider{startErr: startFailure}, guest: &fakeGuestNetwork{}, wantErr: true, wantStarts: 1},
		{name: "empty provider endpoint aborts", provider: &fakeNetworkProvider{}, guest: &fakeGuestNetwork{}, wantErr: true, wantStarts: 1, wantStops: 1},
		{name: "ipv6 enforcement failure aborts", provider: &fakeNetworkProvider{socket: "/network.sock"}, guest: &fakeGuestNetwork{disableErr: errors.New("sysctl refused")}, wantErr: true, wantStarts: 1, wantStops: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			controller := NewNetworkController(func() gomicrovmnet.Provider { return tc.provider }, tc.guest)
			_, err := controller.Start(context.Background(), GuestEgressPolicy{Mode: EgressDenyAll})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Start() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.provider.starts != tc.wantStarts || tc.provider.stops != tc.wantStops {
				t.Fatalf("provider starts/stops = %d/%d, want %d/%d", tc.provider.starts, tc.provider.stops, tc.wantStarts, tc.wantStops)
			}
			if tc.provider.starts > 0 {
				if tc.provider.cfg.EgressPolicy == nil || tc.provider.cfg.FirewallDefaultAction != firewall.Deny {
					t.Fatalf("provider config = %+v, want explicit deny-default egress policy", tc.provider.cfg)
				}
			}
			if tc.wantErr && tc.provider.startErr != nil && tc.guest.disableCalls != 0 {
				t.Fatalf("guest IPv6 configuration ran after provider failure: %d calls", tc.guest.disableCalls)
			}
		})
	}

	t.Run("missing selected provider fails closed", func(t *testing.T) {
		controller := NewNetworkController(func() gomicrovmnet.Provider { return nil }, &fakeGuestNetwork{})
		if _, err := controller.Start(context.Background(), GuestEgressPolicy{Mode: EgressDenyAll}); err == nil {
			t.Fatal("Start() error = nil, want missing-provider rejection")
		}
	})
}

func TestMicroVMEnvironments_Scenario4_GuestEgressIsFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy GuestEgressPolicy
		probes []guestProbe
	}{
		{
			name:   "deny all",
			policy: GuestEgressPolicy{Mode: EgressDenyAll},
			probes: []guestProbe{{host: "example.com", port: 443, protocol: ProtocolTCP, want: false}},
		},
		{
			name: "hostname port protocol allowlist",
			policy: GuestEgressPolicy{Mode: EgressAllowlist, Allow: []EgressDestination{
				{Hostname: "api.example.com", Port: 443, Protocol: ProtocolTCP},
				{Hostname: "dns.example.com", Port: 53, Protocol: ProtocolUDP},
			}},
			probes: []guestProbe{
				{host: "api.example.com", port: 443, protocol: ProtocolTCP, want: true},
				{host: "api.example.com", port: 80, protocol: ProtocolTCP, want: false},
				{host: "api.example.com", port: 443, protocol: ProtocolUDP, want: false},
				{host: "other.example.com", port: 443, protocol: ProtocolTCP, want: false},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeNetworkProvider{socket: "/network.sock"}
			guest := &fakeGuestNetwork{ipv6Enabled: true}
			controller := NewNetworkController(func() gomicrovmnet.Provider { return provider }, guest)
			handle, err := controller.Start(context.Background(), tc.policy)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if handle.GuestEgress == "" {
				t.Fatal("guest egress status is empty")
			}
			for _, probe := range tc.probes {
				if got := fakeGuestCanDial(provider.cfg, guest, probe); got != probe.want {
					t.Errorf("guest dial %s:%d/%d = %v, want %v", probe.host, probe.port, probe.protocol, got, probe.want)
				}
			}
			if guest.disableCalls != 1 || fakeGuestCanDial(provider.cfg, guest, guestProbe{ipv6: true, host: "2001:db8::1", port: 443, protocol: ProtocolTCP}) {
				t.Fatalf("IPv6 disable calls = %d; IPv6 must be unavailable", guest.disableCalls)
			}
		})
	}
}

func TestSysctlGuestNetwork_DisablesAllIPv6Scopes(t *testing.T) {
	t.Parallel()
	got := make(map[string]string)
	guest := NewSysctlGuestNetwork(func(key, value string) error {
		got[key] = value
		return nil
	})
	if err := guest.DisableIPv6(context.Background()); err != nil {
		t.Fatalf("DisableIPv6() error = %v", err)
	}
	for _, key := range []string{
		"net.ipv6.conf.all.disable_ipv6",
		"net.ipv6.conf.default.disable_ipv6",
		"net.ipv6.conf.eth0.disable_ipv6",
	} {
		if got[key] != "1" {
			t.Errorf("sysctl %q = %q, want 1", key, got[key])
		}
	}
}

type fakeNetworkProvider struct {
	cfg       gomicrovmnet.Config
	socket    string
	startErr  error
	startHook func() error
	startCtx  context.Context
	starts    int
	stops     int
	denials   uint64
}

func (p *fakeNetworkProvider) Start(ctx context.Context, cfg gomicrovmnet.Config) error {
	p.starts++
	p.cfg = cfg
	p.startCtx = ctx
	if p.startHook != nil {
		return p.startHook()
	}
	return p.startErr
}

func (p *fakeNetworkProvider) SocketPath() string    { return p.socket }
func (p *fakeNetworkProvider) Stop()                 { p.stops++ }
func (p *fakeNetworkProvider) EgressDenials() uint64 { return p.denials }

type fakeGuestNetwork struct {
	disableCalls int
	disableErr   error
	ipv6Enabled  bool
}

func (g *fakeGuestNetwork) DisableIPv6(context.Context) error {
	g.disableCalls++
	if g.disableErr == nil {
		g.ipv6Enabled = false
	}
	return g.disableErr
}

type guestProbe struct {
	host     string
	port     uint16
	protocol EgressProtocol
	ipv6     bool
	want     bool
}

func fakeGuestCanDial(cfg gomicrovmnet.Config, guest *fakeGuestNetwork, probe guestProbe) bool {
	if probe.ipv6 {
		return guest.ipv6Enabled
	}
	if cfg.EgressPolicy == nil || cfg.FirewallDefaultAction != firewall.Deny {
		return false
	}
	for _, allowed := range cfg.EgressPolicy.AllowedHosts {
		if allowed.Name != probe.host || allowed.Protocol != uint8(probe.protocol) {
			continue
		}
		for _, port := range allowed.Ports {
			if port == probe.port {
				return true
			}
		}
	}
	return false
}
