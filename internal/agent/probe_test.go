package agent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/hoolu0/vps-panel/internal/proto"
)

func TestParseTargetIPLiteralPassthrough(t *testing.T) {
	for _, tc := range []struct {
		target string
		family int
	}{
		{"1.1.1.1", 4},
		{"2400:3200::1", 6},
	} {
		ip, port, err := parseTarget(tc.target, tc.family)
		if err != nil {
			t.Fatalf("parseTarget(%q, %d) error: %v", tc.target, tc.family, err)
		}
		if ip != tc.target {
			t.Fatalf("parseTarget(%q, %d) ip = %q, want literal passthrough", tc.target, tc.family, ip)
		}
		if port != defaultProbePort {
			t.Fatalf("parseTarget(%q, %d) port = %d, want default %d", tc.target, tc.family, port, defaultProbePort)
		}
	}
}

func TestParseTargetExplicitPort(t *testing.T) {
	for _, tc := range []struct {
		target string
		family int
		ip     string
		port   int
	}{
		{"1.1.1.1:8443", 4, "1.1.1.1", 8443},
		{"[2400:3200::1]:443", 6, "2400:3200::1", 443},
		{"localhost:8080", 4, "127.0.0.1", 8080},
		{"localhost", 6, "::1", defaultProbePort},
		{"[::1]", 6, "::1", defaultProbePort},
	} {
		ip, port, err := parseTarget(tc.target, tc.family)
		if err != nil {
			t.Fatalf("parseTarget(%q, %d) error: %v", tc.target, tc.family, err)
		}
		if ip != tc.ip || port != tc.port {
			t.Fatalf("parseTarget(%q, %d) = (%q, %d), want (%q, %d)", tc.target, tc.family, ip, port, tc.ip, tc.port)
		}
	}
}

func TestParseTargetBadPort(t *testing.T) {
	for _, target := range []string{"localhost:abc", "localhost:0", "localhost:65536", "localhost:"} {
		if _, _, err := parseTarget(target, 4); err == nil {
			t.Fatalf("parseTarget(%q, 4) succeeded, want bad-port error", target)
		}
	}
}

func TestParseTargetBadDomain(t *testing.T) {
	// Some environments run a wildcard resolver that answers every name
	// (the sandbox resolver returns a synthetic 198.18.x.x address even
	// for .invalid), so force DNS failure with a dead dialer: an
	// unresolvable domain must surface parseTarget's error either way.
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no dns in test")
		},
	}
	defer func() { net.DefaultResolver = old }()
	for _, target := range []string{"invalid.invalid", "invalid.invalid:80"} {
		if _, _, err := parseTarget(target, 4); err == nil {
			t.Fatalf("parseTarget(%q, 4) succeeded, want error", target)
		}
	}
}

func TestProbeTargetTCPConnectOK(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	res := ProbeTarget(proto.ProbeTarget{Name: "local", IP: "127.0.0.1:" + strconv.Itoa(port), Family: 4})
	if res.Error != "" {
		t.Fatalf("ProbeTarget error: %s", res.Error)
	}
	if res.AvgMs < 0 || res.LossPct != 0 {
		t.Fatalf("ProbeTarget = avg %v loss %v, want avg >= 0 and loss 0", res.AvgMs, res.LossPct)
	}
	if res.IP != "127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("ProbeTarget IP = %q, want configured string kept verbatim", res.IP)
	}
}

func TestProbeTargetTCPRefused(t *testing.T) {
	// Reserve a port and release it so connects are refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	res := ProbeTarget(proto.ProbeTarget{Name: "closed", IP: "127.0.0.1:" + strconv.Itoa(port), Family: 4})
	if res.AvgMs != -1 || res.LossPct != 100 {
		t.Fatalf("ProbeTarget = avg %v loss %v, want -1 and 100", res.AvgMs, res.LossPct)
	}
	if !strings.Contains(res.Error, "TCP 连接失败") {
		t.Fatalf("ProbeTarget Error = %q, want TCP connect failure", res.Error)
	}
}
