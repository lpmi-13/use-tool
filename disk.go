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
		"and asks specific questions about what you saw.",
	StepsFn:      diskSteps,
	Observations: diskObservations,
	Commands:     diskCommands,
}

// ----- Guide steps -----

func diskSteps(si SystemInfo) []GuideStep {
	deviceVariants := diskDeviceVariants()
	devicePick := pickStepVariant(si, deviceVariants)

	throughputVariants := diskThroughputVariants(si)
	throughputPick := pickStepVariant(si, throughputVariants)

	steps := []GuideStep{
		{
			Name:          "devices",
			Intro:         "Step 1: get a sense of what block devices the system has.",
			Suggested:     devicePick.Cmd,
			QuestionsFn:   combineVariantQuestions(deviceVariants),
			QuestionCount: 3,
			AcceptAny:     true,
			Teaching:      devicePick.Teaching,
		},
		{
			Name: "throughput",
			Intro: "Step 2: per-device throughput, queue depth, and latency.\n" +
				"Look for high queue depth (`aqu-sz` / `avgqu-sz`) or high await/svctm —\n" +
				"those are the saturation signals.",
			Suggested:     throughputPick.Cmd,
			QuestionsFn:   combineVariantQuestions(throughputVariants),
			QuestionCount: 3,
			Teaching:      throughputPick.Teaching,
		},
	}
	if si.HasPSI {
		steps = append(steps, GuideStep{
			Name:          "pressure",
			Intro:         "Step 3: PSI reports time-share of tasks stalled on I/O.",
			Suggested:     "cat /proc/pressure/io",
			QuestionsFn:   psiIOColumnQuestions,
			QuestionCount: 3,
			Teaching: "`some` is time at least one task was stalled on I/O; `full` is time\n" +
				"all non-idle tasks were stalled. Non-zero `full avg10` is the strongest\n" +
				"saturation signal — the system was actually wedged, not just busy.",
		})
	}
	steps = append(steps, GuideStep{
		Name:          "attribution",
		Intro:         "Step 4: which processes are doing the I/O?",
		Suggested:     "pidstat -d 1 3",
		QuestionsFn:   pidstatDColumnQuestions,
		QuestionCount: 3,
		AcceptAny:     true,
		Teaching: "kB_rd/s and kB_wr/s are per-process read/write rates from the kernel's\n" +
			"task accounting. If one process accounts for most of the device load,\n" +
			"that's where to look next.\n\n" +
			"Caveat: pidstat reports the host PID namespace. I/O from a process\n" +
			"running inside a Docker/containerd container shows up under that\n" +
			"container's runtime (`runc`, `containerd-shim`) or doesn't show up\n" +
			"at all. If the noisy rows don't look like your real workload, pivot\n" +
			"to `docker stats` (BlockIO column) or `nsenter -t <pid> -m -p` into\n" +
			"the container to re-run pidstat from inside.",
	})
	steps = append(steps, GuideStep{
		Name:               "errors",
		Intro:              "Step 5: kernel I/O errors. Filesystem-level errors are the smoking\ngun for failing media.\n" + dmesgPermissionNote,
		Suggested:          "dmesg -T | grep -iE 'i/o error|EXT4-fs error|XFS|Buffer I/O error|read-only' | tail",
		Alternatives:       journalctlAlternative(si, "journalctl -k -b --no-pager | grep -iE 'i/o error|EXT4-fs error|XFS|Buffer I/O error|read-only' | tail"),
		QuestionsFn:        diskDmesgQuestions,
		AcceptAny:          true,
		EmptyOutputMessage: "No matching I/O errors found.",
		Teaching: "Repeated `I/O error` lines or a kernel-initiated read-only remount\n" +
			"(`Remounting filesystem read-only`) mean hardware that's failing or\n" +
			"already failed. Apps will see EROFS on writes from that point on.",
	})
	return steps
}

