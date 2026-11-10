package agent

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// ProbeTarget runs 3 ICMP echo probes against one target. It uses a raw
// ICMP socket when permitted and falls back to the system ping binary.
func ProbeTarget(t proto.ProbeTarget) proto.ProbeResult {
	res := proto.ProbeResult{Name: t.Name, Carrier: t.Carrier, IP: t.IP, Family: t.Family, AvgMs: -1}
	avg, loss, err := icmpPing(t.IP, t.Family, 3)
	if err != nil {
		avg, loss, err = execPing(t.IP, t.Family, 3)
	}
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

func icmpPing(ipStr string, family, count int) (avgMs, lossPct float64, err error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0, 100, fmt.Errorf("bad ip %q", ipStr)
	}
	network, protoNum := "ip4:icmp", 1
	if family == 6 {
		network, protoNum = "ip6:ipv6-icmp", 58
	}
	conn, err := icmp.ListenPacket(network, "")
	if err != nil {
		return 0, 100, err
	}
	defer conn.Close()

	var total float64
	received := 0
	id := os.Getpid() & 0xffff
	for seq := 1; seq <= count; seq++ {
		var msg icmp.Message
		if family == 6 {
			msg = icmp.Message{Type: ipv6.ICMPTypeEchoRequest, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("vps-panel")}}
		} else {
			msg = icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("vps-panel")}}
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			return 0, 100, err
		}
		start := time.Now()
		if _, err := conn.WriteTo(b, &net.IPAddr{IP: ip}); err != nil {
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1500)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				break
			}
			reply, err := icmp.ParseMessage(protoNum, buf[:n])
			if err != nil {
				continue
			}
			if echo, ok := reply.Body.(*icmp.Echo); ok && echo.Seq == seq &&
				(reply.Type == ipv4.ICMPTypeEchoReply || reply.Type == ipv6.ICMPTypeEchoReply) {
				total += float64(time.Since(start).Microseconds()) / 1000.0
				received++
				break
			}
		}
	}
	if received == 0 {
		return 0, 100, fmt.Errorf("no reply from %s", ipStr)
	}
	loss := float64(count-received) / float64(count) * 100
	return total / float64(received), loss, nil
}

var rttRe = regexp.MustCompile(`rtt min/avg/max[^=]*= [0-9.]+/([0-9.]+)/`)
var lossRe = regexp.MustCompile(`([0-9.]+)% packet loss`)

func execPing(ipStr string, family, count int) (avgMs, lossPct float64, err error) {
	pingBin, err := exec.LookPath("ping")
	if err != nil {
		return 0, 100, fmt.Errorf("icmp socket unavailable and no ping binary")
	}
	args := []string{"-n", "-c", strconv.Itoa(count), "-W", "2", "-i", "0.2"}
	if family == 6 {
		args = append(args, "-6")
	}
	args = append(args, ipStr)
	out, _ := exec.Command(pingBin, args...).CombinedOutput()
	s := string(out)
	if m := rttRe.FindStringSubmatch(s); m != nil {
		avgMs, _ = strconv.ParseFloat(m[1], 64)
		if m2 := lossRe.FindStringSubmatch(s); m2 != nil {
			lossPct, _ = strconv.ParseFloat(m2[1], 64)
		}
		return avgMs, lossPct, nil
	}
	if strings.Contains(s, "Operation not permitted") {
		return 0, 100, fmt.Errorf("ping 需要 CAP_NET_RAW 权限")
	}
	if m := lossRe.FindStringSubmatch(s); m != nil {
		lossPct, _ = strconv.ParseFloat(m[1], 64)
		if lossPct >= 100 {
			return 0, 100, fmt.Errorf("no reply from %s", ipStr)
		}
	}
	return 0, 100, fmt.Errorf("no reply from %s", ipStr)
}
