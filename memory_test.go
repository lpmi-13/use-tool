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

// sampleFreeM mirrors the values in sampleMeminfo so the test can assert
// equivalence between the meminfo and `free -m` extraction paths.
//
//	MemTotal=16384000 kB → 16000 MiB; MemAvailable=8192000 kB → 8000 MiB;
//	Cached+Buffers=6512000 kB → 6359 MiB (rounded to nearest MiB);
//	SwapTotal=2097152 kB → 2048 MiB; SwapFree=1048576 kB → 1024 MiB.
const sampleFreeM = `              total        used        free      shared  buff/cache   available
Mem:          16000        2641        7000         300        6359        8000
Swap:          2048        1024        1024`

// sampleFreeH is the same machine via `free -h`. Values are rounded to
// one decimal place with IEC suffixes, so extracted numbers differ
// slightly from the meminfo path; tests assert within a tolerance.
const sampleFreeH = `              total        used        free      shared  buff/cache   available
Mem:           15Gi        2.6Gi       6.8Gi       300Mi       6.2Gi       7.8Gi
Swap:         2.0Gi        1.0Gi       1.0Gi`

// sampleFreeDefault has no flag, so cells are in kibibytes (procps default).
const sampleFreeDefault = `              total        used        free      shared  buff/cache   available
Mem:       16384000     2703000     7168000      307200     6512000     8192000
Swap:       2097152     1048576     1048576`

func TestParseFreeOutputKB_DashM(t *testing.T) {
	fs, ok := parseFreeOutputKB("free -m", sampleFreeM)
	if !ok {
		t.Fatal("expected free -m to parse")
	}
	if !fs.HasMem || !fs.HasSwap || !fs.HasAvailable {
		t.Fatalf("missing flags: %+v", fs)
	}
	// 16000 MiB = 16384000 KiB
	if fs.MemTotalKB != 16384000 {
		t.Errorf("MemTotalKB: got %v, want 16384000", fs.MemTotalKB)
	}
	if fs.MemAvailableKB != 8192000 {
		t.Errorf("MemAvailableKB: got %v, want 8192000", fs.MemAvailableKB)
	}
	if fs.SwapTotalKB != 2097152 {
		t.Errorf("SwapTotalKB: got %v, want 2097152", fs.SwapTotalKB)
	}
	if fs.Rounded {
		t.Error("free -m should not be marked rounded")
	}
}

func TestParseFreeOutputKB_DashH(t *testing.T) {
	fs, ok := parseFreeOutputKB("free -h", sampleFreeH)
	if !ok {
		t.Fatal("expected free -h to parse")
	}
	if !fs.Rounded {
		t.Error("free -h should be marked rounded")
	}
	// 15 Gi = 15*1024*1024 = 15728640 KiB. Tolerant compare: within 5% of meminfo total.
	const meminfoTotalKB = 16384000
	if fs.MemTotalKB < meminfoTotalKB*0.95 || fs.MemTotalKB > meminfoTotalKB*1.05 {
		t.Errorf("MemTotalKB %v should be within 5%% of %v", fs.MemTotalKB, meminfoTotalKB)
	}
	// 7.8 Gi available → within 5% of 8192000.
	if fs.MemAvailableKB < 8192000*0.95 || fs.MemAvailableKB > 8192000*1.05 {
		t.Errorf("MemAvailableKB %v should be within 5%% of 8192000", fs.MemAvailableKB)
	}
}

func TestParseFreeOutputKB_DefaultIsKB(t *testing.T) {
	fs, ok := parseFreeOutputKB("free", sampleFreeDefault)
	if !ok {
		t.Fatal("expected default free to parse")
	}
	if fs.MemTotalKB != 16384000 {
		t.Errorf("MemTotalKB: got %v, want 16384000", fs.MemTotalKB)
	}
}

func TestParseFreeOutputKB_RejectsNonFree(t *testing.T) {
	if _, ok := parseFreeOutputKB("cat", "Hello world"); ok {
		t.Error("expected non-free output to fail parsing")
	}
}

func TestParseFreeCellKB(t *testing.T) {
	cases := []struct {
		cell   string
		defKB  float64
		want   float64
		wantOK bool
	}{
		{"1024", 1.0, 1024, true},         // default KiB
		{"16000", 1024.0, 16384000, true}, // -m
		{"15Gi", 1.0, 15 * 1024 * 1024, true},
		{"1.5G", 1.0, 1.5 * 1024 * 1024, true},
		{"512Mi", 1.0, 512 * 1024, true},
		{"0B", 1.0, 0, true},
		{"", 1.0, 0, false},
		{"abc", 1.0, 0, false},
		{"15Xi", 1.0, 0, false},
	}
	for _, tc := range cases {
		got, ok := parseFreeCellKB(tc.cell, tc.defKB)
		if ok != tc.wantOK {
			t.Errorf("%q: ok=%v, want %v", tc.cell, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.cell, got, tc.want)
		}
	}
}