// diskDeviceVariants is the pool for the device-orientation step.
// `lsblk` is the canonical view; `cat /proc/partitions` exposes the same
// information in the raw kernel format (major/minor numbers, blocks, name).
func diskDeviceVariants() []stepVariant {
	return []stepVariant{
		{
			Cmd:         "lsblk",
			QuestionsFn: lsblkColumnQuestions,
			Teaching: "Note which devices are physical disks (TYPE=disk) vs partitions (part)\n" +
				"vs LVM volumes (lvm). Per-device metrics from iostat will use the disk\n" +
				"names you see here.",
		},
		{
			Cmd:         "cat /proc/partitions",
			QuestionsFn: procPartitionsColumnQuestions,
			Teaching: "`/proc/partitions` is what `lsblk` reads under the hood. Fields are\n" +
				"`major  minor  #blocks  name`. #blocks counts 1024-byte blocks, so a\n" +
				"512 GiB disk shows ~500_000_000 blocks. No tree formatting and no\n" +
				"mountpoints — when lsblk isn't installed (minimal containers, busybox),\n" +
				"this is the backup way to look around.",
		},
	}
}

// diskThroughputVariants is the pool for the throughput / saturation step.
// `iostat -xz` is the default; `sar -d` exposes a similar table (await,
// avgqu-sz, %util) but with column names tied to sysstat's archive format,
// and is the right tool for after-the-fact diagnosis via `sar -d -f`.
func diskThroughputVariants(si SystemInfo) []stepVariant {
	return []stepVariant{
		{
			Cmd:         "iostat -xz 1 3 | grep -vE '^loop'",
			QuestionsFn: iostatColumnQuestions,
			Teaching: "Three signals matter most:\n" +
				"  • r/s + w/s — workload (the offered IOPS).\n" +
				"  • aqu-sz — average queue depth. Steady > 1 means requests are\n" +
				"    queueing; that's saturation.\n" +
				"  • await — average request latency in ms. Compare to your device's\n" +
				"    expected baseline.\n" +
				"And one trap: %util on non-rotational devices (SSD, NVMe) is misleading\n" +
				"— it measures \"at least one I/O in flight,\" which doesn't reflect parallel\n" +
				"capacity. Read aqu-sz/await on those devices, not %util.",
		},
		{
			Cmd:         "sar -d 1 3",
			QuestionsFn: sarDColumnQuestions,
			Teaching: "`sar -d` reports per-device tps, throughput (rkB/s, wkB/s), average\n" +
				"request size (areq-sz), queue depth (aqu-sz), and await — the same\n" +
				"shape as iostat but saved by sysstat. `sar -d -f /var/log/sa/...`\n" +
				"replays an earlier window without a live capture.\n" +
				"Caveat: device names appear as `dev<major>-<minor>` (e.g. `dev8-0`)\n" +
				"rather than `sda`; cross-check against `/proc/partitions` if unsure.",
			Available: func(si SystemInfo) bool { return si.HasSar },
		},
	}
}

// ----- Comprehension extractors (per-command) -----

// catDiskQuestions dispatches based on the path looked at.

// procPartitionsHeaderRe matches the header line of `/proc/partitions`,
// which always begins with `major minor  #blocks  name`.
var procPartitionsHeaderRe = regexp.MustCompile(`(?m)^\s*major\s+minor\s+#blocks\s+name\s*$`)

// procPartitionsQuestions returns one random column question — extractor variant.
func procPartitionsQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !procPartitionsHeaderRe.MatchString(c.Output) {
		return nil
	}
	return randomColumnQuestions(
		availableColumnQuestionPicks(c.Output, procPartitionsQuestionPicks),
		1,
		procPartitionsStemFor,
	)
}

// procPartitionsColumnQuestions returns the full pool — guide-step variant.
func procPartitionsColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !procPartitionsHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, procPartitionsQuestionPicks),
		procPartitionsStemFor,
	)
}

func procPartitionsStemFor(column string) string {
	return fmt.Sprintf("In `/proc/partitions`, what does the `%s` column represent?", column)
}

