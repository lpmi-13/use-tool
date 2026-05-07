package main

import (
	"strings"
	"testing"
)

// Modern sysstat (12+) iostat -xz output: r_await/w_await separate, aqu-sz.
const sampleIostatModern = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (4 CPU)

avg-cpu:  %user   %nice %system %iowait  %steal   %idle
           5.20    0.00    1.50    2.10    0.00   91.20

Device            r/s     w/s     rkB/s     wkB/s   rrqm/s   wrqm/s  %rrqm  %wrqm r_await w_await aqu-sz rareq-sz wareq-sz  svctm  %util
sda             10.50   25.30    420.00    802.40     0.00     1.20   0.00   4.50    8.45   12.62   0.32    40.00    31.74   0.21  85.50
nvme0n1        500.00 1200.00  20000.00  48000.00     0.00     0.00   0.00   0.00    0.20    0.30   0.50    40.00    40.00   0.10  98.20`

// Older sysstat: combined await, avgqu-sz.
const sampleIostatOlder = `Linux 4.15.0 (host)  10/05/2024  _x86_64_  (2 CPU)

Device:         rrqm/s   wrqm/s     r/s     w/s    rkB/s    wkB/s avgrq-sz avgqu-sz   await r_await w_await  svctm  %util
sda               0.00     1.20   10.50   25.30   420.00   802.40    68.30     1.45   55.00    8.45   12.62   0.21  82.50`

// "Saturated rotational" pattern: high aqu-sz, high await, %util near 100.
const sampleIostatSaturated = `Linux 6.1.0 (host)  10/05/2024  _x86_64_  (2 CPU)

avg-cpu:  %user   %nice %system %iowait  %steal   %idle
           1.00    0.00    0.50   45.00    0.00   53.50

