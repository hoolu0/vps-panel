package agent

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// defaultProbePort is used when a probe target carries no explicit port.
const defaultProbePort = 80

// parseTarget splits a configured probe target into the IP to dial and
// the TCP port. The target may be "host", "host:port" or "[v6]:port";
// host is an IP literal or a domain name resolved within the requested
// family. A missing port defaults to 80. Bare IPv6 literals contain too
// many colons for SplitHostPort and fall through as port-less hosts.
func parseTarget(target string, family int) (ip string, port int, err error) {
	target = strings.TrimSpace(target)
	host := target
	port = defaultProbePort
	if h, p, serr := net.SplitHostPort(target); serr == nil {
		n, cerr := strconv.Atoi(p)
		if cerr != nil || n < 1 || n > 65535 {
			return "", 0, fmt.Errorf("目标 %q 的端口 %q 不合法", target, p)
		}
		host, port = h, n
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// "[v6]" without a port: brackets are just grouping — strip
		// them and keep the default port.
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return "", 0, fmt.Errorf("目标 %q 缺少主机部分", target)
	}
	ip, err = resolveHost(host, family)
	if err != nil {
		return "", 0, err
	}
	return ip, port, nil
}

// resolveHost turns a host name — an IP literal or a domain name — into
// an IP string of the requested family. IP literals pass through
// unchanged; domain names are resolved with a 3s timeout and the first
// address of the requested family wins.
func resolveHost(host string, family int) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	network := "ip4"
	if family == 6 {
		network = "ip6"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("域名 %s 无 IPv%d 记录或解析失败", host, family)
	}
	return ips[0].String(), nil
}

// ProbeTarget measures TCP connect latency to one target: 3 connect
// attempts (2s timeout each), AvgMs is the mean connect time of the
// successful attempts and LossPct the failed share. The target may be
// an IP literal or a domain name, with an optional :port (default 80);
// the result keeps the configured string in IP (so the panel displays
// and keys on what the user entered), while the connects run against
// the resolved address.
func ProbeTarget(t proto.ProbeTarget) proto.ProbeResult {
	res := proto.ProbeResult{Name: t.Name, Carrier: t.Carrier, IP: t.IP, Family: t.Family, AvgMs: -1}
	ip, port, err := parseTarget(t.IP, t.Family)
	if err != nil {
		res.Error = err.Error()
		res.LossPct = 100
		return res
	}
	avg, loss, err := tcpProbe(ip, port, t.Family, 3)
	if err != nil {
		res.Error = err.Error()
		res.LossPct = 100
		return res
	}
	res.AvgMs = avg
	res.LossPct = loss
	return res
}

// RunProbes probes all targets sequentially and returns the report.
func RunProbes(targets []proto.ProbeTarget) proto.ProbeReport {
	rep := proto.ProbeReport{At: time.Now()}
	for _, t := range targets {
		rep.Results = append(rep.Results, ProbeTarget(t))
	}
	return rep
}

// tcpProbe runs count TCP connects against ip:port and reports the mean
// connect time of the successful ones plus the loss percentage. A
// successful connect is closed immediately without sending any data.
// It returns an error only when every attempt failed.
func tcpProbe(ip string, port, family, count int) (avgMs, lossPct float64, err error) {
	network := "tcp4"
	if family == 6 {
		network = "tcp6"
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	d := &net.Dialer{Timeout: 2 * time.Second}
	var total float64
	succeeded := 0
	var lastErr error
	for i := 0; i < count; i++ {
		start := time.Now()
		conn, derr := d.DialContext(context.Background(), network, addr)
		if derr != nil {
			lastErr = derr
			continue
		}
		total += float64(time.Since(start).Microseconds()) / 1000.0
		succeeded++
		_ = conn.Close()
	}
	if succeeded == 0 {
		return -1, 100, fmt.Errorf("TCP 连接失败：%v", lastErr)
	}
	avg := math.Round(total/float64(succeeded)*10) / 10
	loss := math.Round(float64(count-succeeded)/float64(count)*1000) / 10
	return avg, loss, nil
}