var procPartitionsQuestionPicks = []columnQuestionPick{
	{
		Column:  "major",
		Correct: "The kernel major device number, identifying which driver owns the device",
		Distractors: []string{
			"The capacity of the device in major units (e.g. terabytes)",
			"The partition's index within the disk, 1-based",
			"The major version of the filesystem on the partition",
		},
	},
	{
		Column:  "minor",
		Correct: "The kernel minor device number, separating instances within a driver (e.g. sda=0, sda1=1)",
		Distractors: []string{
			"A minor version number for the partition table format",
			"The disk's index within its bus",
			"The number of inodes free on the partition",
		},
	},
	{
		Column:  "#blocks",
		Correct: "The capacity of the device or partition counted in 1024-byte units",
		Distractors: []string{
			"The number of 512-byte sectors in the device",
			"The current number of allocated filesystem allocation units",
			"The number of I/O requests outstanding to the device",
		},
	},
	{
		Column:  "name",
		Correct: "The kernel block-device name (sda, sda1, nvme0n1, dm-0, …)",
		Distractors: []string{
			"The mount point where the device is currently mounted",
			"The filesystem label set with `e2label` or `xfs_admin`",
			"The user-friendly model name reported by the device",
		},
	},
}

// sarDHeaderRe matches the column row of `sar -d`. Pre-12.x sysstat uses
// `avgqu-sz` / `avgrq-sz`; modern versions emit `aqu-sz` / `areq-sz`. We
// require `DEV` plus `await` so a sar -W or sar -n match doesn't qualify.
var sarDHeaderRe = regexp.MustCompile(`(?m)^[\d:]+\s+DEV\b.*\bawait\b`)

// sarDQuestions returns one random column question — extractor variant.

// sarDColumnQuestions returns the full pool — guide-step variant.
func sarDColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarDHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, sarDQuestionPicks),
		sarDStemFor,
	)
}

func sarDStemFor(column string) string {
	return fmt.Sprintf("In `sar -d` output, what does the `%s` column represent?", column)
}

var sarDQuestionPicks = []columnQuestionPick{
	{
		Column:  "DEV",
		Correct: "The block device, often shown as `dev<major>-<minor>` (e.g. `dev8-0` for sda)",
		Distractors: []string{
			"The device driver module name",
			"The mount point currently using the device",
			"The hardware bus the device is attached to",
		},
	},
	{
		Column:  "tps",
		Correct: "Transfers per second issued to the device — combined reads and writes",
		Distractors: []string{
			"Throughput in megabytes per second",
			"Threads per second waiting on the device",
			"Total transfers since boot, divided by uptime",
		},
	},
	{
		Column:  "rkB/s",
		Correct: "Kilobytes read from the device per second during the sample interval",
		Distractors: []string{
			"Read requests per second, ignoring request size",
			"Total kilobytes read since the sar process started",
			"Bytes read from page cache per second",
		},
	},
	{
		Column:  "wkB/s",
		Correct: "Kilobytes written to the device per second during the sample interval",
		Distractors: []string{
			"Write requests per second, ignoring request size",
			"Total kilobytes written since the sar process started",
			"Bytes flushed from dirty page cache per second",
		},
	},
	{
		Column:  "areq-sz",
		Correct: "Average request size in kilobytes — total throughput divided by number of requests",
		Distractors: []string{
			"Average request queue depth",
			"Average response size from the device",
			"Average inter-arrival time between requests, in milliseconds",
		},
	},
	{
		Column:  "avgrq-sz",
		Correct: "Average request size in sectors (older sysstat output for `areq-sz`)",
		Distractors: []string{
			"Average queue depth in requests",
			"Average response time in milliseconds",
			"Average request rate per second",
		},
	},
	{
		Column:  "aqu-sz",
		Correct: "Average number of I/O requests outstanding to the device (queued plus in service)",
		Distractors: []string{
			"Average request size in kilobytes",
			"Average time each request spent in the queue, in milliseconds",
			"Maximum queue depth supported by the driver",
		},
	},
	{
		Column:  "avgqu-sz",
		Correct: "Average number of outstanding requests (older sysstat output for `aqu-sz`)",
		Distractors: []string{
			"Average request size in sectors",
			"Average queueing latency in milliseconds",
			"Average rate of request arrivals",
		},
	},
	{
		Column:  "await",
		Correct: "Average request latency in milliseconds, including queueing and service time",
		Distractors: []string{
			"Average wait before the request was queued (queueing time only)",
			"Average request size in kilobytes",
			"Percent of time the device had a request in flight",
		},
	},
	{
		Column:  "svctm",
		Correct: "Average service time per request in milliseconds (deprecated and unreliable on modern kernels — prefer await)",
		Distractors: []string{
			"Total time the service has spent on I/O since boot",
			"Average size of each service request in kilobytes",
			"Average number of services contributing to the device load",
		},
	},
	{
		Column:  "%util",
		Correct: "Fraction of wall-clock time the device had at least one I/O in flight",
		Distractors: []string{
			"Fraction of device IOPS capacity in use",
			"Fraction of throughput used relative to the bus bandwidth",
			"Fraction of disk space currently allocated",
		},
	},
}

