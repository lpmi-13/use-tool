package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var diskInvestigation = &Investigation{
	Name:  "disk",
	Title: "Disk I/O — Utilization, Saturation, Errors",
	Description: "Investigate disk I/O using Brendan Gregg's USE method.\n" +
		"Run commands at the prompt; the harness captures their output\n" +
		"and asks targeted questions about what you observed.",
	StepsFn:        diskSteps,
	Extractors:     diskExtractors,
	Observations:   diskObservations,
	SynthesisRules: diskSynthesisRules,
	Commands:       diskCommands,
}

// ----- Guide steps -----

func diskSteps(si SystemInfo) []GuideStep {
	steps := []GuideStep{
		{
			Name:      "devices",
			Intro:     "Step 1: orient yourself to what block devices the system has.",
			Suggested: "lsblk",
			AcceptAny: true,
			Teaching: "Note which devices are physical disks (TYPE=disk) vs partitions (part)\n" +
				"vs LVM volumes (lvm). Per-device metrics from iostat will use the disk\n" +
				"names you see here.",
		},
		{
			Name:        "throughput",
			Intro:       "Step 2: per-device throughput, queue depth, and latency.\niostat -xz is the workhorse: -x for extended columns, -z to skip idle devices.",
			Suggested:   "iostat -xz 1 3",
			QuestionsFn: iostatQuestions,
			Teaching: "Three signals matter most:\n" +
				"  • r/s + w/s — workload (the offered IOPS).\n" +
				"  • aqu-sz — average queue depth. Sustained > 1 means requests are\n" +
				"    queueing; that's saturation.\n" +
				"  • await — average request latency in ms. Compare to your device's\n" +
				"    expected baseline.\n" +
				"And one trap: %util on non-rotational devices (SSD, NVMe) is misleading\n" +
				"— it measures \"at least one I/O in flight,\" which doesn't reflect parallel\n" +
				"capacity. Read aqu-sz/await on those devices, not %util.",
		},
	}
	if si.HasPSI {
		steps = append(steps, GuideStep{
			Name:        "pressure",
			Intro:       "Step 3: PSI reports time-share of tasks stalled on I/O.",
			Suggested:   "cat /proc/pressure/io",
			QuestionsFn: psiIOQuestions,
			Teaching: "`some` is time at least one task was stalled on I/O; `full` is time\n" +
				"all non-idle tasks were stalled. Non-zero `full avg10` is the strongest\n" +
				"saturation signal — the system was actually wedged, not just busy.",
		})
	}
	steps = append(steps, GuideStep{
		Name:        "attribution",
		Intro:       "Step 4: which processes are doing the I/O?",
		Suggested:   "pidstat -d 1 3",
		QuestionsFn: pidstatDQuestions,
		AcceptAny:   true,
		Teaching: "kB_rd/s and kB_wr/s are per-process read/write rates from the kernel's\n" +
			"task accounting. If one process accounts for most of the device load,\n" +
			"that's where to look next.",
	})
	steps = append(steps, GuideStep{
		Name:        "errors",
		Intro:       "Step 5: kernel I/O errors. Filesystem-level errors are the smoking\ngun for failing media.",
		Suggested:   "dmesg -T 2>/dev/null | grep -iE 'i/o error|EXT4-fs error|XFS|Buffer I/O error|read-only' | tail",
		QuestionsFn: diskDmesgQuestions,
		AcceptAny:   true,
		Teaching: "Recurring `I/O error` lines or a kernel-initiated read-only remount\n" +
			"(`Remounting filesystem read-only`) indicate hardware that's failing or\n" +
			"already failed. Apps will see EROFS on writes from that point on.",
	})
	return steps
}

// ----- Comprehension extractors (per-command) -----

