package webfetch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseTargetRejectsInvalidURLs(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"/relative",
		"ftp://example.com/file",
		"http:///missing-host",
		"http://user:pass@example.com/",
		"http://example.com:not-a-port/",
		"http://example.com:0/",
		"http://example.com:65536/",
		"http://example.com:/",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := parseTarget(raw); err == nil {
				t.Fatalf("parseTarget(%q) unexpectedly succeeded", raw)
			}
		})
	}
	if _, err := parseTarget("https://example.com/" + strings.Repeat("x", maxURLBytes)); err == nil {
		t.Fatal("oversized URL unexpectedly accepted")
	}
}

func TestUnsafeAddressClasses(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"0.0.0.0",
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"100.127.255.254",
		"169.254.169.254",
		"224.0.0.1",
		"192.0.2.1",
		"198.18.0.1",
		"240.0.0.1",
		"::",
		"::1",
		"fe80::1",
		"fd00::1",
		"ff02::1",
		"2001:db8::1",
		"64:ff9b::7f00:1",
		"64:ff9b::a9fe:a9fe",
		"64:ff9b:1::1",
		"::ffff:127.0.0.1",
		"::ffff:169.254.169.254",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if !unsafeAddress(netip.MustParseAddr(raw)) {
				t.Fatalf("unsafeAddress(%s) = false", raw)
			}
		})
	}

	for _, raw := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "64:ff9b::808:808"} {
		if unsafeAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("unsafeAddress(%s) = true", raw)
		}
	}
}

func TestResolveSafeRejectsMixedAnswers(t *testing.T) {
	t.Parallel()

	f := &fetchTransport{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("127.0.0.1"),
		}, nil
	}}
	u, err := parseTarget("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.resolveSafe(context.Background(), u); err == nil {
		t.Fatal("mixed safe and unsafe answers unexpectedly accepted")
	}
}

func TestGetRejectsLiteralUnsafeTargetsBeforeRoundTrip(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://100.64.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[64:ff9b::a9fe:a9fe]/",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			var trips atomic.Int32
			f := &fetchTransport{
				lookup: func(_ context.Context, _, host string) ([]netip.Addr, error) {
					addr, err := netip.ParseAddr(host)
					if err != nil {
						return nil, err
					}
					return []netip.Addr{addr}, nil
				},
				roundTrip: func(context.Context, *url.URL, []netip.Addr) (*http.Response, error) {
					trips.Add(1)
					return response(http.StatusOK, ""), nil
				},
			}
			if _, err := f.get(context.Background(), raw); err == nil {
				t.Fatal("unsafe target unexpectedly accepted")
			}
			if trips.Load() != 0 {
				t.Fatalf("round trips = %d, want 0", trips.Load())
			}
		})
	}
}

func TestGetCancellationInterruptsResolution(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	f := &fetchTransport{
		lookup: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.get(ctx, "https://example.com/")
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestGetPinsResolvedAddressWithoutSecondLookup(t *testing.T) {
	t.Parallel()

	const publicIP = "93.184.216.34"
	var lookups atomic.Int32
	var dialAddress string
	f := &fetchTransport{
		lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			if network != "ip" || host != "example.com" {
				t.Errorf("lookup = (%q, %q)", network, host)
			}
			return []netip.Addr{netip.MustParseAddr(publicIP)}, nil
		},
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialAddress = address
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				req, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil {
					return
				}
				if req.Host != "example.com:8080" {
					t.Errorf("Host header = %q", req.Host)
				}
				_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}()
			return client, nil
		},
	}
	f.roundTrip = f.oneHop

	resp, err := f.get(context.Background(), "http://example.com:8080/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
	if lookups.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", lookups.Load())
	}
	if dialAddress != publicIP+":8080" {
		t.Fatalf("dial address = %q", dialAddress)
	}
}

func TestGetFollowsRelativeRedirectAndRevalidates(t *testing.T) {
	t.Parallel()

	var targets []string
	f := &fetchTransport{
		lookup: publicLookup,
		roundTrip: func(_ context.Context, u *url.URL, _ []netip.Addr) (*http.Response, error) {
			targets = append(targets, u.String())
			if len(targets) == 1 {
				return response(http.StatusFound, "/next?x=1"), nil
			}
			return response(http.StatusOK, ""), nil
		},
	}

	resp, err := f.get(context.Background(), "https://example.com/start")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	want := []string{"https://example.com/start", "https://example.com/next?x=1"}
	if fmt.Sprint(targets) != fmt.Sprint(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
}

func TestRedirectToPrivateAddressRejectedBeforeDial(t *testing.T) {
	t.Parallel()

	var trips atomic.Int32
	f := &fetchTransport{
		lookup: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host == "public.example" {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		},
		roundTrip: func(_ context.Context, _ *url.URL, _ []netip.Addr) (*http.Response, error) {
			trips.Add(1)
			return response(http.StatusFound, "http://metadata.example/latest"), nil
		},
	}

	if _, err := f.get(context.Background(), "https://public.example/"); err == nil {
		t.Fatal("private redirect unexpectedly accepted")
	}
	if trips.Load() != 1 {
		t.Fatalf("round trips = %d, want 1", trips.Load())
	}
}

func TestRedirectRejectsSchemeAndUserinfo(t *testing.T) {
	t.Parallel()

	for _, location := range []string{
		"file:///etc/passwd",
		"https://user:password@example.net/",
	} {
		t.Run(location, func(t *testing.T) {
			t.Parallel()
			f := &fetchTransport{
				lookup: publicLookup,
				roundTrip: func(context.Context, *url.URL, []netip.Addr) (*http.Response, error) {
					return response(http.StatusFound, location), nil
				},
			}
			if _, err := f.get(context.Background(), "https://example.com/"); err == nil {
				t.Fatal("invalid redirect unexpectedly accepted")
			}
		})
	}
}

func TestRedirectLimit(t *testing.T) {
	t.Parallel()

	var trips atomic.Int32
	f := &fetchTransport{
		lookup: publicLookup,
		roundTrip: func(context.Context, *url.URL, []netip.Addr) (*http.Response, error) {
			n := trips.Add(1)
			return response(http.StatusFound, fmt.Sprintf("/hop/%d", n)), nil
		},
	}
	if _, err := f.get(context.Background(), "https://example.com/"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("error = %v, want redirect limit", err)
	}
	if trips.Load() != maxRedirects+1 {
		t.Fatalf("round trips = %d, want %d", trips.Load(), maxRedirects+1)
	}
}

func publicLookup(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func response(status int, location string) *http.Response {
	header := make(http.Header)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}
