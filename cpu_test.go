package main

import (
	"strings"
	"testing"
)

const sampleUptime = ` 14:32:01 up 7 days,  3:14,  2 users,  load average: 1.42, 0.93, 0.71`

const sampleVmstat = `procs -----------memory---------- ---swap-- -----io---- -system-- ------cpu-----
 r  b   swpd   free   buff  cache   si   so    bi    bo   in   cs us sy id wa st
 2  0      0 123456  78901 234567    0    0    12     8  120  330  3  1 95  1  0
 3  1      0 123100  78920 234600    0    0     0   200  150  410  8  2 80 10  0
 1  0      0 123080  78930 234610    0    0     0     0  130  290  2  1 96  1  0`

const sampleMpstat = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

14:32:00     CPU    %usr   %nice    %sys %iowait    %irq   %soft  %steal  %guest  %gnice   %idle
14:32:01     all   12.50    0.00    3.00    1.50    0.00    0.25    0.00    0.00    0.00   82.75
14:32:01       0   25.00    0.00    4.00    1.00    0.00    0.50    0.00    0.00    0.00   69.50
14:32:01       1    8.00    0.00    2.00    1.00    0.00    0.10    0.00    0.00    0.00   88.90
14:32:01       2    5.00    0.00    3.00    2.00    0.00    0.00    0.00    0.00    0.00   90.00
14:32:01       3   12.00    0.00    3.00    2.00    0.00    0.40    0.00    0.00    0.00   82.60`

const sampleDmesgWithMCE = `[Tue Oct  5 14:00:00 2024] mce: [Hardware Error]: CPU 2: Machine Check Exception
[Tue Oct  5 14:00:00 2024] mce: [Hardware Error]: TSC 0 ADDR 1234 MISC 5678
[Tue Oct  5 14:01:00 2024] thermal_throttle: CPU2 above threshold`

func TestUptimeQuestionsExtractsLoadavg(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "uptime", Output: sampleUptime}
	qs := uptimeQuestions(si, c)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	if !strings.Contains(qs[0].Stem, "1.42") || !strings.Contains(qs[0].Stem, "0.93") {
		t.Errorf("stem missing loadavg numbers: %q", qs[0].Stem)
	}
	if qs[0].Correct != "The 5-minute load average" {
		t.Errorf("unexpected correct answer: %q", qs[0].Correct)
	}
}

func TestUptimeQuestionsRejectsUnrelatedOutput(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "echo", Output: "hello world"}
	if qs := uptimeQuestions(si, c); qs != nil {
		t.Errorf("expected nil for non-uptime output, got %d", len(qs))
	}
}

func TestExtractLoadavgFirstField(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	caps := []CapturedCommand{{Cmd: "uptime", Output: sampleUptime}}
	v, ok := extractLoadavgN(0)(si, caps)
	if !ok {
		t.Fatal("expected loadavg extraction to succeed")
	}
	if v.Number != 1.42 {
		t.Errorf("expected 1.42, got %v", v.Number)
	}
	if !strings.Contains(v.Note, "NumCPU=4") {
		t.Errorf("note missing NumCPU: %q", v.Note)
	}
}

func TestExtractLoadavgMissing(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	caps := []CapturedCommand{{Cmd: "echo", Output: "no loadavg here"}}
	if _, ok := extractLoadavgN(0)(si, caps); ok {
		t.Error("expected extraction to fail without uptime output")
	}
}

func TestExtractVmstatColumnRunQueue(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	caps := []CapturedCommand{{Cmd: "vmstat 1 3", Output: sampleVmstat}}
	v, ok := extractVmstatColumn("r")(si, caps)
	if !ok {
		t.Fatal("expected vmstat r extraction to succeed")
	}
	want := []float64{2, 3, 1}
	if len(v.Samples) != len(want) {
		t.Fatalf("expected %d samples, got %d", len(want), len(v.Samples))
	}
	for i, w := range want {
		if v.Samples[i] != w {
			t.Errorf("sample[%d]: expected %v, got %v", i, w, v.Samples[i])
		}
	}
}

func TestExtractVmstatColumnIOWait(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	caps := []CapturedCommand{{Cmd: "vmstat 1 3", Output: sampleVmstat}}
	v, ok := extractVmstatColumn("wa")(si, caps)
	if !ok {
		t.Fatal("expected vmstat wa extraction to succeed")
	}
	if v.Unit != "%" {
		t.Errorf("expected %% unit, got %q", v.Unit)
	}
	want := []float64{1, 10, 1}
	for i, w := range want {
		if v.Samples[i] != w {
			t.Errorf("sample[%d]: expected %v, got %v", i, w, v.Samples[i])
		}
	}
}

func TestExtractMpstatIdleSamples(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "mpstat -P ALL 1 1", Output: sampleMpstat}}
	samples := extractMpstatIdleSamples(caps)
	// Should pick up the four per-CPU rows, skipping the "all" row
	if len(samples) != 4 {
		t.Fatalf("expected 4 per-CPU samples, got %d (samples=%v)", len(samples), samples)
	}
	wantSet := map[float64]bool{69.50: true, 88.90: true, 90.00: true, 82.60: true}
	for _, s := range samples {
		if !wantSet[s] {
			t.Errorf("unexpected sample: %v", s)
		}
	}
}

func TestExtractDmesgCpuKeywordsCounts(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "dmesg", Output: sampleDmesgWithMCE}}
	v, ok := extractDmesgCpuKeywords(si, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// All three lines mention an MCE/thermal keyword
	if !strings.Contains(v.Text, "3/3") {
		t.Errorf("expected 3/3 in text, got %q", v.Text)
	}
}

func TestExtractDmesgCpuKeywordsRequiresDmesgCommand(t *testing.T) {
	si := SystemInfo{}
	caps := []CapturedCommand{{Cmd: "echo", Output: sampleDmesgWithMCE}}
	if _, ok := extractDmesgCpuKeywords(si, caps); ok {
		t.Error("expected extraction to fail when no dmesg command was captured")
	}
}

func TestExtractDmesgCpuKeywordsIgnoresFalsePositiveCmds(t *testing.T) {
	si := SystemInfo{}
	cases := []string{
		"vim dmesg.txt",
		"cat /etc/dmesg.conf",
		"grep dmesg notes.md",
		"echo journalctl",
	}
	for _, cmd := range cases {
		caps := []CapturedCommand{{Cmd: cmd, Output: sampleDmesgWithMCE}}
		if _, ok := extractDmesgCpuKeywords(si, caps); ok {
			t.Errorf("expected %q not to match dmesg/journalctl", cmd)
		}
	}
}

func TestExtractDmesgCpuKeywordsAcceptsSudoAndPipelines(t *testing.T) {
	si := SystemInfo{}
	cases := []string{
		"dmesg",
		"dmesg -T | tail -50",
		"sudo dmesg --level=err,warn",
		"journalctl -k -p err",
	}
	for _, cmd := range cases {
		caps := []CapturedCommand{{Cmd: cmd, Output: sampleDmesgWithMCE}}
		if _, ok := extractDmesgCpuKeywords(si, caps); !ok {
			t.Errorf("expected %q to match dmesg/journalctl", cmd)
		}
	}
}

func TestBaseCmd(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"dmesg", "dmesg"},
		{"dmesg -T | tail", "dmesg"},
		{"sudo dmesg", "dmesg"},
		{"sudo", "sudo"}, // bare sudo with no following command
		{"vim dmesg.txt", "vim"},
		{"cat /etc/dmesg.conf", "cat"},
	}
	for _, tc := range cases {
		if got := baseCmd(tc.in); got != tc.want {
			t.Errorf("baseCmd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDmesgQuestionsFiresOnMCE(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "dmesg", Output: sampleDmesgWithMCE}
	qs := dmesgQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected at least one question for MCE output")
	}
	found := false
	for _, q := range qs {
		if strings.Contains(q.Stem, "machine-check") || strings.Contains(q.Stem, "MCE") {
			found = true
		}
	}
	if !found {
		t.Errorf("no MCE-related question generated; got: %+v", qs)
	}
}

func TestExtractVmstatColumnSteal(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	caps := []CapturedCommand{{Cmd: "vmstat 1 3", Output: sampleVmstat}}
	v, ok := extractVmstatColumn("st")(si, caps)
	if !ok {
		t.Fatal("expected vmstat st extraction to succeed")
	}
	if v.Unit != "%" {
		t.Errorf("expected %% unit, got %q", v.Unit)
	}
	want := []float64{0, 0, 0}
	for i, w := range want {
		if v.Samples[i] != w {
			t.Errorf("sample[%d]: expected %v, got %v", i, w, v.Samples[i])
		}
	}
}

func TestVmstatStealRecallEmpty(t *testing.T) {
	if qs := vmstatStealRecall(Value{}); qs != nil {
		t.Errorf("expected nil for empty samples, got %d questions", len(qs))
	}
}

func TestStealVsIowaitNoisyNeighbour(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_wa": {Samples: []float64{1, 2, 1}},
		"vmstat_st": {Samples: []float64{8, 10, 9}},
	}
	q, ok := stealVsIowaitDistinction.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "hypervisor contention") || !strings.Contains(q.Correct, "noisy neighbour") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestStealVsIowaitGenuineIO(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_wa": {Samples: []float64{12, 18, 15}},
		"vmstat_st": {Samples: []float64{0, 0, 0}},
	}
	q, ok := stealVsIowaitDistinction.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "genuine I/O wait") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestStealVsIowaitBothElevated(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_wa": {Samples: []float64{15, 18, 12}},
		"vmstat_st": {Samples: []float64{6, 8, 7}},
	}
	q, ok := stealVsIowaitDistinction.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Both are elevated") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestStealVsIowaitBothLow(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_wa": {Samples: []float64{1, 2, 0}},
		"vmstat_st": {Samples: []float64{0, 0, 0}},
	}
	q, ok := stealVsIowaitDistinction.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Both are low") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestStealVsIowaitAmbiguousReturnsFalse(t *testing.T) {
	si := SystemInfo{}
	vs := map[string]Value{
		"vmstat_wa": {Samples: []float64{6, 7, 5}},
		"vmstat_st": {Samples: []float64{2, 3, 2}},
	}
	if _, ok := stealVsIowaitDistinction.Generate(si, vs); ok {
		t.Error("expected synthesis to skip the ambiguous middle case")
	}
}

func TestLoadavgIdleSynthesisLightLoad(t *testing.T) {
	si := SystemInfo{NumCPU: 8}
	vs := map[string]Value{
		"loadavg_1min":     {Number: 1.0},
		"mpstat_idle_mean": {Number: 85},
	}
	q, ok := loadavgIdleConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire for light load")
	}
	if !strings.Contains(q.Correct, "lightly loaded") {
		t.Errorf("wrong branch chosen: %q", q.Correct)
	}
}

func TestLoadavgIdleSynthesisSaturated(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	vs := map[string]Value{
		"loadavg_1min":     {Number: 6.0},
		"mpstat_idle_mean": {Number: 10},
	}
	q, ok := loadavgIdleConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire for saturation")
	}
	if !strings.Contains(q.Correct, "saturated") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestLoadavgIdleSynthesisInconsistent(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	vs := map[string]Value{
		"loadavg_1min":     {Number: 5.0},
		"mpstat_idle_mean": {Number: 90},
	}
	q, ok := loadavgIdleConsistency.Generate(si, vs)
	if !ok {
		t.Fatal("expected synthesis to fire when readings disagree")
	}
	if !strings.Contains(q.Correct, "Inconsistent") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestLoadavgIdleSynthesisAmbiguousReturnsFalse(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	vs := map[string]Value{
		"loadavg_1min":     {Number: 2.0},
		"mpstat_idle_mean": {Number: 50},
	}
	if _, ok := loadavgIdleConsistency.Generate(si, vs); ok {
		t.Error("expected synthesis to skip the ambiguous middle case")
	}
}