Device            r/s     w/s     rkB/s     wkB/s   rrqm/s   wrqm/s  %rrqm  %wrqm r_await w_await aqu-sz rareq-sz wareq-sz  svctm  %util
sda            120.00   80.00   8000.00   6000.00     0.00     0.00   0.00   0.00   55.00   65.00  12.00    66.67    75.00   0.50  98.50`

const samplePSIIO = `some avg10=1.20 avg60=0.80 avg300=0.40 total=12345
full avg10=0.30 avg60=0.10 avg300=0.05 total=2345`

const sampleDmesgIOError = `[Tue Oct  5 14:00:00 2024] sd 0:0:0:0: [sda] tag#42 FAILED Result: hostbyte=DID_OK driverbyte=DRIVER_SENSE
[Tue Oct  5 14:00:00 2024] blk_update_request: I/O error, dev sda, sector 12345
[Tue Oct  5 14:00:00 2024] EXT4-fs error (device sda1): ext4_journal_check_start:84
[Tue Oct  5 14:00:00 2024] EXT4-fs (sda1): Remounting filesystem read-only
some unrelated noise that should not match`

const sampleLsblk = `NAME        MAJ:MIN RM   SIZE RO TYPE MOUNTPOINTS
sda           8:0    0   1.8T  0 disk
├─sda1        8:1    0   512M  0 part /boot/efi
└─sda2        8:2    0   1.8T  0 part /
nvme0n1     259:0    0 953.9G  0 disk
└─nvme0n1p1 259:1    0 953.9G  0 part /data`

func TestParseIostatModern(t *testing.T) {
	rows := parseIostat(sampleIostatModern)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	devices := map[string]iostatRow{}
	for _, r := range rows {
		devices[r.Device] = r
	}
	sda, ok := devices["sda"]
	if !ok {
		t.Fatal("sda row missing")
	}
	if sda.Util != 85.50 {
		t.Errorf("sda %%util: got %v, want 85.50", sda.Util)
	}
	if sda.RPerS != 10.50 || sda.WPerS != 25.30 {
		t.Errorf("sda r/s,w/s: got %v,%v want 10.50,25.30", sda.RPerS, sda.WPerS)
	}
	if sda.AquSz != 0.32 {
		t.Errorf("sda aqu-sz: got %v, want 0.32", sda.AquSz)
	}
	// No combined `await`; await should be max(r_await, w_await) = 12.62
	if sda.Await != 12.62 {
		t.Errorf("sda await (max of r/w): got %v, want 12.62", sda.Await)
	}

	nvme, ok := devices["nvme0n1"]
	if !ok {
		t.Fatal("nvme0n1 row missing")
	}
	if nvme.Util != 98.20 {
		t.Errorf("nvme0n1 %%util: got %v, want 98.20", nvme.Util)
	}
}

func TestParseIostatOlderSysstat(t *testing.T) {
	rows := parseIostat(sampleIostatOlder)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Device != "sda" {
		t.Errorf("device: got %q, want sda", r.Device)
	}
	if r.Util != 82.50 {
		t.Errorf("util: got %v, want 82.50", r.Util)
	}
	// Combined `await` is present and should win over r_await/w_await
	if r.Await != 55.00 {
		t.Errorf("await (combined): got %v, want 55.00", r.Await)
	}
	// avgqu-sz fallback should populate AquSz
	if r.AquSz != 1.45 {
		t.Errorf("aqu-sz (avgqu-sz fallback): got %v, want 1.45", r.AquSz)
	}
}

func TestExtractIostatMaxUtil(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "iostat -xz 1 1", Output: sampleIostatModern}}
	v, ok := extractIostatMaxUtil(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 98.20 {
		t.Errorf("got %v, want 98.20", v.Number)
	}
	if v.Unit != "%" {
		t.Errorf("expected %% unit, got %q", v.Unit)
	}
}

func TestExtractIostatMaxAquSz(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "iostat -xz 1 1", Output: sampleIostatSaturated}}
	v, ok := extractIostatMaxAquSz(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 12.00 {
		t.Errorf("got %v, want 12.00", v.Number)
	}
}

func TestExtractIostatMaxAwait(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "iostat -xz 1 1", Output: sampleIostatSaturated}}
	v, ok := extractIostatMaxAwait(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 65.00 {
		t.Errorf("got %v, want 65.00", v.Number)
	}
	if v.Unit != " ms" {
		t.Errorf("expected ms unit, got %q", v.Unit)
	}
}

func TestExtractIostatPeakIOPS(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "iostat -xz 1 1", Output: sampleIostatModern}}
	v, ok := extractIostatPeakIOPS(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// nvme: 500 + 1200 = 1700
	if v.Number != 1700 {
		t.Errorf("got %v, want 1700", v.Number)
	}
}

func TestExtractIostatRequiresIostatCommand(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "echo", Output: sampleIostatModern}}
	if _, ok := extractIostatMaxUtil(SystemInfo{}, caps); ok {
		t.Error("expected extraction to fail when output is not from iostat")
	}
}

func TestIostatQuestionsFires(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "iostat -xz 1 1", Output: sampleIostatModern}
	qs := iostatQuestions(si, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for iostat output")
	}
}

func TestIostatQuestionsRejectsUnrelated(t *testing.T) {
	si := SystemInfo{}
	c := CapturedCommand{Cmd: "iostat", Output: "no extended header here"}
	if qs := iostatQuestions(si, c); qs != nil {
		t.Error("expected nil for output without %util header")
	}
}

func TestPSIIOExtractRequiresCorrectPath(t *testing.T) {
	caps := []CapturedCommand{
		// Same content shape as PSI io but path is /proc/pressure/memory:
		// disk extractor must NOT pick this up.
		{Cmd: "cat /proc/pressure/memory", Output: samplePSIIO},
	}
	if _, ok := extractPSIIO("some")(SystemInfo{}, caps); ok {
		t.Error("expected PSI io extractor to ignore /proc/pressure/memory output")
	}
}

func TestPSIIOExtractSome(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/io", Output: samplePSIIO}}
	v, ok := extractPSIIO("some")(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 1.20 {
		t.Errorf("got %v, want 1.20", v.Number)
	}
}

func TestPSIIOExtractFull(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "cat /proc/pressure/io", Output: samplePSIIO}}
	v, ok := extractPSIIO("full")(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if v.Number != 0.30 {
		t.Errorf("got %v, want 0.30", v.Number)
	}
}

func TestPSIIOQuestionsRequiresPath(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/pressure/memory", Output: samplePSIIO}
	if qs := psiIOQuestions(SystemInfo{}, c); qs != nil {
		t.Error("expected no PSI io questions for /proc/pressure/memory path")
	}
}

func TestPSIIOQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "cat /proc/pressure/io", Output: samplePSIIO}
	qs := psiIOQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected PSI io questions")
	}
}

func TestExtractDmesgIOErrorsCounts(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "dmesg", Output: sampleDmesgIOError}}
	v, ok := extractDmesgIOErrors(SystemInfo{}, caps)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	// 3 of 5 lines mention an I/O-error keyword: blk_update_request line,
	// EXT4-fs error line, and the read-only remount line. The "FAILED Result"
	// line and the "unrelated noise" line don't match.
	if !strings.Contains(v.Text, "3/5") {
		t.Errorf("expected 3/5 in text, got %q", v.Text)
	}
}

func TestExtractDmesgIOErrorsRequiresDmesgBaseCmd(t *testing.T) {
	caps := []CapturedCommand{{Cmd: "vim dmesg.txt", Output: sampleDmesgIOError}}
	if _, ok := extractDmesgIOErrors(SystemInfo{}, caps); ok {
		t.Error("expected extraction to reject vim dmesg.txt as not-actually-dmesg")
	}
}

func TestDiskDmesgQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "dmesg", Output: sampleDmesgIOError}
	qs := diskDmesgQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for I/O error output")
	}
}

func TestDiskDmesgQuestionsSkipsBenign(t *testing.T) {
	c := CapturedCommand{Cmd: "dmesg", Output: "[Tue Oct  5] kernel boot complete"}
	if qs := diskDmesgQuestions(SystemInfo{}, c); qs != nil {
		t.Error("expected no questions for benign dmesg output")
	}
}

func TestLsblkQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "lsblk", Output: sampleLsblk}
	qs := lsblkQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for lsblk output")
	}
}

func TestPidstatDQuestionsFires(t *testing.T) {
	c := CapturedCommand{Cmd: "pidstat -d 1 1", Output: `
