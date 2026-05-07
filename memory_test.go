package main

import (
	"strings"
	"testing"
)

const sampleMeminfo = `MemTotal:       16384000 kB
MemFree:         1024000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          6000000 kB
SwapCached:            0 kB
Active:          5000000 kB
Inactive:        2000000 kB
SwapTotal:       2097152 kB
SwapFree:        1048576 kB`

const sampleMeminfoNoSwap = `MemTotal:       16384000 kB
MemFree:         1024000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          6000000 kB
SwapTotal:             0 kB
SwapFree:              0 kB`

const samplePSIMemory = `some avg10=4.20 avg60=2.50 avg300=1.10 total=12345678
full avg10=0.80 avg60=0.20 avg300=0.05 total=2345678`

const samplePSIMemoryQuiet = `some avg10=0.00 avg60=0.00 avg300=0.00 total=0
full avg10=0.00 avg60=0.00 avg300=0.00 total=0`

const sampleVmstatPaging = `procs -----------memory---------- ---swap-- -----io---- -system-- ------cpu-----
 r  b   swpd   free   buff  cache   si   so    bi    bo   in   cs us sy id wa st
 1  2 524288  10240   8000  60000  120   80   500   200  300  500  5  3 70 22  0
 2  3 524288  10100   8001  59999   80   60   400   180  280  490  4  3 73 20  0`

const sampleDmesgOOM = `[Tue Oct  5 14:00:00 2024] Out of memory: Killed process 12345 (worker) total-vm:4194304kB
[Tue Oct  5 14:00:00 2024] oom-killer: gfp_mask=0xcc0(GFP_KERNEL)
[Tue Oct  5 14:00:00 2024] Memory cgroup out of memory: Killed process 12345
some unrelated noise line that should not match
[Tue Oct  5 14:01:00 2024] Killed process 12346 (helper) total-vm:1048576kB`

func TestMeminfoQuestionsFiresOnMeminfoOutput(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}
	qs := meminfoQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for /proc/meminfo output")
	}
}

func TestMeminfoQuestionsRejectsUnrelatedOutput(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "cat", Output: "MemTotalIsh: not really"}
	if qs := meminfoQuestions(si, c); qs != nil {
		t.Error("expected nil for non-meminfo output")
	}
}

func TestParseMeminfoKB(t *testing.T) {
	cases := []struct {
		key  string
		want float64
		ok   bool
	}{
		{"MemTotal", 16384000, true},
		{"MemAvailable", 8192000, true},
		{"SwapFree", 1048576, true},
		{"NotPresent", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseMeminfoKB(sampleMeminfo, tc.key)
		if ok != tc.ok {
			t.Errorf("%s: ok=%v, want %v", tc.key, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestExtractMemUsedPct(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}}
	v, ok := extractMemUsedPct(si, caps)
	if !ok {
		t.Fatal("expected mem used extraction to succeed")
	}
	// (16384000 - 8192000) / 16384000 = 50%
	if v.Number != 50 {
		t.Errorf("expected 50%%, got %v", v.Number)
	}
	if v.Unit != "%" {
		t.Errorf("expected %% unit, got %q", v.Unit)
	}
}

func TestExtractMemUsedPctNoMeminfo(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "echo", Output: "nope"}}
	if _, ok := extractMemUsedPct(si, caps); ok {
		t.Error("expected extraction to fail without meminfo")
	}
}

func TestExtractSwapUsedPct(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}}
	v, ok := extractSwapUsedPct(si, caps)
	if !ok {
		t.Fatal("expected swap extraction to succeed")
	}
	// (2097152 - 1048576) / 2097152 = 50%
	if v.Number != 50 {
		t.Errorf("expected 50%%, got %v", v.Number)
	}
}

func TestExtractSwapUsedPctNoSwap(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/meminfo", Output: sampleMeminfoNoSwap}}
	v, ok := extractSwapUsedPct(si, caps)
	if !ok {
		t.Fatal("expected extraction to succeed even with no swap")
	}
	if v.Text != "no swap configured" {
		t.Errorf("expected no-swap text, got %q", v.Text)
	}
}

func TestExtractCacheBuffersGiB(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}}
	v, ok := extractCacheBuffersGiB(si, caps)
	if !ok {
		t.Fatal("expected cache+buffers extraction to succeed")
	}
	// (6000000 + 512000) kB = 6512000 kB ≈ 6.21 GiB
	if v.Number < 6.0 || v.Number > 6.4 {
		t.Errorf("expected ~6.2 GiB, got %v", v.Number)
	}
}

func TestPSIMemoryQuestionsFires(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}
	qs := psiMemoryQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected PSI questions for valid PSI output")
	}
}

func TestPSIMemoryQuestionsRejectsOther(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "cat", Output: "this has nothing to do with PSI"}
	if qs := psiMemoryQuestions(si, c); qs != nil {
		t.Error("expected nil for non-PSI output")
	}
}

