package agent

import (
	"os"
	"testing"
)

func TestInitKindFromProbe(t *testing.T) {
	cases := []struct {
		name  string
		probe initProbe
		want  initKind
	}{
		{"systemd runtime dir", initProbe{hasRunSystemd: true, pid1: "systemd"}, initSystemd},
		{"systemd pid1 only", initProbe{pid1: "systemd"}, initSystemd},
		{"alpine openrc", initProbe{pid1: "init", hasRCService: true, hasOpenRCRun: true, hasInitD: true}, initOpenRC},
		{"openrc via rc-service", initProbe{pid1: "init", hasRCService: true}, initOpenRC},
		{"openrc via openrc-run", initProbe{pid1: "init", hasOpenRCRun: true}, initOpenRC},
		{"openrc via init+init.d", initProbe{pid1: "init", hasInitD: true}, initOpenRC},
		{"init without rc tools or init.d", initProbe{pid1: "init"}, initNone},
		{"bare container", initProbe{pid1: "vps-agent"}, initNone},
		{"empty probe", initProbe{}, initNone},
		{"systemd wins over rc leftovers", initProbe{hasRunSystemd: true, hasRCService: true, pid1: "systemd"}, initSystemd},
	}
	for _, c := range cases {
		if got := initKindFromProbe(c.probe); got != c.want {
			t.Errorf("%s: initKindFromProbe(%+v) = %q, want %q", c.name, c.probe, got, c.want)
		}
	}
}

func TestParseOSReleaseID(t *testing.T) {
	alpine := "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.21.0\nPRETTY_NAME=\"Alpine Linux v3.21\"\n"
	if got := parseOSReleaseID([]byte(alpine)); got != "alpine" {
		t.Errorf("alpine os-release: got %q", got)
	}
	debian := "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
	if got := parseOSReleaseID([]byte(debian)); got != "debian" {
		t.Errorf("debian os-release: got %q", got)
	}
	quoted := "ID=\"alpine\"\n"
	if got := parseOSReleaseID([]byte(quoted)); got != "alpine" {
		t.Errorf("quoted alpine ID: got %q", got)
	}
	if got := parseOSReleaseID([]byte("NAME=Nothing\n")); got != "" {
		t.Errorf("missing ID: got %q", got)
	}
}

func TestIsAlpineOnThisHost(t *testing.T) {
	// The test host is not expected to be Alpine; just make sure the call
	// is consistent with a manual os-release check.
	got := isAlpine()
	if b, err := readOSReleaseForTest(); err == nil && parseOSReleaseID(b) == "alpine" && !got {
		t.Error("os-release says alpine but isAlpine() = false")
	}
}

func readOSReleaseForTest() ([]byte, error) {
	return os.ReadFile("/etc/os-release")
}
