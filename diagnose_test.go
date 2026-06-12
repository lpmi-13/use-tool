package main

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func snapWith(values map[string]Value) Snapshot {
	return Snapshot{Values: values}
}

func findObservationByName(obs []Observation, name string) (Observation, bool) {
	for _, o := range obs {
		if o.Name == name {
			return o, true
		}
	}
	return Observation{}, false
}

func observationNames(obs []Observation) []string {
	names := make([]string, len(obs))
	for i, o := range obs {
		names[i] = o.Name
	}
	return names
}

func TestCPUVerdicts(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	cases := []struct {
		name string
		got  Signal
		want Signal
	}{
		{"load over NumCPU", verdictLoad1min(si, Value{Number: 5}, Snapshot{}), SignalHigh},
		{"load near NumCPU", verdictLoad1min(si, Value{Number: 3}, Snapshot{}), SignalModerate},
		{"load well under", verdictLoad1min(si, Value{Number: 1}, Snapshot{}), SignalLow},
		{"idle low = busy", verdictIdleMean(si, Value{Number: 10}, Snapshot{}), SignalHigh},
		{"idle mid", verdictIdleMean(si, Value{Number: 50}, Snapshot{}), SignalModerate},
		{"idle high = quiet", verdictIdleMean(si, Value{Number: 95}, Snapshot{}), SignalLow},
		{"per-cpu idle range has hot CPU", verdictIdleRange(si, Value{Samples: []float64{0, 95}}, Snapshot{}), SignalHigh},
		{"per-cpu idle range all quiet", verdictIdleRange(si, Value{Samples: []float64{85, 95}}, Snapshot{}), SignalLow},
		{"runq over NumCPU", verdictRunQueue(si, Value{Samples: []float64{1, 2, 7}}, Snapshot{}), SignalHigh},
		{"runq under NumCPU", verdictRunQueue(si, Value{Samples: []float64{0, 1, 2}}, Snapshot{}), SignalLow},
		{"steal present", verdictSteal(si, Value{Samples: []float64{0, 3}}, Snapshot{}), SignalHigh},
		{"steal absent", verdictSteal(si, Value{Samples: []float64{0, 0}}, Snapshot{}), SignalLow},
		{"errors present", verdictDmesgErrors(si, Value{Number: 2}, Snapshot{}), SignalHigh},
		{"errors absent", verdictDmesgErrors(si, Value{Number: 0}, Snapshot{}), SignalLow},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestGradeDimensionWellSupportedSaturation(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_r":  {Samples: []float64{2, 5, 7}}, // max 7 > 4 -> present
		"vmstat_st": {Samples: []float64{0, 2}},    // steal > 0 -> present
	})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"vmstat_r", "vmstat_st"})
	if g.Supports != 2 || g.Contradicts != 0 {
		t.Fatalf("supports=%d contradicts=%d, want 2/0", g.Supports, g.Contradicts)
	}
	if !g.Accurate {
		t.Errorf("claim should match data (both signals read present)")
	}
	if g.assessment() != "well supported" {
		t.Errorf("assessment = %q, want well supported", g.assessment())
	}
}

func TestGradeDimensionCleanBillOfHealth(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_r":  {Samples: []float64{0, 1, 1}}, // under NumCPU -> absent
		"vmstat_st": {Samples: []float64{0, 0}},    // no steal -> absent
	})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "absent", []string{"vmstat_r", "vmstat_st"})
	if g.Supports != 2 || g.Contradicts != 0 {
		t.Fatalf("supports=%d contradicts=%d, want 2/0", g.Supports, g.Contradicts)
	}
	if !g.Accurate {
		t.Errorf("an evidenced 'absent' on an idle box should be accurate")
	}
}

func TestGradeDimensionContradictingEvidence(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_r": {Samples: []float64{0, 1}}, // reads absent
	})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"vmstat_r"})
	if g.Supports != 0 || g.Contradicts != 1 {
		t.Fatalf("supports=%d contradicts=%d, want 0/1", g.Supports, g.Contradicts)
	}
	if g.Accurate {
		t.Errorf("claiming 'present' when run-queue reads absent should not be accurate")
	}
	if len(g.Cited) != 1 || g.Cited[0].Verdict != citeContradicts {
		t.Errorf("expected one contradicting citation, got %+v", g.Cited)
	}
}

func TestGradeDimensionWrongDimensionCitation(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_r":     {Samples: []float64{2, 7}}, // saturation present (gives HasData)
		"loadavg_1min": {Number: 5},                // utilization signal
	})
	// Claim saturation, but cite a utilization signal as evidence.
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"loadavg_1min"})
	if len(g.Cited) != 1 || g.Cited[0].Verdict != citeWrongDimension {
		t.Fatalf("expected wrong-dimension citation, got %+v", g.Cited)
	}
	if g.Supports != 0 {
		t.Errorf("a wrong-dimension citation must not count as support")
	}
	if g.assessment() != "no valid evidence cited" {
		t.Errorf("assessment = %q", g.assessment())
	}
}

