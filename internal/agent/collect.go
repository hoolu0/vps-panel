package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// Collector gathers host status; it keeps previous CPU/net samples to
// compute rates.
type Collector struct {
	prevCPUTotal, prevCPUIdle uint64
	prevNetIn, prevNetOut     uint64
	prevSampleAt              time.Time
}

func readProcStat() (total, idle uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 5 && fields[0] == "cpu" {
			var sum uint64
			for _, s := range fields[1:] {
				v, _ := strconv.ParseUint(s, 10, 64)
				sum += v
			}
			idleV, _ := strconv.ParseUint(fields[4], 10, 64)
			iowait := uint64(0)
			if len(fields) >= 6 {
				iowait, _ = strconv.ParseUint(fields[5], 10, 64)
			}
			return sum, idleV + iowait
		}
	}
	return 0, 0
}

func memInfo() (memTotal, memAvail, swapTotal, swapFree uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		v *= 1024
		switch fields[0] {
		case "MemTotal:":
			memTotal = v
		case "MemAvailable:":
			memAvail = v
		case "SwapTotal:":
			swapTotal = v
		case "SwapFree:":
			swapFree = v
		}
	}
	return
}

func loadAvg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 3 {
		l1, _ = strconv.ParseFloat(fields[0], 64)
		l5, _ = strconv.ParseFloat(fields[1], 64)
		l15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

func uptimeSec() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 1 {
		f, _ := strconv.ParseFloat(fields[0], 64)
		return int64(f)
	}
	return 0
}

func netTotals() (in, out uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:idx])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) >= 9 {
			rx, _ := strconv.ParseUint(fields[0], 10, 64)
			tx, _ := strconv.ParseUint(fields[8], 10, 64)
			in += rx
			out += tx
		}
	}
	return
}

// PrettyOS returns the OS pretty name from /etc/os-release.
func PrettyOS() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

// KernelRelease returns the running kernel version.
func KernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// Collect takes one status sample.
func (c *Collector) Collect() proto.Status {
	now := time.Now()
	total, idle := readProcStat()
	var cpuPct float64
	if c.prevCPUTotal > 0 && total > c.prevCPUTotal {
		dTotal := float64(total - c.prevCPUTotal)
		dIdle := float64(idle - c.prevCPUIdle)
		if dTotal > 0 {
			cpuPct = (dTotal - dIdle) / dTotal * 100
		}
	}
	c.prevCPUTotal, c.prevCPUIdle = total, idle

	memTotal, memAvail, swapTotal, swapFree := memInfo()
	l1, l5, l15 := loadAvg()

	var diskUsed, diskTotal uint64
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		diskTotal = fs.Blocks * uint64(fs.Bsize)
		free := fs.Bavail * uint64(fs.Bsize)
		if diskTotal >= free {
			diskUsed = diskTotal - free
		}
	}

	netIn, netOut := netTotals()
	var inSpeed, outSpeed uint64
	if !c.prevSampleAt.IsZero() {
		dt := now.Sub(c.prevSampleAt).Seconds()
		if dt > 0 {
			if netIn >= c.prevNetIn {
				inSpeed = uint64(float64(netIn-c.prevNetIn) / dt)
			}
			if netOut >= c.prevNetOut {
				outSpeed = uint64(float64(netOut-c.prevNetOut) / dt)
			}
		}
	}
	c.prevNetIn, c.prevNetOut, c.prevSampleAt = netIn, netOut, now

	hostname, _ := os.Hostname()
	return proto.Status{
		At:          now,
		UptimeSec:   uptimeSec(),
		Load1:       l1,
		Load5:       l5,
		Load15:      l15,
		CPUPercent:  cpuPct,
		MemUsed:     memTotal - memAvail,
		MemTotal:    memTotal,
		SwapUsed:    swapTotal - swapFree,
		SwapTotal:   swapTotal,
		DiskUsed:    diskUsed,
		DiskTotal:   diskTotal,
		NetInSpeed:  inSpeed,
		NetOutSpeed: outSpeed,
		NetInTotal:  netIn,
		NetOutTotal: netOut,
		Hostname:    hostname,
		OS:          PrettyOS(),
		Kernel:      KernelRelease(),
		Arch:        runtime.GOARCH,
	}
}