var iostatHeaderRe = regexp.MustCompile(`(?m)^Device.*%util`)

func iostatColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !iostatHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableIostatQuestionPicks(c.Output),
		func(column string) string {
			return fmt.Sprintf("In `iostat -x` output, what does the `%s` column represent?", column)
		},
	)
}

var iostatQuestionPicks = []columnQuestionPick{
	{
		Column:  "r/s",
		Correct: "Read requests completed per second for the device",
		Distractors: []string{
			"Read kilobytes completed per second for the device",
			"Read requests currently queued for the device",
			"Average read request latency in milliseconds",
		},
	},
	{
		Column:  "w/s",
		Correct: "Write requests completed per second for the device",
		Distractors: []string{
			"Write kilobytes completed per second for the device",
			"Write requests currently queued for the device",
			"Average write request latency in milliseconds",
		},
	},
	{
		Column:  "r_await",
		Correct: "Average read request latency in milliseconds, including queueing and service time",
		Distractors: []string{
			"Average read request size in kilobytes",
			"Read requests completed per second",
			"Average number of reads waiting in the queue only",
		},
	},
	{
		Column:  "w_await",
		Correct: "Average write request latency in milliseconds, including queueing and service time",
		Distractors: []string{
			"Average write request size in kilobytes",
			"Write requests completed per second",
			"Average number of writes waiting in the queue only",
		},
	},
	{
		Column:  "await",
		Correct: "Average request latency in milliseconds, including queueing and service time",
		Distractors: []string{
			"Average request size in kilobytes",
			"Average number of requests outstanding",
			"Percent of time the device had at least one request in flight",
		},
	},
	{
		Column:  "aqu-sz",
		Correct: "Average number of I/O requests outstanding to the device (queued plus in service)",
		Distractors: []string{
			"Average request size in kilobytes",
			"Average time each request spent in queue, in milliseconds",
			"Maximum queue depth supported by the device driver",
		},
	},
	{
		Column:  "avgqu-sz",
		Correct: "Average number of I/O requests outstanding to the device (queued plus in service)",
		Distractors: []string{
			"Average request size in sectors",
			"Average request latency in milliseconds",
			"Maximum queue depth supported by the device driver",
		},
	},
	{
		Column:  "%util",
		Correct: "The fraction of wall-clock time when at least one I/O request was in flight",
		Distractors: []string{
			"The fraction of the device's IOPS capacity currently in use",
			"The percentage of disk space currently allocated",
			"The percentage of throughput used relative to the bus bandwidth",
		},
	},
}

func availableIostatQuestionPicks(output string) []columnQuestionPick {
	return availableColumnQuestionPicks(output, iostatQuestionPicks)
}

func lsblkColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "NAME") || !strings.Contains(c.Output, "TYPE") {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, lsblkQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `lsblk` output, what does the `%s` column represent?", column)
		},
	)
}