func TestExtractMemUsedPct_FromFreeM(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "free -m", Output: sampleFreeM}}
	v, ok := extractMemUsedPct(si, caps)
	if !ok {
		t.Fatal("expected mem used extraction from free -m to succeed")
	}
	// 16000-8000 / 16000 = 50%
	if v.Number != 50 {
		t.Errorf("expected 50%%, got %v", v.Number)
	}
}

func TestExtractMemUsedPct_FromFreeH(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "free -h", Output: sampleFreeH}}
	v, ok := extractMemUsedPct(si, caps)
	if !ok {
		t.Fatal("expected mem used extraction from free -h to succeed")
	}
	// Tolerant: meminfo path returns 50%; -h rounding may shift by a few %.
	if v.Number < 45 || v.Number > 55 {
		t.Errorf("expected ~50%%, got %v", v.Number)
	}
	if !strings.Contains(v.Note, "free -h") {
		t.Errorf("expected note to flag rounded free -h source, got %q", v.Note)
	}
}

func TestExtractMemAvailableGiB_FromFreeM(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "free -m", Output: sampleFreeM}}
	v, ok := extractMemAvailableGiB(si, caps)
	if !ok {
		t.Fatal("expected mem available extraction from free -m to succeed")
	}
	// 8000 MiB = 7.8125 GiB
	if v.Number < 7.7 || v.Number > 7.9 {
		t.Errorf("expected ~7.8 GiB, got %v", v.Number)
	}
}

func TestExtractCacheBuffersGiB_FromFreeM(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "free -m", Output: sampleFreeM}}
	v, ok := extractCacheBuffersGiB(si, caps)
	if !ok {
		t.Fatal("expected cache+buffers extraction from free -m to succeed")
	}
	// 6359 MiB ≈ 6.21 GiB
	if v.Number < 6.0 || v.Number > 6.4 {
		t.Errorf("expected ~6.2 GiB, got %v", v.Number)
	}
}

func TestExtractSwapUsedPct_FromFreeM(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "free -m", Output: sampleFreeM}}
	v, ok := extractSwapUsedPct(si, caps)
	if !ok {
		t.Fatal("expected swap extraction from free -m to succeed")
	}
	// (2048-1024)/2048 = 50%
	if v.Number != 50 {
		t.Errorf("expected 50%%, got %v", v.Number)
	}
}

func TestExtractMemUsedPct_MeminfoTakesPrecedence(t *testing.T) {
	// When both meminfo and free are captured, meminfo wins (it's the
	// canonical source). Use a free output that would yield a different
	// number to confirm.
	si := SystemInfo{}
	caps := []CapturedCommand{
		{Cmd: "free -m", Output: sampleFreeM}, // would say 50%
		{Cmd: "cat /proc/meminfo", Output: sampleMeminfo},
	}
	v, ok := extractMemUsedPct(si, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 50 {
		t.Errorf("expected meminfo path (50%%), got %v", v.Number)
	}
	// And the note shouldn't mention rounded free -h.
	if strings.Contains(v.Note, "free -h") {
		t.Errorf("meminfo path should not flag rounded source, got %q", v.Note)
	}
}

func TestExtractPSIMemorySome(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}}
	v, ok := extractPSIAvg10("/proc/pressure/memory", "some")(si, caps)
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
	v, ok := extractPSIAvg10("/proc/pressure/memory", "full")(si, caps)
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
	// vmstat's first row is since-boot and dropped by the extractor; only
	// the second-row interval sample remains from the two-row fixture.
	want := []float64{80}
	if len(siVal.Samples) != len(want) {
		t.Fatalf("si: got %d samples, want %d", len(siVal.Samples), len(want))
	}
	for i, w := range want {
		if siVal.Samples[i] != w {
			t.Errorf("si[%d]: got %v, want %v", i, siVal.Samples[i], w)
		}
	}
	soVal, ok := extractVmstatColumn("so")(si, caps)
	if !ok {
		t.Fatal("expected so extraction to succeed")
	}
	wantSo := []float64{60}
	if len(soVal.Samples) != len(wantSo) {
		t.Fatalf("so: got %d samples, want %d", len(soVal.Samples), len(wantSo))
	}
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

