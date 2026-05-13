package main

import (
	"math/rand"
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

func TestMeminfoQuestionsUseObservedValues(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}
	qs := meminfoQuestions(si, c)
	if len(qs) < 3 {
		t.Fatalf("expected several observed-value questions, got %d", len(qs))
	}
	var stems []string
	for _, q := range qs {
		stems = append(stems, q.Stem)
	}
	joined := strings.Join(stems, "\n")
	for _, want := range []string{"15.6 GiB", "7.8 GiB", "5.7 GiB"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("meminfo question stems did not include observed value %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "If `Cached` is 6 GiB") {
		t.Fatalf("meminfo questions still include fixed cache example:\n%s", joined)
	}
}

func TestGuideQuestionSelectionCanVaryForMeminfo(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	qs := meminfoQuestions(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo})
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		q, ok := chooseGuideQuestion(qs)
		if !ok {
			t.Fatal("expected guide question")
		}
		seen[q.Stem] = true
	}
	if len(seen) < 2 {
		t.Fatalf("guide selection did not vary across repeated picks; saw %d unique stem(s)", len(seen))
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

// sampleFreeM mirrors the values in sampleMeminfo so the test can assert
// equivalence between the meminfo and `free -m` extraction paths.
//   MemTotal=16384000 kB → 16000 MiB; MemAvailable=8192000 kB → 8000 MiB;
//   Cached+Buffers=6512000 kB → 6359 MiB (rounded to nearest MiB);
//   SwapTotal=2097152 kB → 2048 MiB; SwapFree=1048576 kB → 1024 MiB.
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
		cell    string
		defKB   float64
		want    float64
		wantOK  bool
	}{
		{"1024", 1.0, 1024, true},        // default KiB
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

// ----- recall edge cases -----

func TestMemUsedPctRecallAtZeroAndFull(t *testing.T) {
	for _, pct := range []float64{0, 100} {
		v := Value{Number: pct}
		qs := memUsedPctRecall(v)
		if len(qs) != 1 {
			t.Fatalf("expected 1 question at pct=%v, got %d", pct, len(qs))
		}
		q := qs[0]
		seen := map[string]bool{q.Correct: true}
		for _, d := range q.Distractors {
			if seen[d] {
				t.Errorf("duplicate option at pct=%v: %v", pct, q.Distractors)
			}
			seen[d] = true
		}
	}
}

func TestSwapSamplesRecallAtZero(t *testing.T) {
	// On a host with no swap activity, samples are all 0 — the original
	// implementation included literal "0" as a distractor, colliding with
	// correct. Helper must dedupe.
	v := Value{Samples: []float64{0, 0, 0}}
	qs := swapSamplesRecall("si")(v)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	q := qs[0]
	if q.Correct != "0" {
		t.Errorf("correct: got %q, want 0", q.Correct)
	}
	for _, d := range q.Distractors {
		if d == q.Correct {
			t.Errorf("distractor matches correct: %v", q.Distractors)
		}
	}
}

func TestVmstatMemoryQuestionsCoversBothDirections(t *testing.T) {
	c := CapturedCommand{Cmd: "vmstat 1 2", Output: sampleVmstatPaging}
	seen := map[string]bool{}
	wantCorrect := map[string]string{
		"`si`": "Pages swapped in from swap to memory per second",
		"`so`": "Pages swapped out from memory to swap per second",
	}
	for i := 0; i < 100; i++ {
		qs := vmstatMemoryQuestions(SystemInfo{}, c)
		if len(qs) < 1 {
			t.Fatalf("iteration %d: expected at least 1 question", i)
		}
		column := ""
		for candidate := range wantCorrect {
			if strings.Contains(qs[0].Stem, candidate) {
				column = candidate
				break
			}
		}
		if column == "" {
			t.Fatalf("iteration %d: stem does not ask about si/so: %q", i, qs[0].Stem)
		}
		if qs[0].Correct != wantCorrect[column] {
			t.Fatalf("iteration %d: column %q had correct answer %q, want %q", i, column, qs[0].Correct, wantCorrect[column])
		}
		seen[column] = true
	}
	for _, want := range []string{"`si`", "`so`"} {
		if !seen[want] {
			t.Errorf("direction %q never appeared across 100 iterations; saw %v", want, seen)
		}
	}
}

func TestVmstatMemoryQuestionsUseObservedSamples(t *testing.T) {
	c := CapturedCommand{Cmd: "vmstat 1 2", Output: sampleVmstatPaging}
	qs := vmstatMemoryQuestions(SystemInfo{}, c)
	var stems []string
	for _, q := range qs {
		stems = append(stems, q.Stem)
	}
	joined := strings.Join(stems, "\n")
	for _, want := range []string{"`si` samples [120, 80]", "`so` samples [80, 60]", "max `si` 120", "max `so` 80"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("vmstat questions did not include observed value %q:\n%s", want, joined)
		}
	}
}

func TestPSIMemoryQuestionsCoversBothMetrics(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}
	seen := map[string]bool{}
	wantCorrect := map[string]string{
		"`some`": "The percentage of time during which at least one task was stalled on memory",
		"`full`": "The percentage of time during which all non-idle tasks were simultaneously stalled on memory",
	}
	for i := 0; i < 100; i++ {
		qs := psiMemoryQuestions(SystemInfo{}, c)
		if len(qs) < 1 {
			t.Fatalf("iteration %d: expected at least 1 question", i)
		}
		metric := ""
		for candidate := range wantCorrect {
			if strings.Contains(qs[0].Stem, candidate) {
				metric = candidate
				break
			}
		}
		if metric == "" {
			t.Fatalf("iteration %d: stem does not ask about some/full: %q", i, qs[0].Stem)
		}
		if qs[0].Correct != wantCorrect[metric] {
			t.Fatalf("iteration %d: metric %q had correct answer %q, want %q", i, metric, qs[0].Correct, wantCorrect[metric])
		}
		seen[metric] = true
	}
	for _, want := range []string{"`some`", "`full`"} {
		if !seen[want] {
			t.Errorf("metric %q never appeared across 100 iterations; saw %v", want, seen)
		}
	}
}

func TestPSIMemoryQuestionsUseObservedValues(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/pressure/memory", Output: samplePSIMemory}
	qs := psiMemoryQuestions(SystemInfo{}, c)
	var stems []string
	for _, q := range qs {
		stems = append(stems, q.Stem)
	}
	joined := strings.Join(stems, "\n")
	for _, want := range []string{"some avg10=4.20", "full avg10=0.80"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("PSI questions did not include observed value %q:\n%s", want, joined)
		}
	}
}

func TestPSRSSQuestionsUseObservedTopProcess(t *testing.T) {
	c := CapturedCommand{Cmd: "ps -eo pid,rss,comm --sort=-rss | head -10", Output: `    PID   RSS COMMAND
   2576 1466312 qemu-system-x86
 475978 862308 k3s-server
`}
	qs := psRssQuestions(SystemInfo{}, c)
	var stems []string
	for _, q := range qs {
		stems = append(stems, q.Stem)
	}
	joined := strings.Join(stems, "\n")
	for _, want := range []string{"qemu-system-x86", "1.4 GiB"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RSS questions did not include observed value %q:\n%s", want, joined)
		}
	}
}
