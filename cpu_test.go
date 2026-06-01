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
	// Position is randomized, so the correct answer can be any of the three.
	valid := map[string]bool{
		"The 1-minute load average":  true,
		"The 5-minute load average":  true,
		"The 15-minute load average": true,
	}
	if !valid[qs[0].Correct] {
		t.Errorf("correct answer not in expected set: %q", qs[0].Correct)
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
	// vmstat's first interval row is since-boot stats; the extractor drops
	// it, so only the two interval samples [3, 1] remain from the three-row
	// fixture.
	want := []float64{3, 1}
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
	// First row dropped as since-boot; only interval samples remain.
	want := []float64{10, 1}
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

func TestBaseCmdStripsSudoAndArgs(t *testing.T) {
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

func TestDmesgQuestionsNamesCapturedTool(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{name: "dmesg", cmd: "dmesg --level=err,warn | tail -20", want: "`dmesg` output"},
		{name: "sudo dmesg", cmd: "sudo dmesg --level=err,warn", want: "`dmesg` output"},
		{name: "journalctl", cmd: "journalctl -k -b -p warning --no-pager -n 30", want: "`journalctl` output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs := dmesgQuestions(SystemInfo{}, CapturedCommand{Cmd: tc.cmd, Output: sampleDmesgWithMCE})
			if len(qs) == 0 {
				t.Fatal("expected question")
			}
			if !strings.Contains(qs[0].Stem, tc.want) {
				t.Fatalf("stem = %q, want it to contain %q", qs[0].Stem, tc.want)
			}
		})
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
	// First row dropped as since-boot; only interval samples remain.
	want := []float64{0, 0}
	for i, w := range want {
		if v.Samples[i] != w {
			t.Errorf("sample[%d]: expected %v, got %v", i, w, v.Samples[i])
		}
	}
}

// ----- recall edge cases -----

func TestUptimeQuestionsAnswerableWhenAllLoadavgsEqual(t *testing.T) {
	// All three loadavgs are 0.00 — historically this made the question
	// "what does the value 0.00 refer to?" ambiguous. Position-based phrasing
	// keeps it answerable regardless of which position is randomly picked.
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{
		Cmd:    "uptime",
		Output: " 14:32:01 up 1 min,  1 user,  load average: 0.00, 0.00, 0.00",
	}
	qs := uptimeQuestions(si, c)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	usesPosition := strings.Contains(qs[0].Stem, "first") ||
		strings.Contains(qs[0].Stem, "middle") ||
		strings.Contains(qs[0].Stem, "last")
	if !usesPosition {
		t.Errorf("expected stem to use position-based language (first/middle/last); got %q", qs[0].Stem)
	}
	valid := map[string]bool{
		"The 1-minute load average":  true,
		"The 5-minute load average":  true,
		"The 15-minute load average": true,
	}
	if !valid[qs[0].Correct] {
		t.Errorf("correct answer not in expected set: %q", qs[0].Correct)
	}
}

func TestUptimeQuestionsCoversAllPositions(t *testing.T) {
	// Run many iterations and confirm that the random pick asks about all three
	// positions over time. This checks the stem wording, not just the correct
	// answer, so it catches regressions where the guide always asks about the
	// first load average.
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "uptime", Output: sampleUptime}
	wantCorrect := map[string]string{
		"first":  "The 1-minute load average",
		"middle": "The 5-minute load average",
		"last":   "The 15-minute load average",
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		qs := uptimeQuestions(si, c)
		if len(qs) != 1 {
			t.Fatalf("iteration %d: expected 1 question, got %d", i, len(qs))
		}
		position := ""
		for candidate := range wantCorrect {
			if strings.Contains(qs[0].Stem, candidate) {
				position = candidate
				break
			}
		}
		if position == "" {
			t.Fatalf("iteration %d: stem does not ask about first/middle/last: %q", i, qs[0].Stem)
		}
		if qs[0].Correct != wantCorrect[position] {
			t.Fatalf("iteration %d: position %q had correct answer %q, want %q", i, position, qs[0].Correct, wantCorrect[position])
		}
		seen[position] = true
	}
	for _, want := range []string{"first", "middle", "last"} {
		if !seen[want] {
			t.Errorf("position %q never appeared across 100 iterations; saw %v", want, seen)
		}
	}
}

