package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// Deleting every probe target must persist as an empty list: the store
// file has to contain [] (not null), otherwise OpenStore would treat the
// value as "never configured" and resurrect the default targets.
func TestProbeTargetsDeletedAllPersists(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Data.ProbeTargets) == 0 {
		t.Fatal("fresh store should start with the default targets")
	}
	if err := s.Update(func(d *Data) {
		d.ProbeTargets = []proto.ProbeTarget{}
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"probe_targets": []`) {
		t.Fatalf("store file should persist an empty array, got:\n%s", raw)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Data.ProbeTargets) != 0 {
		t.Fatalf("deleted-all targets resurrected after reopen: %d targets", len(s2.Data.ProbeTargets))
	}
}

func TestProbeTargetSetsEqual(t *testing.T) {
	a := []proto.ProbeTarget{
		{Name: "北京电信", Carrier: "CT", IP: "219.141.136.10", Family: 4},
		{Name: "阿里DNSv6", Carrier: "OTHER", IP: "2400:3200::1", Family: 6},
	}
	renamed := []proto.ProbeTarget{
		{Name: "改名了", Carrier: "CM", IP: "219.141.136.10", Family: 4},
		{Name: "另一个名字", Carrier: "CT", IP: "2400:3200::1", Family: 6},
	}
	if !probeTargetSetsEqual(a, renamed) {
		t.Fatal("rename/relabel should not count as a set change")
	}
	removed := a[:1]
	if probeTargetSetsEqual(a, removed) {
		t.Fatal("dropping a target must count as a set change")
	}
	dup := []proto.ProbeTarget{a[0], a[0]}
	if probeTargetSetsEqual(a, dup) {
		t.Fatal("duplicates must not masquerade as the same set")
	}
}

func TestTrimNodeProbes(t *testing.T) {
	d := &Data{Nodes: []*Node{
		{ID: "n1", Probes: &proto.ProbeReport{At: time.Now(), Results: []proto.ProbeResult{
			{Name: "A", IP: "1.1.1.1", Family: 4, AvgMs: 10},
			{Name: "B", IP: "8.8.8.8", Family: 4, AvgMs: 20},
			{Name: "B6", IP: "8.8.8.8", Family: 6, AvgMs: 30},
		}}},
		{ID: "n2"}, // no report yet: must be left alone
	}}
	keep := probeTargetKeySet([]proto.ProbeTarget{{IP: "8.8.8.8", Family: 4}})
	trimNodeProbes(d, keep)
	got := d.Nodes[0].Probes.Results
	if len(got) != 1 || got[0].IP != "8.8.8.8" || got[0].Family != 4 {
		t.Fatalf("trim kept %+v, want only the 8.8.8.8/IPv4 result", got)
	}
	// Trimming to the empty set leaves an empty (non-nil) result list.
	trimNodeProbes(d, probeTargetKeySet(nil))
	if d.Nodes[0].Probes.Results == nil || len(d.Nodes[0].Probes.Results) != 0 {
		t.Fatalf("trim to empty set: results = %+v, want empty non-nil", d.Nodes[0].Probes.Results)
	}
}
