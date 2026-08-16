package news

import (
	"context"
	"net"
	"testing"
)

type staticResolver map[string][]net.IPAddr

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r[host], nil
}

func TestURLGuardRejectsPrivateAndMixedDNS(t *testing.T) {
	guard := URLGuard{Resolver: staticResolver{
		"private.example": {{IP: net.ParseIP("10.0.0.2")}},
		"mixed.example": {
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("127.0.0.1")},
		},
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}

	for _, raw := range []string{
		"http://localhost/feed", "http://private.example/feed",
		"https://mixed.example/feed", "file:///etc/passwd",
		"https://user:pass@public.example/feed", "https://public.example:8080/feed",
	} {
		if err := guard.Validate(context.Background(), raw); err == nil {
			t.Errorf("Validate(%q) unexpectedly succeeded", raw)
		}
	}
	if err := guard.Validate(context.Background(), "https://public.example/feed"); err != nil {
		t.Fatalf("public feed rejected: %v", err)
	}
}