func TestExtractPSIMemorySome(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}}
	v, ok := extractPSIMemory("some")(si, caps)
	if !ok {
		t.Fatal("expected PSI some extraction to succeed")
	}
	if v.Number != 4.20 {
		t.Errorf("expected 4.20, got %v", v.Number)
	}
	if v.Unit != "%" {
		t.Errorf("expected %% unit, got %q", v.Unit)
	}
}

func TestExtractPSIMemoryFull(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}}
	v, ok := extractPSIMemory("full")(si, caps)
	if !ok {
		t.Fatal("expected PSI full extraction to succeed")
	}
	if v.Number != 0.80 {
		t.Errorf("expected 0.80, got %v", v.Number)
	}
}

func TestVmstatSiSoExtraction(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "vmstat 1 2", Output: sampleVmstatPaging}}
	siVal, ok := extractVmstatColumn("si")(si, caps)
	if !ok {
		t.Fatal("expected si extraction to succeed")
	}
	want := []float64{120, 80}
	for i, w := range want {
		if siVal.Samples[i] != w {
			t.Errorf("si[%d]: got %v, want %v", i, siVal.Samples[i], w)
		}
	}
	soVal, ok := extractVmstatColumn("so")(si, caps)
	if !ok {
		t.Fatal("expected so extraction to succeed")
	}
	wantSo := []float64{80, 60}
	for i, w := range wantSo {
		if soVal.Samples[i] != w {
			t.Errorf("so[%d]: got %v, want %v", i, soVal.Samples[i], w)
		}
	}
}

func TestExtractDmesgOOMCounts(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "dmesg", Output: sampleDmesgOOM}}
	v, ok := extractDmesgOOM(si, caps)
	if !ok {
		t.Fatal("expected OOM extraction to succeed")
	}
	// 4 lines mention OOM keywords, 1 unrelated
	if !strings.Contains(v.Text, "4/5") {
		t.Errorf("expected 4/5 OOM count, got %q", v.Text)
	}
}

func TestExtractDmesgOOMRequiresDmesgCommand(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "echo", Output: sampleDmesgOOM}}
	if _, ok := extractDmesgOOM(si, caps); ok {
		t.Error("expected extraction to fail when output is not from dmesg/journalctl")
	}
}

func TestOOMQuestionsFiresOnOOMOutput(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "dmesg", Output: sampleDmesgOOM}
	qs := oomQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected OOM questions for OOM output")
	}
}

func TestOOMQuestionsSkipsBenign(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "dmesg", Output: "[Tue Oct  5] kernel boot complete"}
	if qs := oomQuestions(si, c); qs != nil {
		t.Error("expected no OOM questions for non-OOM output")
	}
}

func TestSwapPSIConsistencyQuiet(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_si":          {Samples: []float64{0, 0}},
		"vmstat_so":          {Samples: []float64{0, 0}},
		"psi_mem_full_avg10": {Number: 0.05},
	}
	q, ok := swapPSIConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire for quiet system")
	}
	if !strings.Contains(q.Correct, "Consistent") || !strings.Contains(q.Correct, "not under memory pressure") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestSwapPSIConsistencyPaging(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_si":          {Samples: []float64{120, 80}},
		"vmstat_so":          {Samples: []float64{80, 60}},
		"psi_mem_full_avg10": {Number: 5.0},
	}
	q, ok := swapPSIConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire for paging system")
	}
	if !strings.Contains(q.Correct, "Consistent") || !strings.Contains(q.Correct, "tasks are being slowed") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestSwapPSIConsistencyCgroupSignal(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_si":          {Samples: []float64{0, 0}},
		"vmstat_so":          {Samples: []float64{0, 0}},
		"psi_mem_full_avg10": {Number: 8.0},
	}
	q, ok := swapPSIConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire for cgroup-pressure pattern")
	}
	if !strings.Contains(q.Correct, "cgroup") {
		t.Errorf("expected cgroup-level explanation, got %q", q.Correct)
	}
}

func TestSwapPSIConsistencyAmbiguousReturnsFalse(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_si":          {Samples: []float64{1, 2}},
		"vmstat_so":          {Samples: []float64{0, 1}},
		"psi_mem_full_avg10": {Number: 0.7},
	}
	if _, ok := swapPSIConsistency.Generate(si, vs); ok {
		t.Error("expected synthesis to skip the ambiguous middle case")
	}
}

func TestMemoryInvestigationRegistered(t *testing.T) {
	inv, err := getInvestigation("memory")
	if err != nil {
		t.Fatalf("memory investigation not registered: %v", err)
	}
	if inv.Name != "memory" {
		t.Errorf("unexpected name %q", inv.Name)
	}
	if len(inv.Observations) == 0 || len(inv.Commands) == 0 || len(inv.Extractors) == 0 {
		t.Error("memory investigation is missing observations/commands/extractors")
	}
}