var lsblkQuestionPicks = []columnQuestionPick{
	{
		Column:  "NAME",
		Correct: "The kernel block-device name, with tree indentation showing parent/child relationships",
		Distractors: []string{
			"The filesystem label mounted on the device",
			"The hardware model string for the disk",
			"The mount namespace that owns the device",
		},
	},
	{
		Column:  "MAJ:MIN",
		Correct: "The kernel major and minor device numbers",
		Distractors: []string{
			"The major and minor filesystem version",
			"The PCI bus and slot numbers",
			"The partition start and end sectors",
		},
	},
	{
		Column:  "RM",
		Correct: "Whether the device is removable",
		Distractors: []string{
			"Whether the device is mounted read-only",
			"Whether the device is remote",
			"Whether the device has recently been removed",
		},
	},
	{
		Column:  "SIZE",
		Correct: "The apparent capacity of the block device or partition",
		Distractors: []string{
			"The filesystem space currently used",
			"The amount of dirty writeback data",
			"The hardware queue depth",
		},
	},
	{
		Column:  "RO",
		Correct: "Whether the block device is read-only",
		Distractors: []string{
			"Whether the device is removable",
			"Whether reads are currently outstanding",
			"Whether the filesystem is mounted",
		},
	},
	{
		Column:  "TYPE",
		Correct: "The block-device category, such as disk, partition, loop device, or LVM volume",
		Distractors: []string{
			"The filesystem format, such as ext4 or xfs",
			"The bus category, such as PCIe or SATA",
			"The current I/O scheduler",
		},
	},
	{
		Column:  "MOUNTPOINTS",
		Correct: "Where filesystems from that block device are mounted",
		Distractors: []string{
			"The partition table entries on the device",
			"The device's physical attachment points",
			"The applications currently writing to the device",
		},
	},
}

func pidstatDColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "kB_rd/s") && !strings.Contains(c.Output, "kB_wr/s") {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, pidstatDQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `pidstat -d` output, what does the `%s` column represent?", column)
		},
	)
}

var pidstatDQuestionPicks = []columnQuestionPick{
	{
		Column:  "UID",
		Correct: "The user ID that owns the process",
		Distractors: []string{
			"The unique disk ID being accessed",
			"The kernel thread ID",
			"The container ID for the process",
		},
	},
	{
		Column:  "PID",
		Correct: "The process ID being reported",
		Distractors: []string{
			"The parent process ID",
			"The physical disk ID",
			"The process's priority",
		},
	},
	{
		Column:  "kB_rd/s",
		Correct: "Kilobytes per second the process caused to be read from storage",
		Distractors: []string{
			"Kilobytes the process has read since it started",
			"Kilobytes per second read from memory cache only",
			"Pending read queue size in kilobytes",
		},
	},
	{
		Column:  "kB_wr/s",
		Correct: "Kilobytes per second the process caused to be written to storage",
		Distractors: []string{
			"Kilobytes the process has written since it started",
			"Kilobytes per second written to memory cache only",
			"Pending write queue size in kilobytes",
		},
	},
	{
		Column:  "kB_ccwr/s",
		Correct: "Kilobytes per second of writes cancelled by the task",
		Distractors: []string{
			"Kilobytes per second compressed before writing",
			"Kilobytes per second copied from cache",
			"Kilobytes per second written by child processes",
		},
	},
	{
		Column:  "iodelay",
		Correct: "Clock ticks the task spent blocked on block I/O",
		Distractors: []string{
			"Average device latency in milliseconds",
			"Current number of queued I/O requests",
			"Pause before the process starts after fork",
		},
	},
	{
		Column:  "Command",
		Correct: "The process name shown for the task",
		Distractors: []string{
			"The full argument vector including flags",
			"The cgroup path for the process",
			"The block device name being accessed",
		},
	},
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
		{"some", "The percentage of time when at least one task was stalled on I/O", "The percentage of time when all non-idle tasks were stalled on I/O at the same time"},
		{"full", "The percentage of time when all non-idle tasks were stalled on I/O at the same time", "The percentage of time when at least one task was stalled on I/O"},
	})
	return []Question{
		{
			Stem:    fmt.Sprintf("In /proc/pressure/io, what does the `%s` line measure?", pick.metric),
			Correct: pick.correct,
			Distractors: []string{
				pick.sibling,
				"The percentage of disk capacity currently used",
				"The percentage of I/O requests that completed within their target latency",
			},
		},
	}
}

func psiIOColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Cmd, "/proc/pressure/io") {
		return nil
	}
	if !psiIOHeaderRe.MatchString(c.Output) {
		return nil
	}
	return psiColumnQuestions("/proc/pressure/io", "I/O", c.Output)
}

// ----- Observations -----

var diskObservations = []Observation{
	{
		Name:      "iostat_max_util_pct",
		Title:     "Max %util across devices",
		Section:   "Utilization",
		Extract:   extractIostatMaxUtil,
		Verdict:   verdictDiskUtil,
		Heuristic: "%util near 100 = device backplane busy that interval (note: on SSDs/NVMe with parallel queues %util can saturate while the device still has headroom; cross-check with aqu-sz / await)",
	},
	{
		Name:      "iostat_peak_iops",
		Title:     "Peak per-device IOPS (r/s + w/s)",
		Section:   "Utilization",
		Extract:   extractIostatPeakIOPS,
		Heuristic: "absolute IOPS is context only — what counts as high depends on the device class (HDD vs SATA SSD vs NVMe)",
	},
	{
		Name:      "iostat_max_aqu_sz",
		Title:     "Max queue depth (aqu-sz)",
		Section:   "Saturation",
		Extract:   extractIostatMaxAquSz,
		Verdict:   verdictAquSz,
		Heuristic: "average queue depth > 1 steady = requests are queueing behind the device = saturation",
	},
	{
		Name:      "iostat_max_await_ms",
		Title:     "Max await (ms)",
		Section:   "Saturation",
		Extract:   extractIostatMaxAwait,
		Verdict:   verdictAwait,
		Heuristic: "await is end-to-end request latency (queue + service); steady >20ms on SSDs/NVMe is suspicious",
	},
	{
		Name:      "psi_io_some_avg10",
		Title:     "PSI io some (avg10)",
		Section:   "Saturation",
		Extract:   extractPSIIO("some"),
		Verdict:   verdictPSISome,
		Heuristic: "PSI io 'some' avg10 = percent of the last 10s with at least one task stalled on I/O; >10% steady = saturation",
	},
	{
		Name:      "psi_io_full_avg10",
		Title:     "PSI io full (avg10)",
		Section:   "Saturation",
		Extract:   extractPSIIO("full"),
		Verdict:   verdictPSIFull,
		Heuristic: "PSI io 'full' avg10 = percent of the window where ALL non-idle tasks stalled on I/O; any steady non-zero = severe saturation",
	},
	{
		Name:      "dmesg_io_errors",
		Title:     "I/O errors in dmesg",
		Section:   "Errors",
		Extract:   extractDmesgIOErrors,
		Verdict:   verdictDmesgIOErrors,
		Heuristic: "I/O error / buffer error / read-only remount entries in the kernel log = device-level failures",
	},
}

// ----- Diagnosis verdicts -----