func TestGradeDimensionNotEnoughData(t *testing.T) {
	si := SystemInfo{NumCPU: 4}

	// No errors signal captured -> "not enough data" is the accurate call.
	empty := snapWith(map[string]Value{"vmstat_r": {Samples: []float64{1}}})
	g := gradeDimension(si, empty, cpuObservations, "Errors", "", nil)
	if g.HasData || !g.Accurate {
		t.Errorf("errors not captured: HasData=%v Accurate=%v, want false/true", g.HasData, g.Accurate)
	}

	// Errors signal present -> "not enough data" is wrong.
	withErr := snapWith(map[string]Value{"dmesg_cpu_keywords": {Number: 0}})
	g = gradeDimension(si, withErr, cpuObservations, "Errors", "", nil)
	if !g.HasData || g.Accurate {
		t.Errorf("errors captured: HasData=%v Accurate=%v, want true/false", g.HasData, g.Accurate)
	}
}

func TestGradeDimensionThinSupport(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{"vmstat_r": {Samples: []float64{2, 7}}})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"vmstat_r"})
	if g.Supports != 1 || g.Contradicts != 0 {
		t.Fatalf("supports=%d contradicts=%d, want 1/0", g.Supports, g.Contradicts)
	}
	if got := g.assessment(); got == "well supported" {
		t.Errorf("single-signal claim should be flagged as thin, got %q", got)
	}
}

func TestGradeDimensionTracksUncitedSupportingEvidence(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_r":  {Samples: []float64{0, 1}},
		"vmstat_wa": {Samples: []float64{0, 0}},
		"vmstat_st": {Samples: []float64{0, 0}},
	})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "absent", []string{"vmstat_r"})
	if g.Supports != 1 {
		t.Fatalf("supports=%d, want 1", g.Supports)
	}
	if supportingUncitedCount(g) != 2 {
		t.Fatalf("supporting uncited = %d, want 2; uncited=%+v", supportingUncitedCount(g), g.Uncited)
	}
}

