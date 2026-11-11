package agent

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// initKind identifies the init/service manager of the host the agent runs
// on. The panel has an equivalent remote detection for onboarding; the
// rules are kept in sync by hand.
type initKind string

const (
	initSystemd initKind = "systemd"
	initOpenRC  initKind = "openrc"
	initNone    initKind = "none"
)

// forcedInit returns the VPS_FORCE_INIT override when it names a known
// init system (used for testing and exotic hosts).
func forcedInit() (initKind, bool) {
	switch initKind(os.Getenv("VPS_FORCE_INIT")) {
	case initSystemd, initOpenRC, initNone:
		return initKind(os.Getenv("VPS_FORCE_INIT")), true
	}
	return "", false
}

// initProbe carries the raw observations initKindFromProbe reasons about,
// split out so the decision logic is unit-testable.
type initProbe struct {
	hasRunSystemd bool   // /run/systemd/system exists
	pid1          string // comm of PID 1
	hasRCService  bool   // rc-service on PATH
	hasOpenRCRun  bool   // /sbin/openrc-run exists
	hasInitD      bool   // /etc/init.d exists
}

func initKindFromProbe(p initProbe) initKind {
	if p.hasRunSystemd || p.pid1 == "systemd" {
		return initSystemd
	}
	if p.hasRCService || p.hasOpenRCRun || (p.pid1 == "init" && p.hasInitD) {
		return initOpenRC
	}
	return initNone
}

func detectInit() initKind {
	if k, ok := forcedInit(); ok {
		return k
	}
	var p initProbe
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		p.hasRunSystemd = true
	}
	if out, err := exec.Command("ps", "-p", "1", "-o", "comm=").Output(); err == nil {
		p.pid1 = strings.TrimSpace(string(out))
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		p.hasRCService = true
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		p.hasOpenRCRun = true
	}
	if _, err := os.Stat("/etc/init.d"); err == nil {
		p.hasInitD = true
	}
	return initKindFromProbe(p)
}

var (
	initOnce   sync.Once
	initCached initKind
)

// currentInit detects the host init system once per process.
func currentInit() initKind {
	initOnce.Do(func() { initCached = detectInit() })
	return initCached
}

// parseOSReleaseID extracts the ID= value from os-release content.
func parseOSReleaseID(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ID=") {
			return strings.Trim(strings.TrimPrefix(line, "ID="), `"'`)
		}
	}
	return ""
}

// isAlpine reports whether the host is Alpine Linux.
func isAlpine() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	return parseOSReleaseID(b) == "alpine"
}