Linux 6.1.0  10/05/2024
14:00:00       UID       PID   kB_rd/s   kB_wr/s kB_ccwr/s iodelay  Command
14:00:01      1000      1234     12.50    400.00      0.00       2  postgres
`}
	qs := pidstatDQuestions(SystemInfo{}, c)
	if len(qs) == 0 {
		t.Fatal("expected questions for pidstat -d output")
	}
}

func TestUtilAwaitConsistencySSDPattern(t *testing.T) {
	// SSD-like: %util high, queue low, await low.
	vs := map[string]Value{
		"iostat_max_util_pct":  {Number: 98},
		"iostat_max_aqu_sz":    {Number: 0.5},
		"iostat_max_await_ms":  {Number: 1.2},
	}
	q, ok := utilAwaitConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Busy but not saturated") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestUtilAwaitConsistencyTrueSaturation(t *testing.T) {
	vs := map[string]Value{
		"iostat_max_util_pct":  {Number: 99},
		"iostat_max_aqu_sz":    {Number: 12},
		"iostat_max_await_ms":  {Number: 65},
	}
	q, ok := utilAwaitConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Saturated") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestUtilAwaitConsistencyHeadroom(t *testing.T) {
	vs := map[string]Value{
		"iostat_max_util_pct":  {Number: 20},
		"iostat_max_aqu_sz":    {Number: 0.1},
		"iostat_max_await_ms":  {Number: 0.8},
	}
	q, ok := utilAwaitConsistency.Generate(SystemInfo{}, vs)
	if !ok {
		t.Fatal("expected synthesis to fire")
	}
	if !strings.Contains(q.Correct, "Headroom") {
		t.Errorf("wrong branch: %q", q.Correct)
	}
}

func TestUtilAwaitConsistencyAmbiguousReturnsFalse(t *testing.T) {
	// In between: not clearly saturated, not clearly idle, not clearly SSD-pattern.
	vs := map[string]Value{
		"iostat_max_util_pct":  {Number: 70},
		"iostat_max_aqu_sz":    {Number: 2.5},
		"iostat_max_await_ms":  {Number: 8},
	}
	if _, ok := utilAwaitConsistency.Generate(SystemInfo{}, vs); ok {
		t.Error("expected synthesis to skip the ambiguous middle case")
	}
}

func TestDiskInvestigationRegistered(t *testing.T) {
	inv, err := getInvestigation("disk")
	if err != nil {
		t.Fatalf("disk investigation not registered: %v", err)
	}
	if inv.Name != "disk" {
		t.Errorf("unexpected name %q", inv.Name)
	}
	if len(inv.Observations) == 0 || len(inv.Commands) == 0 || len(inv.Extractors) == 0 {
		t.Error("disk investigation is missing observations/commands/extractors")
	}
}