func verdictDiskUtil(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number >= 80:
		return SignalHigh
	case v.Number >= 50:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictAquSz(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 1 {
		return SignalHigh
	}
	return SignalLow
}

func verdictAwait(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number >= 20:
		return SignalHigh
	case v.Number >= 10:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictDmesgIOErrors(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
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
		return "rotational devices seen; %util shows actual busy time"
	case nonRotational > 0 && rotational == 0:
		return "non-rotational devices seen; %util can be misleading — read aqu-sz/await instead"
	case rotational > 0 && nonRotational > 0:
		return "mixed device classes seen; read %util per-device"
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
	return Value{Number: max, Note: "steady > 1 = requests queueing"}, true
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
		Number: float64(matched),
		Text:   fmt.Sprintf("%d/%d lines mention I/O errors or read-only remounts", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

// ----- Synthesis rules -----

func diskDmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "i/o error") &&
		!strings.Contains(low, "buffer i/o error") &&
		!strings.Contains(low, "read-only") &&
		!strings.Contains(low, "ext4-fs error") {
		return nil
	}
	tool := kernelLogQuestionTool(c.Cmd)
	return []Question{
		{
			Stem:    fmt.Sprintf("If `%s` shows `Remounting filesystem read-only`, what just happened and what's the immediate consequence for processes?", tool),
			Correct: "The kernel hit unrecoverable I/O errors and remounted the filesystem read-only as a safety measure; any further writes will fail with EROFS",
			Distractors: []string{
				"An administrator enabled read-only mode; processes will pause until it's unset",
				"The filesystem is being checked; writes are temporarily queued and will replay",
				"Disk space is full; processes will see ENOSPC until space frees",
			},
		},
		{
			Stem:    fmt.Sprintf("In `%s` output, repeated `I/O error` lines on a single device most strongly suggest:", tool),
			Correct: "Failing hardware — the storage media or its connection is going bad",
			Distractors: []string{
				"The kernel I/O scheduler is misconfigured",
				"The filesystem needs to be reformatted",
				"A user-space process is opening too many file descriptors",
			},
		},
	}
}

var diskCommands = []CommandRef{
	{
		Cmd:     "lsblk",
		Section: "Utilization",
		Summary: "Tree of block devices and their partitions/LVMs.\nFastest way to get oriented when you don't know the device names.",
	},
	{
		Cmd:      "iostat -xz 1 N | grep -vE '^loop'",
		Section:  "Utilization",
		Summary:  "Per-device extended stats over N intervals.\n-x adds %util/await/aqu-sz; -z hides idle devices.\nThe grep filters snap loop-mounts so the real devices don't get\nlost in ~25 rows of `loop0..loopN` noise. Drop the pipe if you\nspecifically want to see those.\nThe single most important disk command.",
		Requires: []string{"iostat"},
	},
	{
		Cmd:     "df -h",
		Section: "Utilization",
		Summary: "Filesystem capacity (not bandwidth).\nUseful when 'disk full' is the suspected problem.",
	},
	{
		Cmd:      "iostat -xz 1 N | grep -vE '^loop'",
		Section:  "Saturation",
		Summary:  "Same command — read aqu-sz (queueing) and await (latency).\nSteady aqu-sz > 1 or await well above your device baseline\n= saturation, no matter what %util shows.",
		Requires: []string{"iostat"},
	},
	{
		Cmd:      "cat /proc/pressure/io",
		Section:  "Saturation",
		Summary:  "PSI: time-share of tasks stalled on I/O.\n`full` > 0 is the strongest saturation signal.\nLinux 4.20+ with PSI enabled.",
		Requires: []string{"psi"},
	},
	{
		Cmd:     "pidstat -d 1 N",
		Section: "Saturation",
		Summary: "Per-process I/O rates over N intervals.\nFinds the process responsible for device load. (sysstat package.)\nNote: shows host PID namespace. I/O from processes inside Docker/\ncontainerd containers (separate PID namespace) appears under the\ncontainer's runtime or not at all; switch to `docker stats` to see\nwhich service, or `nsenter` into the container's PID ns.",
	},
	{
		Cmd:     "iotop -bn1",
		Section: "Saturation",
		Summary: "Friendlier per-process I/O snapshot.\nBatch mode (-b -n1) avoids needing a TTY. (iotop package.)",
	},
	{
		Cmd:     "dmesg -T | grep -iE 'i/o error|EXT4-fs error|Buffer I/O error|read-only'",
		Section: "Errors",
		Summary: "Kernel I/O errors and read-only remounts.\nRecurring I/O errors → failing media; read-only remount → kernel\ngave up on writes.\n" + dmesgPermissionNote,
	},
	{
		Cmd:                 "journalctl -k -b --no-pager | grep -iE 'i/o error|EXT4-fs error|XFS|Buffer I/O error|read-only'",
		Section:             "Errors",
		Summary:             "Kernel I/O errors and read-only remounts via journald.\nAlternative to dmesg on systemd systems.",
		Requires:            []string{"journalctl"},
		HideWhenUnavailable: true,
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