func TestMemoryVerdicts(t *testing.T) {
	si := SystemInfo{}
	cases := []struct {
		name string
		got  Signal
		want Signal
	}{
		{"avail very low", verdictMemAvailable(si, Value{Number: 0.2}, Snapshot{}), SignalHigh},
		{"avail mid", verdictMemAvailable(si, Value{Number: 1.5}, Snapshot{}), SignalModerate},
		{"avail plenty", verdictMemAvailable(si, Value{Number: 8}, Snapshot{}), SignalLow},
		{"swap heavy", verdictSwapUsed(si, Value{Number: 80}, Snapshot{}), SignalHigh},
		{"swap touched", verdictSwapUsed(si, Value{Number: 10}, Snapshot{}), SignalModerate},
		{"swap quiet", verdictSwapUsed(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"swap-in active", verdictSwapActivity(si, Value{Samples: []float64{0, 4}}, Snapshot{}), SignalHigh},
		{"swap-in quiet", verdictSwapActivity(si, Value{Samples: []float64{0, 0}}, Snapshot{}), SignalLow},
		{"psi some heavy", verdictPSISome(si, Value{Number: 15}, Snapshot{}), SignalHigh},
		{"psi some mild", verdictPSISome(si, Value{Number: 2}, Snapshot{}), SignalModerate},
		{"psi some zero", verdictPSISome(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"psi full non-zero", verdictPSIFull(si, Value{Number: 0.5}, Snapshot{}), SignalHigh},
		{"oom present", verdictDmesgOOM(si, Value{Number: 1}, Snapshot{}), SignalHigh},
		{"oom absent", verdictDmesgOOM(si, Value{Number: 0}, Snapshot{}), SignalLow},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestDiskVerdicts(t *testing.T) {
	si := SystemInfo{}
	cases := []struct {
		name string
		got  Signal
		want Signal
	}{
		{"util pegged", verdictDiskUtil(si, Value{Number: 90}, Snapshot{}), SignalHigh},
		{"util mid", verdictDiskUtil(si, Value{Number: 60}, Snapshot{}), SignalModerate},
		{"util quiet", verdictDiskUtil(si, Value{Number: 5}, Snapshot{}), SignalLow},
		{"aqu-sz queueing", verdictAquSz(si, Value{Number: 17}, Snapshot{}), SignalHigh},
		{"aqu-sz idle", verdictAquSz(si, Value{Number: 0.1}, Snapshot{}), SignalLow},
		{"await slow", verdictAwait(si, Value{Number: 50}, Snapshot{}), SignalHigh},
		{"await borderline", verdictAwait(si, Value{Number: 12}, Snapshot{}), SignalModerate},
		{"await fast", verdictAwait(si, Value{Number: 1}, Snapshot{}), SignalLow},
		{"io errors", verdictDmesgIOErrors(si, Value{Number: 3}, Snapshot{}), SignalHigh},
		{"no io errors", verdictDmesgIOErrors(si, Value{Number: 0}, Snapshot{}), SignalLow},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestNetworkVerdicts(t *testing.T) {
	si := SystemInfo{}
	cases := []struct {
		name string
		got  Signal
		want Signal
	}{
		{"ifutil high", verdictNetIfutil(si, Value{Number: 90}, Snapshot{}), SignalHigh},
		{"ifutil moderate", verdictNetIfutil(si, Value{Number: 60}, Snapshot{}), SignalModerate},
		{"ifutil low", verdictNetIfutil(si, Value{Number: 0.42}, Snapshot{}), SignalLow},
		{"positive high", verdictPositiveIsHigh(si, Value{Number: 1}, Snapshot{}), SignalHigh},
		{"zero low", verdictPositiveIsHigh(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"drops present", verdictNetDrops(si, Value{Number: 5}, Snapshot{}), SignalHigh},
		{"drops absent", verdictNetDrops(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"retrans high", verdictRetransRatio(si, Value{Number: 1.2}, Snapshot{}), SignalHigh},
		{"retrans some", verdictRetransRatio(si, Value{Number: 0.1}, Snapshot{}), SignalModerate},
		{"retrans clean", verdictRetransRatio(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"overflows seen", verdictListenOverflows(si, Value{Number: 12}, Snapshot{}), SignalHigh},
		{"overflows none", verdictListenOverflows(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"iface errors", verdictNetIfaceErrors(si, Value{Number: 4}, Snapshot{}), SignalHigh},
		{"iface clean", verdictNetIfaceErrors(si, Value{Number: 0}, Snapshot{}), SignalLow},
		{"dmesg net events", verdictDmesgNet(si, Value{Number: 2}, Snapshot{}), SignalHigh},
		{"dmesg net quiet", verdictDmesgNet(si, Value{Number: 0}, Snapshot{}), SignalLow},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestGradeMemoryCleanBillOfHealth(t *testing.T) {
	si := SystemInfo{}
	snap := snapWith(map[string]Value{
		"vmstat_si":          {Samples: []float64{0, 0}},
		"vmstat_so":          {Samples: []float64{0, 0}},
		"psi_mem_some_avg10": {Number: 0},
	})
	g := gradeDimension(si, snap, memoryObservations, "Saturation", "absent",
		[]string{"vmstat_si", "vmstat_so", "psi_mem_some_avg10"})
	if g.Supports != 3 || g.Contradicts != 0 {
		t.Fatalf("supports=%d contradicts=%d, want 3/0", g.Supports, g.Contradicts)
	}
	if !g.Accurate {
		t.Errorf("evidenced 'absent' on a quiet memory snapshot should be accurate")
	}
}

func TestGradeDiskBusySaturation(t *testing.T) {
	si := SystemInfo{}
	snap := snapWith(map[string]Value{
		"iostat_max_aqu_sz":   {Number: 17},
		"iostat_max_await_ms": {Number: 35},
	})
	g := gradeDimension(si, snap, diskObservations, "Saturation", "present",
		[]string{"iostat_max_aqu_sz", "iostat_max_await_ms"})
	if g.Supports != 2 || g.Contradicts != 0 {
		t.Fatalf("supports=%d contradicts=%d, want 2/0", g.Supports, g.Contradicts)
	}
	if !g.Accurate || g.assessment() != "well supported" {
		t.Errorf("accurate=%v assessment=%q", g.Accurate, g.assessment())
	}
}

func TestNetworkSsInfoIsDiagnoseCandidate(t *testing.T) {
	snap := snapWith(map[string]Value{
		"tcp_ss_retrans_sockets": {Number: 1, Unit: " sockets"},
		"tcp_ss_sendq_max":       {Number: 131072, Unit: " B"},
		"tcp_ss_limited_pct_max": {Number: 15.2, Unit: "%"},
		"tcp_estab_connections":  {Number: 3},
	})
	byResource := candidatesByResource(networkInvestigation, snap)
	cands := byResource[ResourceNetwork]
	for _, name := range []string{"tcp_ss_retrans_sockets", "tcp_ss_sendq_max", "tcp_ss_limited_pct_max"} {
		obs, ok := findObservationByName(cands, name)
		if !ok {
			t.Fatalf("expected %s candidate, got %v", name, observationNames(cands))
		}
		if obs.Section != "Saturation" || obs.Verdict == nil {
			t.Fatalf("%s candidate not verdict-bearing saturation: %+v", name, obs)
		}
	}
	if _, ok := findObservationByName(cands, "tcp_estab_connections"); ok {
		t.Fatalf("connection count should remain context-only, got %v", observationNames(cands))
	}
}

func TestNetworkIfutilIsDiagnoseCandidate(t *testing.T) {
	snap := snapWith(map[string]Value{
		"net_peak_ifutil_pct": {Number: 0.42, Unit: "%"},
		"net_peak_tx_kbps":    {Number: 5163.18, Unit: " kB/s"},
	})
	byResource := candidatesByResource(networkInvestigation, snap)
	cands := byResource[ResourceNetwork]
	if len(cands) == 0 {
		t.Fatal("expected network candidates")
	}
	util, ok := findObservationByName(cands, "net_peak_ifutil_pct")
	if !ok {
		t.Fatalf("expected ifutil candidate, got %v", observationNames(cands))
	}
	if util.Section != "Utilization" || util.Verdict == nil {
		t.Fatalf("ifutil candidate not verdict-bearing utilization: %+v", util)
	}
	if _, ok := findObservationByName(cands, "net_peak_tx_kbps"); ok {
		t.Fatalf("raw throughput should remain context-only, got %v", observationNames(cands))
	}
}

func TestGradeNoVerdictCitationSurfacesHeuristic(t *testing.T) {
	si := SystemInfo{}
	snap := snapWith(map[string]Value{
		"mem_used_pct":       {Number: 92},
		"psi_mem_some_avg10": {Number: 0},
	})
	// The classic Linux mistake: citing mem_used_pct as a utilization signal.
	// mem_used_pct has no Verdict; the cite must come back as no-signal with
	// the Heuristic explaining why used% isn't pressure on Linux.
	g := gradeDimension(si, snap, memoryObservations, "Utilization", "high",
		[]string{"mem_used_pct"})
	if len(g.Cited) != 1 {
		t.Fatalf("want 1 citation, got %d", len(g.Cited))
	}
	c := g.Cited[0]
	if c.Verdict != citeNoSignal {
		t.Errorf("verdict = %v, want citeNoSignal", c.Verdict)
	}
	if c.Heuristic == "" {
		t.Errorf("expected a teaching heuristic on mem_used_pct, got empty")
	}
}

func TestVmstatExtractorDropsSinceBootFirstRow(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	// Header + 3 data rows, where the since-boot first row is the only one
	// above NumCPU. After dropping it, the verdict should read "absent" —
	// the same scenario that surfaces a citeContradicts result if a learner
	// tries to use the first-row spike as evidence for "saturation present".
	out := "procs -----------memory---------- ---swap-- -----io---- -system-- ------cpu-----\n" +
		" r  b   swpd   free   buff  cache   si   so    bi    bo   in   cs us sy id wa st\n" +
		"99  0      0 100000      0      0    0    0     0     0    0    0  0  0 99  0  0\n" +
		" 1  0      0 100000      0      0    0    0     0     0    0    0  0  0 99  0  0\n" +
		" 0  0      0 100000      0      0    0    0     0     0    0    0  0  0 99  0  0\n"
	caps := []CapturedCommand{{Cmd: "vmstat 1 3", Output: out}}
	v, ok := extractVmstatColumn("r")(si, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Max() != 1 {
		t.Errorf("after dropping since-boot row 99, expected Max=1, got %v", v.Max())
	}
	if verdictRunQueue(si, v, Snapshot{}) != SignalLow {
		t.Errorf("verdict should read SignalLow once since-boot spike is dropped")
	}

	// A learner who cites this vmstat_r for "saturation present" should now
	// hit the citeContradicts path because the verdict reads low.
	snap := snapWith(map[string]Value{"vmstat_r": v})
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"vmstat_r"})
	if g.Contradicts != 1 {
		t.Errorf("citing a vmstat_r whose only non-zero was the dropped since-boot row should contradict 'present', got contradicts=%d", g.Contradicts)
	}
}

func TestNetworkInvestigationCarriesUtilizationNote(t *testing.T) {
	note := networkInvestigation.DiagnoseNotes["Utilization"]
	if note == "" {
		t.Fatal("expected a DiagnoseNotes entry for network Utilization")
	}
	// The text must communicate that the tool can't infer utilization, so
	// that the learner has explicit guidance at the prompt rather than
	// having to infer it from each observation's heuristic.
	// The text must communicate (a) that the tool can't infer utilization
	// and (b) why — the link-speed gap. It's read in two contexts: at the
	// verdict prompt when there are captured signals, and embedded in the
	// auto-skip line when there aren't.
	for _, want := range []string{"link speed", "for context"} {
		if !strings.Contains(note, want) {
			t.Errorf("network utilization note missing %q: %q", want, note)
		}
	}
}

// ----- Slice 2: whole-system & cross-resource -----

func TestObservationsTaggedWithResource(t *testing.T) {
	cases := []struct {
		obs      []Observation
		resource string
	}{
		{cpuObservations, ResourceCPU},
		{memoryObservations, ResourceMemory},
		{diskObservations, ResourceDisk},
		{networkObservations, ResourceNetwork},
	}
	for _, tc := range cases {
		for _, o := range tc.obs {
			if o.Resource != tc.resource {
				t.Errorf("%s: %q Resource = %q, want %q", tc.resource, o.Name, o.Resource, tc.resource)
			}
		}
	}
}

func TestSystemInvestigationRegisteredAndAggregates(t *testing.T) {
	sys, err := getInvestigation("system")
	if err != nil {
		t.Fatalf("system investigation not registered: %v", err)
	}
	want := len(cpuObservations) + len(memoryObservations) + len(diskObservations) + len(networkObservations)
	if len(sys.Observations) != want {
		t.Errorf("aggregated observations = %d, want %d", len(sys.Observations), want)
	}
	// DiagnoseNotes at the system level is intentionally nil; the per-resource
	// note lookup happens at prompt time via investigationForResource.
	if len(sys.DiagnoseNotes) != 0 {
		t.Errorf("system DiagnoseNotes should be empty; got %v", sys.DiagnoseNotes)
	}
}

func TestResourcesWithSignalsCanonicalOrder(t *testing.T) {
	// Memory and Network are absent in this snapshot; CPU and Disk remain
	// and should come out CPU-before-Disk per the canonical USE walk order.
	byRes := map[string][]Observation{
		ResourceDisk: {{Name: "iostat_max_aqu_sz", Section: "Saturation", Resource: ResourceDisk}},
		ResourceCPU:  {{Name: "vmstat_r", Section: "Saturation", Resource: ResourceCPU}},
	}
	got := resourcesWithSignals(byRes)
	if len(got) != 2 || got[0] != ResourceCPU || got[1] != ResourceDisk {
		t.Errorf("got %v, want [CPU Disk]", got)
	}
}

func TestVerdictIOWaitCrossResource(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	low := Value{Samples: []float64{1, 2}} // wa max 2 < 10
	if got := verdictIOWait(si, low, Snapshot{}); got != SignalLow {
		t.Errorf("low wa: got %v, want SignalLow", got)
	}

	highWA := Value{Samples: []float64{15, 25}} // wa max 25
	// With no disk data, elevated wa flags as Moderate (prompt the learner
	// to capture disk metrics).
	if got := verdictIOWait(si, highWA, Snapshot{}); got != SignalModerate {
		t.Errorf("high wa, no disk data: got %v, want SignalModerate", got)
	}
	// With disk showing saturation (aqu-sz > 1), wa correctly reads Low for
	// CPU saturation — the saturation belongs to the disk.
	diskSat := snapWith(map[string]Value{"iostat_max_aqu_sz": {Number: 17}})
	if got := verdictIOWait(si, highWA, diskSat); got != SignalLow {
		t.Errorf("high wa + disk saturated: got %v, want SignalLow (signal belongs to disk)", got)
	}
}

func TestGradeCPUSaturationCitingIOWaitWhileDiskSaturated(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"vmstat_wa":         {Samples: []float64{15, 25}}, // elevated wa
		"iostat_max_aqu_sz": {Number: 17},                 // disk is saturated
	})
	// A learner who claims CPU saturation and cites vmstat_wa as evidence
	// should get a contradicts result: with the disk-aware verdict, wa reads
	// Low for CPU saturation in this context. The heuristic on the citation
	// will then explain the secondary-cause inference.
	g := gradeDimension(si, snap, cpuObservations, "Saturation", "present", []string{"vmstat_wa"})
	if g.Contradicts != 1 {
		t.Fatalf("expected one contradicting citation, got contradicts=%d cited=%+v", g.Contradicts, g.Cited)
	}
	if len(g.Cited) != 1 || g.Cited[0].Verdict != citeContradicts {
		t.Errorf("expected citeContradicts on vmstat_wa, got %+v", g.Cited)
	}
}

func TestPrintDiagnoseFeedbackUsesReadableBlocks(t *testing.T) {
	grades := []dimensionGrade{
		{
			Dimension:   "Saturation",
			Claim:       "absent",
			HasData:     true,
			DataReads:   SignalHigh,
			Accurate:    false,
			Contradicts: 1,
			Cited: []citedEvidence{
				{
					Title:     "vmstat r (run-queue)",
					Verdict:   citeContradicts,
					Reads:     SignalHigh,
					Heuristic: "vmstat r above NumCPU = runnable threads waiting for a CPU = saturation (the tool drops vmstat's since-boot first row, so the verdict reflects interval samples only)",
				},
			},
		},
	}
	out := captureStdout(func() {
		printDiagnoseFeedback(grades, false)
	})
	for _, want := range []string{
		"Saturation\n",
		"  Verdict:    absent\n",
		"  Assessment: not supported — your evidence reads the opposite way\n",
		"  Evidence:\n\n    ✗ vmstat r (run-queue)\n",
		"      Reads \"present\", which points the other way.\n",
		"      Why: vmstat r above NumCPU",
		"  Note:\n",
		"    Strongest saturation signal reads \"present\".\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Saturation: you said") || strings.Contains(out, "Heads up:") {
		t.Fatalf("feedback still uses old dense wording:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 100 {
			t.Fatalf("feedback line too long (%d): %q\nfull output:\n%s", len(line), line, out)
		}
	}
}

func TestCPUUtilizationFeedbackExplainsAggregateLoadVsPerCPU(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	snap := snapWith(map[string]Value{
		"loadavg_1min":      {Number: 1.42, Note: "NumCPU=4, ratio 0.35"},
		"mpstat_idle_range": {Text: "0.0 - 96.0%", Samples: []float64{0, 96}, Note: "spread 96.0"},
		"vmstat_r":          {Samples: []float64{1, 1}},
	})
	g := gradeDimension(si, snap, cpuObservations, "Utilization", "high", []string{
		"loadavg_1min",
		"mpstat_idle_range",
		"vmstat_r",
	})
	out := captureStdout(func() {
		printDiagnoseFeedback([]dimensionGrade{g}, false)
	})

	for _, want := range []string{
		"✗ 1-min load average",
		"Observed: 1.42 (NumCPU=4, ratio 0.35)",
		"below NumCPU is not evidence of high overall utilization",
		"✓ Per-CPU %idle range",
		"Observed: 0.0 - 96.0% (spread 96.0)",
		"one CPU is highly utilized",
		"Wrong USE dimension: this is not a utilization signal.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q in:\n%s", want, out)
		}
	}
}

func TestPrintDiagnoseFeedbackSuggestsUncitedEvidence(t *testing.T) {
	grades := []dimensionGrade{
		{
			Dimension: "Saturation",
			Claim:     "absent",
			HasData:   true,
			DataReads: SignalLow,
			Accurate:  true,
			Supports:  1,
			Cited: []citedEvidence{{
				Title:   "vmstat r (run-queue)",
				Verdict: citeSupports,
				Reads:   SignalLow,
			}},
			Uncited: []citedEvidence{
				{Title: "vmstat wa (cpu I/O wait)", Verdict: citeSupports, Reads: SignalLow},
				{Title: "vmstat st (hypervisor steal)", Verdict: citeSupports, Reads: SignalLow},
			},
		},
	}
	out := captureStdout(func() {
		printDiagnoseFeedback(grades, false)
	})
	for _, want := range []string{
		"  Other relevant evidence you captured:\n",
		"    • vmstat wa (cpu I/O wait)\n",
		"      Reads \"absent\" and would support this verdict.\n",
		"    • vmstat st (hypervisor steal)\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q in:\n%s", want, out)
		}
	}
}

func TestSuggestNextCommandsSkipsAlreadyCaptured(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "vmstat 1 3", Output: sampleVmstat}}
	got := suggestNextCommands(cpuInvestigation, "Saturation", caps, SystemInfo{HasPSI: true}, 2)
	if len(got) == 0 {
		t.Fatal("expected at least one suggested command")
	}
	if got[0].Cmd != "cat /proc/pressure/cpu" {
		t.Fatalf("first suggested command = %q, want cat /proc/pressure/cpu; all=%+v", got[0].Cmd, got)
	}
	for _, cmd := range got {
		if cmd.Cmd == "vmstat 1 N" {
			t.Fatalf("suggested already captured vmstat command: %+v", got)
		}
	}
}

func TestSuggestNextCommandsCoversOtherResources(t *testing.T) {
	cases := []struct {
		name string
		inv  *Investigation
		dim  string
		caps []CapturedCommand
		si   SystemInfo
		want string
	}{
		{
			name: "memory saturation suggests PSI after vmstat",
			inv:  memoryInvestigation,
			dim:  "Saturation",
			caps: []CapturedCommand{{Cmd: "vmstat 1 3", Output: sampleVmstat}},
			si:   SystemInfo{HasMemoryPSI: true},
			want: "cat /proc/pressure/memory",
		},
		{
			name: "network saturation keeps sar EDEV distinct from sar DEV",
			inv:  networkInvestigation,
			dim:  "Saturation",
			caps: []CapturedCommand{{Cmd: "sar -n DEV 1 3", Output: sampleSarDev}},
			si:   SystemInfo{HasSar: true},
			want: "sar -n EDEV 1 N",
		},
		{
			name: "network errors suggest proc net dev first",
			inv:  networkInvestigation,
			dim:  "Errors",
			caps: nil,
			si:   SystemInfo{},
			want: "cat /proc/net/dev",
		},
	}
	for _, tc := range cases {
		got := suggestNextCommands(tc.inv, tc.dim, tc.caps, tc.si, 2)
		if len(got) == 0 || got[0].Cmd != tc.want {
			t.Fatalf("%s: suggestions=%+v, first want %q", tc.name, got, tc.want)
		}
	}
}

func TestCommandFamilyKeyDistinguishesSarNetworkModes(t *testing.T) {
	if commandFamilyKey("sar -n DEV 1 3") == commandFamilyKey("sar -n EDEV 1 3") {
		t.Fatalf("sar -n DEV and sar -n EDEV should be distinct command families")
	}
	if !commandWasCaptured(CommandRef{Cmd: "sar -n EDEV 1 N"}, []CapturedCommand{{Cmd: "sar -n EDEV 1 3"}}) {
		t.Fatalf("sar -n EDEV command family should match different sample counts")
	}
}

func TestPrintDiagnoseFeedbackSuggestsNextCommands(t *testing.T) {
	grades := []dimensionGrade{
		{
			Dimension: "Saturation",
			Claim:     "absent",
			HasData:   true,
			DataReads: SignalLow,
			Accurate:  true,
			Supports:  1,
			Cited: []citedEvidence{{
				Title:   "vmstat r (run-queue)",
				Verdict: citeSupports,
				Reads:   SignalLow,
			}},
			NextCommands: []CommandRef{{
				Cmd:     "cat /proc/pressure/cpu",
				Summary: "PSI: time-share of tasks stalled on CPU.\nLinux 4.20+ with PSI enabled.",
			}},
		},
	}
	out := captureStdout(func() {
		printDiagnoseFeedback(grades, false)
	})
	for _, want := range []string{
		"  To gather more supporting evidence:\n",
		"    • cat /proc/pressure/cpu\n",
		"      PSI: time-share of tasks stalled on CPU.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q in:\n%s", want, out)
		}
	}
}

func TestParseIndexList(t *testing.T) {
	got, err := parseIndexList("3, 1,1 2", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := parseIndexList("6", 5); err == nil {
		t.Errorf("expected out-of-range error")
	}
	if _, err := parseIndexList("none", 5); err == nil {
		t.Errorf("expected error for non-numeric input")
	}
}

func TestMoveSelectionWraps(t *testing.T) {
	if got := moveSelection(0, 3, keyUp); got != 2 {
		t.Errorf("move up from first = %d, want 2", got)
	}
	if got := moveSelection(2, 3, keyDown); got != 0 {
		t.Errorf("move down from last = %d, want 0", got)
	}
	if got := moveSelection(1, 3, keySpace); got != 1 {
		t.Errorf("unhandled key moved selection to %d, want 1", got)
	}
}

func TestReadTerminalKeyDistinguishesClearAndRedraw(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()

	stdin = bufio.NewReader(strings.NewReader("n\f?kJ"))
	key, err := readTerminalKey()
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != keyClear {
		t.Fatalf("first key = %v, want keyClear", key.Key)
	}

	key, err = readTerminalKey()
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != keyRedraw {
		t.Fatalf("second key = %v, want keyRedraw", key.Key)
	}

	key, err = readTerminalKey()
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != keyHelp {
		t.Fatalf("third key = %v, want keyHelp", key.Key)
	}

	key, err = readTerminalKey()
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != keyUp {
		t.Fatalf("fourth key = %v, want keyUp", key.Key)
	}

	key, err = readTerminalKey()
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != keyDown {
		t.Fatalf("fifth key = %v, want keyDown", key.Key)
	}
}

func TestSelectorRenderersReportLineCounts(t *testing.T) {
	claimLines := 0
	captureStdout(func() {
		claimLines = renderClaimSelector("Utilization", "extra context", claimOptions("Utilization"), 0, false)
	})
	if claimLines != 7 {
		t.Fatalf("claim selector lines = %d, want 7", claimLines)
	}

	evidenceLines := 0
	candidates := []Observation{
		{Name: "loadavg_1min", Title: "1-min load average"},
		{Name: "mpstat_idle_mean", Title: "Mean %idle (mpstat)"},
	}
	captureStdout(func() {
		evidenceLines = renderEvidenceSelector(candidates, Snapshot{}, 0, []bool{true, false}, false)
	})
	if evidenceLines != 4 {
		t.Fatalf("evidence selector lines = %d, want 4", evidenceLines)
	}
}

func TestSelectorRenderersCountWrappedRows(t *testing.T) {
	oldWidth := selectorTerminalWidth
	selectorTerminalWidth = func() int { return 30 }
	defer func() { selectorTerminalWidth = oldWidth }()

	candidates := []Observation{
		{Name: "long_signal", Title: "Very long evidence title"},
	}
	snap := Snapshot{Values: map[string]Value{
		"long_signal": {Text: strings.Repeat("x", 70)},
	}}
	lines := 0
	captureStdout(func() {
		lines = renderEvidenceSelector(candidates, snap, 0, []bool{false}, false)
	})

	logicalLines := 1 + len(candidates) + len(evidenceSelectorHelp(len(candidates), false))
	if lines <= logicalLines {
		t.Fatalf("wrapped evidence selector rows = %d, want more than logical line count %d", lines, logicalLines)
	}
}

func TestVisualLineRows(t *testing.T) {
	cases := []struct {
		line  string
		width int
		want  int
	}{
		{"", 10, 1},
		{"1234567890", 10, 1},
		{"12345678901", 10, 2},
		{"123\t", 4, 2},
	}
	for _, tc := range cases {
		if got := visualLineRows(tc.line, tc.width); got != tc.want {
			t.Fatalf("visualLineRows(%q, %d) = %d, want %d", tc.line, tc.width, got, tc.want)
		}
	}
}

func TestSelectorRenderersCanExpandHelp(t *testing.T) {
	claimLines := 0
	claimOut := captureStdout(func() {
		claimLines = renderClaimSelector("Errors", "", claimOptions("Errors"), 0, true)
	})
	if claimLines <= 1+len(claimOptions("Errors"))+1 {
		t.Fatalf("expanded claim selector lines = %d, want more than compact help", claimLines)
	}
	if !strings.Contains(claimOut, "hide help") || !strings.Contains(claimOut, "move between verdicts") {
		t.Fatalf("expanded claim help missing expected text:\n%s", claimOut)
	}

	candidates := []Observation{
		{Name: "vmstat_r", Title: "vmstat r (run-queue)"},
	}
	evidenceLines := 0
	evidenceOut := captureStdout(func() {
		evidenceLines = renderEvidenceSelector(candidates, Snapshot{}, 0, []bool{false}, true)
	})
	if evidenceLines <= 1+len(candidates)+1 {
		t.Fatalf("expanded evidence selector lines = %d, want more than compact help", evidenceLines)
	}
	if !strings.Contains(evidenceOut, "submit selected signals") || !strings.Contains(evidenceOut, "hide help") {
		t.Fatalf("expanded evidence help missing expected text:\n%s", evidenceOut)
	}
}

func TestRedrawFullScreenClearsAndRerenders(t *testing.T) {
	lines := 0
	out := captureStdout(func() {
		lines = redrawFullScreen(func() int {
			fmt.Print("selector")
			return 1
		})
	})
	if lines != 1 {
		t.Fatalf("lines = %d, want 1", lines)
	}
	if !strings.HasPrefix(out, "\x1b[H\x1b[2Jselector") {
		t.Fatalf("redraw output = %q, want clear-screen prefix and selector", out)
	}
}

func TestClearRenderedBlockUsesCursorUpInsteadOfSaveRestore(t *testing.T) {
	out := captureStdout(func() {
		clearRenderedBlock(3)
	})
	if strings.Contains(out, "\x1b7") || strings.Contains(out, "\x1b8") {
		t.Fatalf("clearRenderedBlock should not use save/restore cursor escapes: %q", out)
	}
	if got := strings.Count(out, "\x1b[1A"); got != 2 {
		t.Fatalf("cursor-up count = %d, want 2 in %q", got, out)
	}
}

func TestAskClaimFallsBackToNumberInput(t *testing.T) {
	oldStdin := stdin
	oldRaw := rawInputEnabled
	defer func() {
		stdin = oldStdin
		rawInputEnabled = oldRaw
	}()
	stdin = bufio.NewReader(strings.NewReader("3\n"))
	rawInputEnabled = func() bool { return false }

	var claim string
	var quit bool
	captureStdout(func() {
		claim, quit = askClaim("Utilization", "Utilization", "")
	})
	if quit || claim != "low" {
		t.Fatalf("askClaim() = %q, quit=%v; want low, false", claim, quit)
	}
}

func TestAskEvidenceFallsBackToIndexInput(t *testing.T) {
	oldStdin := stdin
	oldRaw := rawInputEnabled
	defer func() {
		stdin = oldStdin
		rawInputEnabled = oldRaw
	}()
	stdin = bufio.NewReader(strings.NewReader("2, 1\n"))
	rawInputEnabled = func() bool { return false }

	candidates := []Observation{
		{Name: "loadavg_1min", Title: "1-min load average"},
		{Name: "mpstat_idle_mean", Title: "Mean %idle (mpstat)"},
	}
	var names []string
	var quit bool
	captureStdout(func() {
		names, quit = askEvidence(candidates, Snapshot{})
	})
	want := []string{"loadavg_1min", "mpstat_idle_mean"}
	if quit || len(names) != len(want) {
		t.Fatalf("askEvidence() = %v, quit=%v; want %v, false", names, quit, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("askEvidence() = %v, want %v", names, want)
		}
	}
}