const sampleW = ` 11:24:12 up 47 days,  3:42,  3 users,  load average: 0.42, 0.31, 0.28
USER     TTY      FROM             LOGIN@   IDLE   JCPU   PCPU WHAT
adam     pts/0    -                09:00    0.00s  1:34   0.05s ssh user@host
root     pts/1    192.168.1.5      08:55   12:01   0:02   0.00s -bash`

const sampleProcLoadavg = `0.42 0.31 0.28 1/234 5678`

const sampleProcPressureCpu = `some avg10=0.12 avg60=0.05 avg300=0.01 total=12345678
full avg10=0.00 avg60=0.00 avg300=0.00 total=0`

const sampleSarU = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

12:00:00        CPU     %user     %nice   %system   %iowait    %steal     %idle
12:00:01        all      5.00      0.00      2.00      1.00      0.00     92.00
12:00:02        all      4.00      0.00      1.00      0.00      0.00     95.00
12:00:03        all      6.00      0.00      3.00      0.00      0.00     91.00
Average:        all      5.00      0.00      2.00      0.33      0.00     92.67`

func TestWQuestionsCoversJCPUAndPCPU(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "w", Output: sampleW}
	qs := wQuestions(si, c)
	if len(qs) < 3 {
		t.Fatalf("expected at least 3 questions (JCPU, PCPU, loadavg), got %d", len(qs))
	}
	wantStemFragments := []string{"JCPU", "PCPU"}
	for _, frag := range wantStemFragments {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, frag) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a question whose stem contains %q; pool was %v", frag, stems(qs))
		}
	}
	// Also confirm the loadavg-position question made it in (shared with uptime).
	loadavgQ := false
	for _, q := range qs {
		if strings.Contains(q.Stem, "load average:") {
			loadavgQ = true
			break
		}
	}
	if !loadavgQ {
		t.Errorf("expected loadavg-position question to be one of the candidates; pool was %v", stems(qs))
	}
}

func TestWQuestionsRejectsUptimeOnly(t *testing.T) {
	// `uptime` output also has a `load average:` line but no session table —
	// wQuestions should decline so combineVariantQuestions falls through.
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "uptime", Output: sampleUptime}
	if qs := wQuestions(si, c); qs != nil {
		t.Errorf("expected nil for uptime-only output, got %d questions", len(qs))
	}
}

func TestWQuestionsRejectsUnrelatedOutput(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "echo", Output: "hello world"}
	if qs := wQuestions(si, c); qs != nil {
		t.Errorf("expected nil for unrelated output, got %d questions", len(qs))
	}
}

func TestProcLoadavgQuestionsExtractsFields(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/loadavg", Output: sampleProcLoadavg}
	qs := procLoadavgQuestions(SystemInfo{NumCPU: 4}, c)
	if len(qs) != 2 {
		t.Fatalf("expected 2 questions (running/total and last PID), got %d", len(qs))
	}
	// First question should embed the running/total field as `1/234`.
	if !strings.Contains(qs[0].Stem, "1/234") {
		t.Errorf("first question stem missing `1/234`: %q", qs[0].Stem)
	}
	// Second question should embed the last PID.
	if !strings.Contains(qs[1].Stem, "5678") {
		t.Errorf("second question stem missing `5678`: %q", qs[1].Stem)
	}
	// Critically: neither question should be the load-average-position question
	// that uptimeQuestions already covers.
	for _, q := range qs {
		if strings.Contains(q.Stem, "first") && strings.Contains(q.Stem, "load average") {
			t.Errorf("procLoadavgQuestions should not ask the loadavg-position question; got %q", q.Stem)
		}
	}
}

func TestProcLoadavgQuestionsRejectsUnrelatedOutput(t *testing.T) {
	cases := []string{
		"",
		sampleUptime, // has `load average:` text but not the bare /proc format
		"hello world",
		"0.42 0.31 0.28", // missing the running/total and last PID
	}
	for _, out := range cases {
		c := CapturedCommand{Cmd: "cat /proc/loadavg", Output: out}
		if qs := procLoadavgQuestions(SystemInfo{}, c); qs != nil {
			t.Errorf("expected nil for %q, got %d questions", out, len(qs))
		}
	}
}

func TestProcPressureCpuQuestionsReturnsAllThree(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "cat /proc/pressure/cpu", Output: sampleProcPressureCpu}
	qs := procPressureCpuQuestions(si, c)
	if len(qs) != 3 {
		t.Fatalf("expected 3 questions (avg10, full vs some, total), got %d", len(qs))
	}
	// Confirm each distinct concept is covered exactly once.
	wantFragments := []string{"avg10", "full", "total="}
	for _, frag := range wantFragments {
		found := 0
		for _, q := range qs {
			if strings.Contains(q.Stem, frag) {
				found++
			}
		}
		if found != 1 {
			t.Errorf("expected exactly 1 question containing %q; got %d. Pool: %v", frag, found, stems(qs))
		}
	}
	for _, q := range qs {
		if strings.Contains(q.Stem, "show only a `some` row") {
			t.Errorf("CPU PSI question still assumes the full row is absent: %q", q.Stem)
		}
	}
}

func TestProcPressureCpuQuestionsHandlesSomeOnlyOutput(t *testing.T) {
	c := CapturedCommand{
		Cmd:    "cat /proc/pressure/cpu",
		Output: "some avg10=0.12 avg60=0.05 avg300=0.01 total=12345678",
	}
	qs := procPressureCpuQuestions(SystemInfo{}, c)
	if len(qs) != 3 {
		t.Fatalf("expected 3 questions for some-only CPU PSI output, got %d", len(qs))
	}
	foundCompatQuestion := false
	for _, q := range qs {
		if strings.Contains(q.Stem, "omit a `full` row") {
			foundCompatQuestion = true
		}
	}
	if !foundCompatQuestion {
		t.Fatalf("expected compatibility wording for some-only output; got %v", stems(qs))
	}
}

func TestExtractPSICPUSomeAvg10(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/cpu", Output: sampleProcPressureCpu}}
	v, ok := extractPSIAvg10("/proc/pressure/cpu", "some")(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected CPU PSI extraction to succeed")
	}
	if v.Number != 0.12 || v.Unit != "%" {
		t.Fatalf("CPU PSI value = %+v, want 0.12%%", v)
	}
	if got := verdictPSISome(SystemInfo{}, v, Snapshot{}); got != SignalModerate {
		t.Fatalf("CPU PSI verdict = %v, want SignalModerate", got)
	}
}

func TestProcPressureCpuQuestionsRejectsUnrelatedOutput(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/pressure/io", Output: sampleProcLoadavg}
	if qs := procPressureCpuQuestions(SystemInfo{}, c); qs != nil {
		t.Errorf("expected nil for non-PSI output, got %d questions", len(qs))
	}
}

func TestSarUColumnQuestionsCoversAllColumns(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}
	qs := sarUColumnQuestions(si, c)
	wantColumns := []string{"%user", "%nice", "%system", "%iowait", "%steal", "%idle"}
	if len(qs) != len(wantColumns) {
		t.Fatalf("expected %d questions, got %d", len(wantColumns), len(qs))
	}
	for _, col := range wantColumns {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, col) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a question for column %q; got %v", col, stems(qs))
		}
	}
}

func TestMpstatColumnQuestionsFocusesUSEColumns(t *testing.T) {
	si := SystemInfo{NumCPU: 4}
	c := CapturedCommand{Cmd: "mpstat -P ALL 1 3", Output: sampleMpstat}
	qs := mpstatColumnQuestions(si, c)
	wantColumns := []string{"%usr", "%sys", "%iowait", "%irq", "%soft", "%steal", "%idle"}
	if len(qs) != len(wantColumns) {
		t.Fatalf("expected %d questions, got %d: %v", len(wantColumns), len(qs), stems(qs))
	}
	for _, col := range wantColumns {
		found := false
		for _, q := range qs {
			if strings.Contains(q.Stem, col) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a question for column %q; got %v", col, stems(qs))
		}
	}

	skippedColumns := []string{"CPU", "%nice", "%guest", "%gnice"}
	for _, col := range skippedColumns {
		for _, q := range qs {
			if strings.Contains(q.Stem, col) {
				t.Errorf("did not expect a question for column %q; got %q", col, q.Stem)
			}
		}
	}
}

func TestPickStepVariantFiltersByAvailable(t *testing.T) {
	// PSI-only — the only available variant must be picked every time.
	siPSIOnly := SystemInfo{HasPSI: true}
	variants := []stepVariant{
		{Cmd: "vmstat 1 3", QuestionsFn: vmstatColumnQuestions},
		{Cmd: "cat /proc/pressure/cpu", QuestionsFn: procPressureCpuQuestions, Available: func(si SystemInfo) bool { return si.HasPSI }},
		{Cmd: "sar -u 1 3", QuestionsFn: sarUColumnQuestions, Available: func(si SystemInfo) bool { return si.HasSar }},
	}
	for i := 0; i < 20; i++ {
		pick := pickStepVariant(siPSIOnly, variants)
		if pick.Cmd == "sar -u 1 3" {
			t.Fatalf("iteration %d: pickStepVariant returned unavailable variant %q", i, pick.Cmd)
		}
	}

	// Sar-only — same, with the other gated variant ruled out.
	siSarOnly := SystemInfo{HasSar: true}
	for i := 0; i < 20; i++ {
		pick := pickStepVariant(siSarOnly, variants)
		if pick.Cmd == "cat /proc/pressure/cpu" {
			t.Fatalf("iteration %d: pickStepVariant returned unavailable variant %q", i, pick.Cmd)
		}
	}
}

func TestPickStepVariantReturnsAllAvailableOverTime(t *testing.T) {
	// With all three variants available, pickStepVariant should produce each
	// over enough iterations.
	si := SystemInfo{HasPSI: true, HasSar: true}
	variants := []stepVariant{
		{Cmd: "vmstat 1 3", QuestionsFn: vmstatColumnQuestions},
		{Cmd: "cat /proc/pressure/cpu", QuestionsFn: procPressureCpuQuestions, Available: func(si SystemInfo) bool { return si.HasPSI }},
		{Cmd: "sar -u 1 3", QuestionsFn: sarUColumnQuestions, Available: func(si SystemInfo) bool { return si.HasSar }},
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[pickStepVariant(si, variants).Cmd] = true
	}
	for _, want := range []string{"vmstat 1 3", "cat /proc/pressure/cpu", "sar -u 1 3"} {
		if !seen[want] {
			t.Errorf("variant %q never appeared across 200 iterations; saw %v", want, seen)
		}
	}
}

func TestCombineVariantQuestionsDispatchesByOutputFormat(t *testing.T) {
	// With wQuestions ordered before uptimeQuestions (the canonical case),
	// `w` output should produce a w-specific question, not a generic loadavg one.
	si := SystemInfo{NumCPU: 4}
	loadavgVariants := cpuLoadavgVariants()
	combined := combineVariantQuestions(loadavgVariants)

	// `w` output → wQuestions should win.
	qs := combined(si, CapturedCommand{Cmd: "w", Output: sampleW})
	if len(qs) == 0 {
		t.Fatal("expected questions for w output")
	}
	gotW := false
	for _, q := range qs {
		if strings.Contains(q.Stem, "JCPU") || strings.Contains(q.Stem, "PCPU") {
			gotW = true
			break
		}
	}
	if !gotW {
		t.Errorf("expected w-specific question for w output; got %v", stems(qs))
	}

	// `uptime` output → falls through to uptimeQuestions.
	qs = combined(si, CapturedCommand{Cmd: "uptime", Output: sampleUptime})
	if len(qs) != 1 {
		t.Fatalf("expected 1 question for uptime output, got %d", len(qs))
	}
	if !strings.Contains(qs[0].Stem, "load average:") {
		t.Errorf("expected loadavg position question for uptime; got %q", qs[0].Stem)
	}

	// `/proc/loadavg` output → procLoadavgQuestions.
	qs = combined(si, CapturedCommand{Cmd: "cat /proc/loadavg", Output: sampleProcLoadavg})
	if len(qs) == 0 {
		t.Fatal("expected questions for /proc/loadavg output")
	}
	if !strings.Contains(qs[0].Stem, "1/234") {
		t.Errorf("expected /proc/loadavg-specific question; got %q", qs[0].Stem)
	}
}

func stems(qs []Question) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.Stem
	}
	return out
}