var diskExtractors = []Extractor{
	{BaseCmd: "iostat", QuestionsFn: iostatQuestions},
	{BaseCmd: "lsblk", QuestionsFn: lsblkQuestions},
	{BaseCmd: "pidstat", QuestionsFn: pidstatDQuestions},
	{BaseCmd: "cat", QuestionsFn: catDiskQuestions},
	{BaseCmd: "dmesg", QuestionsFn: diskDmesgQuestions},
	{BaseCmd: "journalctl", QuestionsFn: diskDmesgQuestions},
}

// catDiskQuestions dispatches based on the path looked at.
func catDiskQuestions(si SystemInfo, c CapturedCommand) []Question {
	if strings.Contains(c.Cmd, "/proc/pressure/io") {
		return psiIOQuestions(si, c)
	}
	return nil
}

var iostatHeaderRe = regexp.MustCompile(`(?m)^Device.*%util`)

func iostatQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !iostatHeaderRe.MatchString(c.Output) {
		return nil
	}
	return []Question{
		{
			Stem:    "In `iostat -x` output, what does the `%util` column literally measure?",
			Correct: "The fraction of wall-clock time during which at least one I/O request was in flight",
			Distractors: []string{
				"The fraction of the device's IOPS capacity currently in use",
				"The percentage of disk space currently allocated",
				"The percentage of throughput consumed relative to the bus bandwidth",
			},
		},
		{
			Stem:    "Why is `%util` near 100% NOT a reliable saturation signal on an NVMe or SSD device?",
			Correct: "Non-rotational devices serve I/O in parallel; \"at least one in flight\" can be true continuously without the device queueing or slowing down",
			Distractors: []string{
				"iostat does not actually support non-rotational devices",
				"%util is computed differently for NVMe and is always near 100%",
				"%util is reset on each sample, so high values are sampling artifacts",
			},
		},
		{
			Stem:    "What does the `aqu-sz` (or `avgqu-sz`) column represent in iostat?",
			Correct: "Average number of I/O requests outstanding to the device (queued plus in service)",
			Distractors: []string{
				"Average request size in kilobytes",
				"Average time each request spent in queue, in milliseconds",
				"Maximum queue depth supported by the device driver",
			},
		},
	}
}

func lsblkQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "NAME") || !strings.Contains(c.Output, "TYPE") {
		return nil
	}
	return []Question{{
		Stem:    "In `lsblk`, what's the difference between a row with TYPE=disk and one with TYPE=part?",
		Correct: "`disk` is a whole physical (or virtual) block device; `part` is a partition that lives on a disk",
		Distractors: []string{
			"`disk` is mounted; `part` is not",
			"`disk` rows show capacity; `part` rows show usage",
			"`disk` is local; `part` is networked",
		},
	}}
}

func pidstatDQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "kB_rd/s") && !strings.Contains(c.Output, "kB_wr/s") {
		return nil
	}
	// Randomize between read and write directions.
	type ioPick struct {
		col     string
		correct string
		dMapped string // distractor: "...memory-mapped files only"
		dUptime string // distractor: "...divided by uptime"
		dQueue  string // distractor: "...still in the kernel's queue"
	}
	pick := pickRandom([]ioPick{
		{
			col:     "kB_rd/s",
			correct: "The rate at which the process is causing data to be read from storage, in kilobytes per second",
			dMapped: "The rate at which the process is reading from memory-mapped files only",
			dUptime: "Total bytes read by the process since it started, divided by uptime",
			dQueue:  "The size of pending reads still in the kernel's I/O queue",
		},
		{
			col:     "kB_wr/s",
			correct: "The rate at which the process is causing data to be written to storage, in kilobytes per second",
			dMapped: "The rate at which the process is writing to memory-mapped files only",
			dUptime: "Total bytes written by the process since it started, divided by uptime",
			dQueue:  "The size of pending writes still in the kernel's writeback queue",
		},
	})
	return []Question{{
		Stem:        fmt.Sprintf("In `pidstat -d` output, what does the `%s` column measure for each process?", pick.col),
		Correct:     pick.correct,
		Distractors: []string{pick.dMapped, pick.dUptime, pick.dQueue},
	}}
}

var psiIOHeaderRe = regexp.MustCompile(`(?m)^some avg10=`)

func psiIOQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Cmd, "/proc/pressure/io") {
		return nil
	}
	if !psiIOHeaderRe.MatchString(c.Output) {
		return nil
	}
	type psiPick struct {
		metric, correct, sibling string
	}
	pick := pickRandom([]psiPick{
		{"some", "The percentage of time during which at least one task was stalled on I/O", "The percentage of time during which all non-idle tasks were simultaneously stalled on I/O"},
		{"full", "The percentage of time during which all non-idle tasks were simultaneously stalled on I/O", "The percentage of time during which at least one task was stalled on I/O"},
	})
	return []Question{
		{
			Stem:    fmt.Sprintf("In /proc/pressure/io, what does the `%s` line measure?", pick.metric),
			Correct: pick.correct,
			Distractors: []string{
				pick.sibling,
				"The percentage of disk capacity currently consumed",
				"The percentage of I/O requests that completed within their target latency",
			},
		},
	}
}

func diskDmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "i/o error") &&
		!strings.Contains(low, "buffer i/o error") &&
		!strings.Contains(low, "read-only") &&
		!strings.Contains(low, "ext4-fs error") {
		return nil
	}
	return []Question{
		{
			Stem:    "If `dmesg` shows `Remounting filesystem read-only`, what just happened and what's the immediate consequence for processes?",
			Correct: "The kernel hit unrecoverable I/O errors and remounted the filesystem read-only as a safety measure; subsequent writes will fail with EROFS",
			Distractors: []string{
				"An administrator enabled read-only mode; processes will pause until it's unset",
				"The filesystem is being checked; writes are temporarily queued and will replay",
				"Disk space is exhausted; processes will see ENOSPC until space frees",
			},
		},
		{
			Stem:    "Recurring `I/O error` lines on a single device most strongly suggest:",
			Correct: "Hardware degradation — the storage media or its connection is failing",
			Distractors: []string{
				"The kernel I/O scheduler is misconfigured",
				"The filesystem needs to be reformatted",
				"A user-space process is opening too many file descriptors",
			},
		},
	}
}

// ----- Observations -----

var diskObservations = []Observation{
	{
		Name:    "iostat_max_util_pct",
		Title:   "Max %util across devices",
		Section: "Utilization",
		Extract: extractIostatMaxUtil,
		Recall:  iostatMaxUtilRecall,
	},
	{
		Name:    "iostat_peak_iops",
		Title:   "Peak per-device IOPS (r/s + w/s)",
		Section: "Utilization",
		Extract: extractIostatPeakIOPS,
	},
	{
		Name:    "iostat_max_aqu_sz",
		Title:   "Max queue depth (aqu-sz)",
		Section: "Saturation",
		Extract: extractIostatMaxAquSz,
	},
	{
		Name:    "iostat_max_await_ms",
		Title:   "Max await (ms)",
		Section: "Saturation",
		Extract: extractIostatMaxAwait,
	},
	{
		Name:    "psi_io_some_avg10",
		Title:   "PSI io some (avg10)",
		Section: "Saturation",
		Extract: extractPSIIO("some"),
	},
	{
		Name:    "psi_io_full_avg10",
		Title:   "PSI io full (avg10)",
		Section: "Saturation",
		Extract: extractPSIIO("full"),
	},
	{
		Name:    "dmesg_io_errors",
		Title:   "I/O errors in dmesg",
		Section: "Errors",
		Extract: extractDmesgIOErrors,
	},
}

// iostatRow holds the columns we care about from one iostat output row.
// Other columns (rrqm/s, svctm, etc.) are ignored.
type iostatRow struct {
	Device string
	RPerS  float64
	WPerS  float64
	Util   float64 // %util
	Await  float64 // ms; uses combined `await` if present, else max(r_await, w_await)
	AquSz  float64 // average queue depth
}

