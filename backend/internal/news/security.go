package news

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IPResolver is injectable so SSRF rules are deterministic in tests.
type IPResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// URLGuard validates initial and redirected feed URLs and pins outbound dials
// to an address that passed the same public-IP check.
type URLGuard struct {
	Resolver     IPResolver
	AllowPrivate bool
}

func (g URLGuard) resolver() IPResolver {
	if g.Resolver != nil {
		return g.Resolver
	}
	return net.DefaultResolver
}

// Validate permits only HTTP(S), rejects credentials, suspicious ports and
// every private/local/special-use address unless explicitly allowed for a
// trusted development environment.
func (g URLGuard) Validate(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse feed url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported feed scheme %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("feed url credentials are not allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return errors.New("feed url host is required")
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" && !g.AllowPrivate {
		return fmt.Errorf("feed port %q is not allowed", port)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if !g.AllowPrivate {
			return errors.New("local feed host is not allowed")
		}
		return nil
	}

	addresses, err := g.resolver().LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve feed host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("feed host resolved to no addresses")
	}
	if g.AllowPrivate {
		return nil
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return fmt.Errorf("feed host resolves to non-public address %s", address.IP)
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() && !ip.IsMulticast()
}

// NewSafeHTTPClient enforces the URL guard on the first request, redirects and
// the actual dial, protecting against simple DNS-rebinding races.
func NewSafeHTTPClient(timeout time.Duration, guard URLGuard) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split outbound address: %w", err)
		}
		addresses, err := guard.resolver().LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve outbound host: %w", err)
		}
		for _, candidate := range addresses {
			if guard.AllowPrivate || isPublicIP(candidate.IP) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
		}
		return nil, errors.New("outbound host has no allowed addresses")
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many feed redirects")
			}
			return guard.Validate(req.Context(), req.URL.String())
		},
	}
}