func TestExtractDmesgOOMIgnoresFalsePositiveCmds(t *testing.T) {
	si := SystemInfo{}
	cases := []string{
		"vim dmesg.txt",
		"cat /var/log/old-dmesg",
		"grep dmesg /etc/issue",
	}
	for _, cmd := range cases {
		caps := []CapturedCommand{{Cmd: cmd, Output: sampleDmesgOOM}}
		if _, ok := extractDmesgOOM(si, caps); ok {
			t.Errorf("expected %q not to match dmesg", cmd)
		}
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

func TestMemoryInvestigationRegistered(t *testing.T) {
	inv, err := getInvestigation("memory")
	if err != nil {
		t.Fatalf("memory investigation not registered: %v", err)
	}
	if inv.Name != "memory" {
		t.Errorf("unexpected name %q", inv.Name)
	}
	if len(inv.Observations) == 0 || len(inv.Commands) == 0 {
		t.Error("memory investigation is missing observations/commands/extractors")
	}
}

// ----- recall edge cases -----

const sampleSarW = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

12:00:00     pswpin/s pswpout/s
12:00:01         0.00      0.00
12:00:02         0.00      1.50
12:00:03         0.00      0.00
Average:         0.00      0.50`

const sampleTopMem = `top - 14:32:01 up  3:14,  2 users,  load average: 0.50, 0.30, 0.20
Tasks: 281 total,   1 running, 280 sleeping,   0 stopped,   0 zombie
%Cpu(s):  3.5 us,  1.2 sy,  0.0 ni, 95.1 id,  0.2 wa,  0.0 hi,  0.0 si,  0.0 st
MiB Mem :  15978.5 total,   2456.2 free,  10234.5 used,   3287.8 buff/cache
MiB Swap:   2048.0 total,   2048.0 free,      0.0 used.   3987.6 avail Mem

    PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND
   1234 alice     20   0 5634892 1.234g  56234 S   0.0   7.9   3:01.23 firefox
   5678 bob       20   0  892348 234567  34567 S   0.0   1.5   0:23.45 chrome`

func TestFreeColumnQuestionsCoversObservedColumns(t *testing.T) {
	c := CapturedCommand{Cmd: "free -h", Output: sampleFreeH}
	qs := freeColumnQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for free -h output")
	}
	want := []string{"total", "used", "free", "shared", "buff/cache", "available"}
	for _, col := range want {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, "`"+col+"`") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("freeColumnQuestions missing %q column question", col)
		}
	}
}

func TestFreeColumnQuestionsRejectsUnrelatedOutput(t *testing.T) {
	c := CapturedCommand{Cmd: "echo", Output: "hello world"}
	if qs := freeColumnQuestions(SystemInfo{}, c); qs != nil {
		t.Errorf("expected nil for unrelated output, got %d", len(qs))
	}
}

func TestSarWColumnQuestionsCoversBothColumns(t *testing.T) {
	c := CapturedCommand{Cmd: "sar -W 1 3", Output: sampleSarW}
	qs := sarWColumnQuestions(SystemInfo{}, c)
	if len(qs) != 2 {
		t.Fatalf("expected 2 questions (pswpin/s and pswpout/s), got %d", len(qs))
	}
	wantColumns := []string{"pswpin/s", "pswpout/s"}
	for _, col := range wantColumns {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, col) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("sarWColumnQuestions missing %q column question", col)
		}
	}
}

func TestTopMemColumnQuestionsCoversKeyColumns(t *testing.T) {
	c := CapturedCommand{Cmd: "top -bn1 -o %MEM", Output: sampleTopMem}
	qs := topMemColumnQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for top output")
	}
	wantColumns := []string{"VIRT", "RES", "SHR", "%MEM"}
	for _, col := range wantColumns {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, "`"+col+"`") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("topMemColumnQuestions missing %q column question", col)
		}
	}
}

func TestMemoryBaselineVariantsDispatch(t *testing.T) {
	combined := combineVariantQuestions(memoryBaselineVariants())
	// meminfo output → meminfo questions
	qs := combined(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo})
	if len(qs) == 0 {
		t.Fatal("expected meminfo questions via baseline variants")
	}
	// free -h output → free column questions
	qs = combined(SystemInfo{}, CapturedCommand{Cmd: "free -h", Output: sampleFreeH})
	if len(qs) == 0 {
		t.Fatal("expected free questions via baseline variants")
	}
	gotFreeCol := false
	for _, q := range qs {
		if strings.Contains(q.Stem, "buff/cache") || strings.Contains(q.Stem, "`available`") {
			gotFreeCol = true
			break
		}
	}
	if !gotFreeCol {
		t.Errorf("expected free-column-style question for free output; got %v", stems(qs))
	}
}