// parseIostat extracts every device row across every sample in iostat -x output.
// Column order varies between sysstat versions, so we look up by header name.
func parseIostat(output string) []iostatRow {
	lines := strings.Split(output, "\n")
	var rows []iostatRow

	type colMap struct {
		device, rps, wps, util, await, rAwait, wAwait, aquSz int
		count                                                int
	}

	var current colMap
	current.device = -1

	indexOf := func(headers []string, name string) int {
		for i, h := range headers {
			if h == name {
				return i
			}
		}
		return -1
	}

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			current.device = -1
			continue
		}
		// New header line: rebuild the column map.
		// Older sysstat writes "Device:" with a trailing colon; modern is "Device".
		if fields[0] == "Device" || fields[0] == "Device:" {
			current = colMap{
				device: 0,
				rps:    indexOf(fields, "r/s"),
				wps:    indexOf(fields, "w/s"),
				util:   indexOf(fields, "%util"),
				await:  indexOf(fields, "await"),
				rAwait: indexOf(fields, "r_await"),
				wAwait: indexOf(fields, "w_await"),
				aquSz:  indexOf(fields, "aqu-sz"),
				count:  len(fields),
			}
			if current.aquSz == -1 {
				current.aquSz = indexOf(fields, "avgqu-sz") // older sysstat
			}
			continue
		}
		if current.device == -1 {
			continue
		}
		if len(fields) != current.count {
			continue
		}
		// Parse the device row.
		var r iostatRow
		r.Device = fields[current.device]
		r.RPerS = parseField(fields, current.rps)
		r.WPerS = parseField(fields, current.wps)
		r.Util = parseField(fields, current.util)
		r.AquSz = parseField(fields, current.aquSz)
		switch {
		case current.await >= 0:
			r.Await = parseField(fields, current.await)
		case current.rAwait >= 0 && current.wAwait >= 0:
			ra := parseField(fields, current.rAwait)
			wa := parseField(fields, current.wAwait)
			if ra > wa {
				r.Await = ra
			} else {
				r.Await = wa
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func parseField(fields []string, idx int) float64 {
	if idx < 0 || idx >= len(fields) {
		return 0
	}
	n, err := strconv.ParseFloat(fields[idx], 64)
	if err != nil {
		return 0
	}
	return n
}

func iostatRowsFromCaptures(caps []CapturedCommand) []iostatRow {
	var all []iostatRow
	for _, c := range caps {
		if baseCmd(c.Cmd) != "iostat" {
			continue
		}
		all = append(all, parseIostat(c.Output)...)
	}
	return all
}

// deviceClassNote returns a teaching note about the rotational status of the
// devices observed. Reads /sys/block/<dev>/queue/rotational directly — that's
// a deterministic file read, not user-captured output.
func deviceClassNote(rows []iostatRow) string {
	if len(rows) == 0 {
		return ""
	}
	seen := map[string]bool{}
	rotational := 0
	nonRotational := 0
	for _, r := range rows {
		if seen[r.Device] {
			continue
		}
		seen[r.Device] = true
		switch deviceRotational(r.Device) {
		case 1:
			rotational++
		case 0:
			nonRotational++
		}
	}
	switch {
	case rotational > 0 && nonRotational == 0:
		return "rotational devices observed; %util reflects actual busy time"
	case nonRotational > 0 && rotational == 0:
		return "non-rotational devices observed; %util can be misleading — read aqu-sz/await instead"
	case rotational > 0 && nonRotational > 0:
		return "mixed device classes observed; interpret %util per-device"
	default:
		return ""
	}
}

// deviceRotational returns 1 for spinning disks, 0 for SSD/NVMe, -1 if
// unknown (path doesn't exist, can't be read, etc.).
func deviceRotational(device string) int {
	data, err := os.ReadFile("/sys/block/" + device + "/queue/rotational")
	if err != nil {
		return -1
	}
	switch strings.TrimSpace(string(data)) {
	case "1":
		return 1
	case "0":
		return 0
	default:
		return -1
	}
}

func extractIostatMaxUtil(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	rows := iostatRowsFromCaptures(caps)
	if len(rows) == 0 {
		return Value{}, false
	}
	max := 0.0
	for _, r := range rows {
		if r.Util > max {
			max = r.Util
		}
	}
	v := Value{Number: max, Unit: "%"}
	if note := deviceClassNote(rows); note != "" {
		v.Note = note
	}
	return v, true
}

func extractIostatPeakIOPS(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	rows := iostatRowsFromCaptures(caps)
	if len(rows) == 0 {
		return Value{}, false
	}
	max := 0.0
	for _, r := range rows {
		iops := r.RPerS + r.WPerS
		if iops > max {
			max = iops
		}
	}
	return Value{Number: max, Unit: " IOPS", Note: "single-device peak across samples"}, true
}

func extractIostatMaxAquSz(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	rows := iostatRowsFromCaptures(caps)
	if len(rows) == 0 {
		return Value{}, false
	}
	max := 0.0
	for _, r := range rows {
		if r.AquSz > max {
			max = r.AquSz
		}
	}
	return Value{Number: max, Note: "sustained > 1 = requests queueing"}, true
}

func extractIostatMaxAwait(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	rows := iostatRowsFromCaptures(caps)
	if len(rows) == 0 {
		return Value{}, false
	}
	max := 0.0
	for _, r := range rows {
		if r.Await > max {
			max = r.Await
		}
	}
	return Value{Number: max, Unit: " ms"}, true
}

var psiIOLineRe = regexp.MustCompile(`^(some|full)\s+avg10=([0-9.]+)`)

func extractPSIIO(which string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		for i := len(caps) - 1; i >= 0; i-- {
			c := caps[i]
			if !strings.Contains(c.Cmd, "/proc/pressure/io") {
				continue
			}
			if !psiIOHeaderRe.MatchString(c.Output) {
				continue
			}
			for _, line := range strings.Split(c.Output, "\n") {
				m := psiIOLineRe.FindStringSubmatch(line)
				if m == nil || m[1] != which {
					continue
				}
				n, err := strconv.ParseFloat(m[2], 64)
				if err != nil {
					continue
				}
				return Value{Number: n, Unit: "%"}, true
			}
		}
		return Value{}, false
	}
}

func extractDmesgIOErrors(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	seen := false
	matched := 0
	totalLines := 0
	keywords := []string{"i/o error", "buffer i/o error", "read-only", "ext4-fs error"}
	for _, c := range caps {
		b := baseCmd(c.Cmd)
		if b != "dmesg" && b != "journalctl" {
			continue
		}
		seen = true
		for _, line := range strings.Split(c.Output, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			totalLines++
			low := strings.ToLower(line)
			for _, kw := range keywords {
				if strings.Contains(low, kw) {
					matched++
					break
				}
			}
		}
	}
	if !seen {
		return Value{}, false
	}
	return Value{
		Text: fmt.Sprintf("%d/%d lines mention I/O errors or read-only remounts", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

func iostatMaxUtilRecall(v Value) []Question {
	correct := fmt.Sprintf("%.0f%%", v.Number)
	pool := []string{
		fmt.Sprintf("%.0f%%", clamp(v.Number-10, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(v.Number-25, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(v.Number-50, 0, 100)),
		"0%", "25%", "50%", "75%", "100%",
	}
	return makeRecallQuestion(
		"What was the highest `%util` value you observed in iostat across all devices?",
		correct, pool)
}

// ----- Synthesis rules -----

var diskSynthesisRules = []SynthesisRule{
	utilAwaitConsistency,
}

// utilAwaitConsistency lands the SSD %util gotcha: high %util alone says
// nothing about saturation on parallel devices; you have to read aqu-sz and
// await alongside it.
var utilAwaitConsistency = SynthesisRule{
	Requires: []string{"iostat_max_util_pct", "iostat_max_aqu_sz", "iostat_max_await_ms"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		util := vs["iostat_max_util_pct"].Number
		aqu := vs["iostat_max_aqu_sz"].Number
		await := vs["iostat_max_await_ms"].Number

		var correct string
		switch {
		case util >= 90 && aqu < 2 && await < 5:
			correct = "Busy but not saturated — high %util with low queue depth and low await is the signature of a parallel (SSD/NVMe) device that can absorb the load. %util is misleading here."
		case aqu >= 4 && await >= 20:
			correct = "Saturated — high queue depth and high await mean requests are queueing and waiting. The device cannot keep up with offered load."
		case util < 50 && aqu < 1 && await < 5:
			correct = "Headroom — low utilization, no queueing, low latency. The disk has spare capacity."
		default:
			return Question{}, false
		}

		pool := []string{
			"Busy but not saturated — high %util with low queue depth and low await is the signature of a parallel (SSD/NVMe) device that can absorb the load. %util is misleading here.",
			"Saturated — high queue depth and high await mean requests are queueing and waiting. The device cannot keep up with offered load.",
			"Headroom — low utilization, no queueing, low latency. The disk has spare capacity.",
			"Cannot be assessed — these three columns measure unrelated subsystems.",
		}
		var distractors []string
		for _, p := range pool {
			if p != correct {
				distractors = append(distractors, p)
			}
		}
		if len(distractors) > 3 {
			distractors = distractors[:3]
		}

		return Question{
			Stem: fmt.Sprintf(
				"Max %%util observed: %.0f%%; max aqu-sz: %.1f; max await: %.1f ms.\n"+
					"Which best describes what these three numbers say together?",
				util, aqu, await),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// ----- Command reference -----

var diskCommands = []CommandRef{
	{
		Cmd:     "lsblk",
		Section: "Utilization",
		Summary: "Tree of block devices and their partitions/LVMs.\nFastest orientation when you don't know the device names.",
	},
	{
		Cmd:     "iostat -xz 1 N",
		Section: "Utilization",
		Summary: "Per-device extended stats over N intervals.\n-x adds %util/await/aqu-sz; -z hides idle devices.\nThe single most important disk command.",
	},
	{
		Cmd:     "df -h",
		Section: "Utilization",
		Summary: "Filesystem capacity (not bandwidth).\nUseful when 'disk full' is the suspected problem.",
	},
	{
		Cmd:     "iostat -xz 1 N",
		Section: "Saturation",
		Summary: "Same command — read aqu-sz (queueing) and await (latency).\nSustained aqu-sz > 1 or await well above your device baseline\n= saturation, regardless of what %util shows.",
	},
	{
		Cmd:     "cat /proc/pressure/io",
		Section: "Saturation",
		Summary: "PSI: time-share of tasks stalled on I/O.\n`full` > 0 is the strongest saturation signal.\nLinux 4.20+ with PSI enabled.",
	},
	{
		Cmd:     "pidstat -d 1 N",
		Section: "Saturation",
		Summary: "Per-process I/O rates over N intervals.\nFinds the process responsible for device load. (sysstat package.)",
	},
	{
		Cmd:     "iotop -bn1",
		Section: "Saturation",
		Summary: "Friendlier per-process I/O snapshot.\nBatch mode (-b -n1) avoids needing a TTY. (iotop package.)",
	},
	{
		Cmd:     "dmesg -T 2>/dev/null | grep -iE 'i/o error|EXT4-fs error|Buffer I/O error|read-only'",
		Section: "Errors",
		Summary: "Kernel I/O errors and read-only remounts.\nRecurring I/O errors → failing media; read-only remount → kernel\ngave up on writes.",
	},
	{
		Cmd:     "smartctl -a /dev/sda",
		Section: "Errors",
		Summary: "SMART self-assessment for a device.\nVendor-specific output; read the OVERALL-HEALTH line and the\nReallocated_Sector_Ct / Pending_Sector counters. (smartmontools package.)",
	},
	{
		Cmd:     "cat /proc/diskstats",
		Section: "Errors",
		Summary: "Raw kernel I/O counters per device.\nField 14 (read errors) and field 15 (write errors) are non-zero\non degraded media.",
	},
}
