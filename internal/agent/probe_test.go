package agent

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestResolveTargetIPLiteralPassthrough(t *testing.T) {
	for _, tc := range []struct{ host string; family int }{
		{"1.1.1.1", 4},
		{"2400:3200::1", 6},
	} {
		got, err := resolveTarget(tc.host, tc.family)
		if err != nil {
			t.Fatalf("resolveTarget(%q, %d) error: %v", tc.host, tc.family, err)
		}
		if got != tc.host {
			t.Fatalf("resolveTarget(%q, %d) = %q, want literal passthrough", tc.host, tc.family, got)
		}
	}
}

func TestResolveTargetLocalhost(t *testing.T) {
	got, err := resolveTarget("localhost", 4)
	if err != nil {
		t.Fatalf("resolveTarget(localhost, 4) error: %v", err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("resolveTarget(localhost, 4) = %q, want 127.0.0.1", got)
	}
	got, err = resolveTarget("localhost", 6)
	if err != nil {
		t.Fatalf("resolveTarget(localhost, 6) error: %v", err)
	}
	if got != "::1" {
		t.Fatalf("resolveTarget(localhost, 6) = %q, want ::1", got)
	}
}

func TestResolveTargetBadDomain(t *testing.T) {
	// Some environments run a wildcard resolver that answers every name
	// (the sandbox resolver returns a synthetic 198.18.x.x address even
	// for .invalid), so force DNS failure with a dead dialer: an
	// unresolvable domain must surface resolveTarget's error either way.
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no dns in test")
		},
	}
	defer func() { net.DefaultResolver = old }()
	if _, err := resolveTarget("invalid.invalid", 4); err == nil {
		t.Fatal("resolveTarget(invalid.invalid, 4) succeeded, want error")
	}
}
