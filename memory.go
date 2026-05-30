package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var memoryInvestigation = &Investigation{
	Name:  "memory",
	Title: "Memory — Utilization, Saturation, Errors",
	Description: "Investigate memory using Brendan Gregg's USE method.\n" +
		"Run commands at the prompt; the harness captures their output\n" +
		"and asks specific questions about what you saw.",
	StepsFn:      memorySteps,
	Observations: memoryObservations,
	Commands:     memoryCommands,
}

// ----- Guide steps -----

func memorySteps(si SystemInfo) []GuideStep {
	baselineVariants := memoryBaselineVariants()
	baselinePick := pickStepVariant(si, baselineVariants)

	swapVariants := memorySwapVariants(si)
	swapPick := pickStepVariant(si, swapVariants)

	topVariants := memoryTopConsumerVariants()
	topPick := pickStepVariant(si, topVariants)

	steps := []GuideStep{
		{
			Name: "baseline",
			Intro: "Step 1: get a baseline of total, used, available memory and swap.\n" +
				"The kernel exposes this via /proc/meminfo; `free` summarises the same data.",
			Suggested:     baselinePick.Cmd,
			QuestionsFn:   combineVariantQuestions(baselineVariants),
			QuestionCount: 3,
			Teaching: "Read `MemAvailable` (or `available`), not `MemFree`/`free`. `MemFree`\n" +
				"excludes reclaimable page cache; `MemAvailable` is the kernel's own\n" +
				"estimate of how much memory is allocatable without going to swap. The\n" +
				"two can differ by many gigabytes on a busy server.",
		},
		{
			Name: "swap-activity",
			Intro: "Step 2: look at paging rates. Non-zero swap-in / swap-out activity\n" +
				"means the working set is bigger than RAM.",
			Suggested:     swapPick.Cmd,
			QuestionsFn:   combineVariantQuestions(swapVariants),
			QuestionCount: 3,
			Teaching:      swapPick.Teaching,
		},
	}
	if si.HasPSI {
		steps = append(steps, GuideStep{
			Name:          "pressure",
			Intro:         "Step 3: PSI reports the time-share of tasks stalled on memory.\nLinux 4.20+ with PSI enabled in the kernel.",
			Suggested:     "cat /proc/pressure/memory",
			QuestionsFn:   psiMemoryColumnQuestions,
			QuestionCount: 3,
			Teaching: "`some` is time at least one task was stalled on memory; `full` is\n" +
				"time *all* non-idle tasks were stalled. `full` > 0 is the strongest\n" +
				"saturation signal — it means the system was actually wedged, not just\n" +
				"under load.",
		})
	}
	steps = append(steps, GuideStep{
		Name:          "top-consumers",
		Intro:         "Step 4: who's actually using memory?",
		Suggested:     topPick.Cmd,
		QuestionsFn:   combineVariantQuestions(topVariants),
		QuestionCount: 3,
		AcceptAny:     true,
		Teaching:      topPick.Teaching,
	})
	steps = append(steps, GuideStep{
		Name:               "errors",
		Intro:              "Step 5: kernel memory errors mean OOM kills.\nEven if the system is fine *now*, recent OOM events explain flapping services.\n" + dmesgPermissionNote,
		Suggested:          "dmesg -T | grep -iE 'killed process|out of memory|oom-killer' | tail",
		Alternatives:       journalctlAlternative(si, "journalctl -k -b --no-pager | grep -iE 'killed process|out of memory|oom-killer' | tail"),
		QuestionsFn:        oomQuestions,
		AcceptAny:          true,
		EmptyOutputMessage: "No matching OOM errors found.",
		Teaching: "When the kernel runs out of memory it picks a victim by `oom_score`\n" +
			"and kills it. The dmesg line records the victim's PID, RSS at time of\n" +
			"kill, and which cgroup hit its limit (if container-scoped).",
	})
	return steps
}

// memoryBaselineVariants is the pool for the memory-baseline step.
// `cat /proc/meminfo` is the canonical source; `free -h` summarises the same
// data in a more compact table with a different vocabulary (`available` /
// `buff/cache`). The variants don't share output format, so order doesn't
// matter for dispatch.
func memoryBaselineVariants() []stepVariant {
	return []stepVariant{
		{Cmd: "cat /proc/meminfo", QuestionsFn: meminfoColumnQuestions},
		{Cmd: "free -h", QuestionsFn: freeColumnQuestions},
	}
}

// memorySwapVariants is the pool for the swap-activity step. vmstat is the
// default; `sar -W` reports just the swap rates (pswpin/s, pswpout/s) and
// requires sysstat. Each variant has its own teaching note because the
// columns and emphasis differ.
func memorySwapVariants(si SystemInfo) []stepVariant {
	return []stepVariant{
		{
			Cmd:         "vmstat 1 5",
			QuestionsFn: vmstatColumnQuestions,
			Teaching: "Steady `si`/`so` > 0 in vmstat means the working set is bigger than\n" +
				"physical memory. On its own this is a hint; combine with PSI or latency\n" +
				"observations to confirm tasks are actually being slowed down.",
		},
		{
			Cmd:         "sar -W 1 5",
			QuestionsFn: sarWColumnQuestions,
			Teaching: "`sar -W` reports pages swapped in/out per second (`pswpin/s` and\n" +
				"`pswpout/s`) — the same signal as vmstat's `si`/`so` but isolated\n" +
				"from the rest of vmstat's CPU and I/O columns. sysstat also archives\n" +
				"these samples, so `sar -W -f /var/log/sa/...` reads back yesterday's\n" +
				"data without a live capture.",
			Available: func(si SystemInfo) bool { return si.HasSar },
		},
	}
}

// memoryTopConsumerVariants is the pool for the top-consumers step. `ps`
// (always available) sorts by RSS; `top -bn1 -o %MEM` adds a snapshot view
// with %MEM, VIRT, RES, and SHR — pedagogically richer because it lets the
// learner ask about VIRT-vs-RES and SHR (shared) memory.
func memoryTopConsumerVariants() []stepVariant {
	return []stepVariant{
		{
			Cmd:         "ps -eo pid,rss,comm --sort=-rss | head -10",
			QuestionsFn: psRssColumnQuestions,
			Teaching: "RSS is resident memory in pages. Note: shared pages (libc, mmaped\n" +
				"binaries) are double-counted across processes, so summing RSS can\n" +
				"exceed real memory used. For accurate per-process accounting, look\n" +
				"at PSS via `smem` or /proc/<pid>/smaps_rollup.",
		},
		{
			Cmd:         "top -bn1 -o %MEM | head -20",
			QuestionsFn: topMemColumnQuestions,
			Teaching: "`top -o %MEM` ranks by percentage of physical memory. The key\n" +
				"three columns: VIRT (entire virtual address space, often huge and\n" +
				"misleading), RES (resident in RAM — top's name for RSS), and SHR\n" +
				"(memory shared with other processes). RES − SHR is a rough proxy\n" +
				"for the process's private footprint.",
		},
	}
}

// ----- Comprehension extractors (per-command) -----

// catMemoryQuestions dispatches based on what the captured output looks like
// (since `cat` is a multipurpose command).

var meminfoMarkerRe = regexp.MustCompile(`(?m)^MemTotal:\s`)

func meminfoQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !meminfoMarkerRe.MatchString(c.Output) {
		return nil
	}
	total, tok := parseMeminfoKB(c.Output, "MemTotal")
	free, fok := parseMeminfoKB(c.Output, "MemFree")
	avail, aok := parseMeminfoKB(c.Output, "MemAvailable")
	cached, cok := parseMeminfoKB(c.Output, "Cached")
	buffers, bok := parseMeminfoKB(c.Output, "Buffers")
	swapFree, sfok := parseMeminfoKB(c.Output, "SwapFree")

	var qs []Question
	if fok && aok {
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"This run reported `MemFree` as %s and `MemAvailable` as %s.\n"+
					"What does `MemAvailable` include that makes it the better USE baseline?",
				formatKiBGiB(free), formatKiBGiB(avail)),
			Correct: "An estimate of memory allocatable without swapping, including reclaimable cache",
			Distractors: []string{
				"Memory mapped by user-space processes only",
				"The size of the largest contiguous free block",
				"Memory minus what the kernel has reserved for buffers",
			},
		})
	}
	if tok && aok && total > 0 {
		usedPct := (total - avail) / total * 100
		correct := fmt.Sprintf("%.0f%%", usedPct)
		pool := []string{
			fmt.Sprintf("%.0f%%", clamp(usedPct+25, 0, 100)),
			fmt.Sprintf("%.0f%%", clamp(usedPct-20, 0, 100)),
			fmt.Sprintf("%.0f%%", clamp(usedPct+10, 0, 100)),
			"0%", "25%", "50%", "75%", "99%",
		}
		qs = append(qs, makeRecallQuestion(
			fmt.Sprintf(
				"This run reported `MemTotal` as %s and `MemAvailable` as %s.\n"+
					"About what memory-used percentage does that work out to when reclaimable cache is treated as available?",
				formatKiBGiB(total), formatKiBGiB(avail)),
			correct, pool)...)
	}
	if cok && aok {
		cacheText := formatKiBGiB(cached)
		if bok {
			cacheText = fmt.Sprintf("%s (`Cached`) plus %s (`Buffers`)", formatKiBGiB(cached), formatKiBGiB(buffers))
		}
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"This run reported %s and `MemAvailable` as %s.\n"+
					"If the system feels memory-constrained, what's the right next step?",
				cacheText, formatKiBGiB(avail)),
			Correct: "Check `MemAvailable` — most page cache is reclaimable, so high `Cached` is not itself a problem",
			Distractors: []string{
				"Run `echo 3 > /proc/sys/vm/drop_caches` to free it",
				"Conclude the system is out of memory and needs more RAM",
				"Restart the largest process to release its cached pages",
			},
		})
	}
	if aok {
		correct := fmt.Sprintf("`MemAvailable`: %s", formatKiBGiB(avail))
		var pool []string
		if fok {
			pool = append(pool, fmt.Sprintf("`MemFree`: %s", formatKiBGiB(free)))
		}
		if cok {
			pool = append(pool, fmt.Sprintf("`Cached`: %s", formatKiBGiB(cached)))
		}
		if sfok {
			pool = append(pool, fmt.Sprintf("`SwapFree`: %s", formatKiBGiB(swapFree)))
		}
		qs = append(qs, makeRecallQuestion(
			"From this /proc/meminfo output, which field is the best first estimate of memory available for new allocations without swapping?",
			correct, pool)...)
	}
	return qs
}

func meminfoColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !meminfoMarkerRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableMeminfoQuestionPicks(c.Output),
		func(column string) string {
			return fmt.Sprintf("In `/proc/meminfo`, what does `%s` represent?", column)
		},
	)
}

var meminfoQuestionPicks = []columnQuestionPick{
	{
		Column:  "MemTotal",
		Correct: "Total usable RAM visible to the kernel, in kilobytes",
		Distractors: []string{
			"Total RAM currently free for new allocations",
			"Total memory used by user-space processes only",
			"Total swap plus physical RAM",
		},
	},
	{
		Column:  "MemFree",
		Correct: "Completely unused RAM, not counting reclaimable page cache and buffers",
		Distractors: []string{
			"Memory available for new allocations without swapping",
			"Memory used by filesystem cache",
			"Swap space currently unused",
		},
	},
	{
		Column:  "MemAvailable",
		Correct: "The kernel's estimate of RAM usable for new allocations without swapping",
		Distractors: []string{
			"Completely unused RAM only",
			"Free swap space plus free RAM",
			"Memory currently mapped by running processes",
		},
	},
	{
		Column:  "Buffers",
		Correct: "Memory used for block-device I/O staging and filesystem metadata",
		Distractors: []string{
			"Memory used for executable code pages",
			"Memory used by TCP socket queues only",
			"Pages swapped out to disk",
		},
	},
	{
		Column:  "Cached",
		Correct: "File-backed pages that are mostly reclaimable under memory pressure",
		Distractors: []string{
			"Private anonymous memory owned by processes",
			"Swap space used to store disk blocks",
			"Kernel memory that can never be reclaimed",
		},
	},
	{
		Column:  "SwapCached",
		Correct: "Pages that were swapped out, are back in RAM, and still have a copy in swap",
		Distractors: []string{
			"Free swap space reserved for page cache",
			"Filesystem cache that has been compressed",
			"Total swap activity per second",
		},
	},
	{
		Column:  "Active",
		Correct: "Memory used recently enough that it is less likely to be reclaimed first",
		Distractors: []string{
			"CPU time spent in active user processes",
			"Memory that cannot be paged out",
			"Swap pages actively being read from disk",
		},
	},
	{
		Column:  "Inactive",
		Correct: "Memory not used recently, making it a more likely reclaim candidate",
		Distractors: []string{
			"Memory assigned to stopped processes only",
			"Free memory that has never been allocated",
			"Swap space disabled by the kernel",
		},
	},
	{
		Column:  "SwapTotal",
		Correct: "Total configured swap space, in kilobytes",
		Distractors: []string{
			"Total physical RAM that can be swapped",
			"Amount of swap currently in use",
			"Total pages swapped per second",
		},
	},
	{
		Column:  "SwapFree",
		Correct: "Configured swap space that is currently unused, in kilobytes",
		Distractors: []string{
			"Physical memory available before swap is needed",
			"Swap space currently occupied by anonymous pages",
			"Swap-in operations per second",
		},
	},
}

func availableMeminfoQuestionPicks(output string) []columnQuestionPick {
	var picks []columnQuestionPick
	for _, pick := range meminfoQuestionPicks {
		if outputHasMeminfoField(output, pick.Column) {
			picks = append(picks, pick)
		}
	}
	return picks
}

func outputHasMeminfoField(output, field string) bool {
	prefix := field + ":"
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func formatKiBGiB(kib float64) string {
	return fmt.Sprintf("%.1f GiB", kib/1024/1024)
}

// freeColumnQuestions returns all available column-questions for `free`
// output. Used by the guide step (QuestionCount=3 lets it ask several
// columns per session). For free-form practice mode where one question is
// plenty, `freeQuestions` returns the single MemAvailable comparison.
func freeColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !freeOutputDetected(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, freeQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `free` output, what does the `%s` column report for the `Mem:` row?", column)
		},
	)
}

func freeOutputDetected(output string) bool {
	return strings.Contains(output, "Mem:") && strings.Contains(output, "available")
}

var freeQuestionPicks = []columnQuestionPick{
	{
		Column:  "total",
		Correct: "Total usable physical memory the kernel can see",
		Distractors: []string{
			"Total memory across RAM plus swap",
			"Total memory currently allocated by user processes",
			"Total RAM minus kernel reserved memory",
		},
	},
	{
		Column:  "used",
		Correct: "Total minus free, buff/cache, and (when present) shared",
		Distractors: []string{
			"Memory currently mapped into any process's virtual address space",
			"Memory the kernel has decided cannot be reclaimed",
			"Memory equal to MemTotal minus MemFree, ignoring caches",
		},
	},
	{
		Column:  "free",
		Correct: "Completely unused memory — not counting reclaimable buffers or page cache",
		Distractors: []string{
			"Memory the kernel estimates is available for new allocations without swapping",
			"Memory that has never been touched by any process",
			"Unused swap space plus unused RAM",
		},
	},
	{
		Column:  "shared",
		Correct: "Memory used by tmpfs filesystems and anonymous mappings visible in more than one process",
		Distractors: []string{
			"Memory visible to both application code and the kernel",
			"Memory used by multiple CPUs for IPC",
			"Memory visible both inside containers and on the host",
		},
	},
	{
		Column:  "buff/cache",
		Correct: "Kernel I/O staging plus file-backed pages, most of which is reclaimable under pressure",
		Distractors: []string{
			"Memory permanently reserved by the kernel for I/O staging",
			"Memory used as write-back storage for swap",
			"Memory used by application processes for their own in-process stores",
		},
	},
	{
		Column:  "available",
		Correct: "The kernel's estimate of memory usable for new allocations without swapping (including reclaimable cache)",
		Distractors: []string{
			"Completely unused memory only",
			"Free memory plus free swap",
			"Memory currently held by processes that could be killed cleanly",
		},
	},
}

var vmstatMemHeaderRe = regexp.MustCompile(`(?m)^\s*r\s+b\s+swpd`)

func formatSamples(samples []float64) string {
	if len(samples) == 0 {
		return "[]"
	}
	out := make([]string, 0, len(samples))
	for _, sample := range samples {
		out = append(out, fmt.Sprintf("%.0f", sample))
	}
	return "[" + strings.Join(out, ", ") + "]"
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func psiMemoryQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !psiMemoryHeaderRe.MatchString(c.Output) {
		return nil
	}
	// Randomize whether we ask about `some` or `full` — the two PSI lines
	// measure adjacent but different things, and learners need to distinguish.
	type psiPick struct {
		metric, correct, sibling string
	}
	pick := pickRandom([]psiPick{
		{"some", "The percentage of time when at least one task was stalled on memory", "The percentage of time when all non-idle tasks were stalled on memory at the same time"},
		{"full", "The percentage of time when all non-idle tasks were stalled on memory at the same time", "The percentage of time when at least one task was stalled on memory"},
	})
	qs := []Question{
		{
			Stem:    fmt.Sprintf("In /proc/pressure/memory, what does the `%s` line measure?", pick.metric),
			Correct: pick.correct,
			Distractors: []string{
				pick.sibling, // the OTHER PSI line is the strongest distractor
				"The percentage of physical memory currently in use",
				"The total time the system has spent in any memory pressure since boot",
			},
		},
		{
			Stem:    "Which is the strongest signal that memory saturation is hurting throughput?",
			Correct: "`full avg10` consistently above zero",
			Distractors: []string{
				"`some avg10` near 100% with `full` at zero",
				"`MemFree` below 5% of `MemTotal`",
				"Any non-zero `Cached` value",
			},
		},
	}
	someV, someOK := extractPSIMemory("some")(si, []CapturedCommand{c})
	fullV, fullOK := extractPSIMemory("full")(si, []CapturedCommand{c})
	if someOK && fullOK {
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"This PSI sample reported `some avg10=%.2f` and `full avg10=%.2f`.\n"+
					"Which reading is the stronger saturation signal for throughput?",
				someV.Number, fullV.Number),
			Correct: fmt.Sprintf("`full avg10=%.2f` — it means all non-idle tasks were stalled at the same time during that time-share", fullV.Number),
			Distractors: []string{
				fmt.Sprintf("`some avg10=%.2f` — it is always stronger because it is usually larger", someV.Number),
				"`MemFree` — PSI does not describe stalls",
				"`total` — it directly reports the current percent of memory in use",
			},
		})

		var correct string
		switch {
		case fullV.Number > 0:
			correct = "There was measurable full memory pressure in the avg10 window; tasks were stalled together for part of that interval"
		case someV.Number > 0:
			correct = "Some task stalled on memory, but there was no full-system stall in the avg10 window"
		default:
			correct = "The avg10 window showed no current memory stalls in either PSI line"
		}
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"Given `some avg10=%.2f` and `full avg10=%.2f` from this run, what should you conclude?",
				someV.Number, fullV.Number),
			Correct: correct,
			Distractors: []string{
				"These values are memory utilization percentages, so compare them to 100% used memory",
				"`some` and `full` are cumulative counters, so their avg10 values cannot describe the current window",
				"Any zero in either line proves there is no memory pressure anywhere on the host",
			},
		})
	}
	return qs
}

var psiMemoryHeaderRe = regexp.MustCompile(`(?m)^some avg10=`)

func psiMemoryColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !psiMemoryHeaderRe.MatchString(c.Output) {
		return nil
	}
	return psiColumnQuestions("/proc/pressure/memory", "memory", c.Output)
}

func psiColumnQuestions(path, resource, output string) []Question {
	return columnQuestionsFromPicks(
		availablePSIQuestionPicks(output, resource),
		func(column string) string {
			return fmt.Sprintf("In `%s` output, what does `%s` represent?", path, column)
		},
	)
}

func availablePSIQuestionPicks(output, resource string) []columnQuestionPick {
	picks := []columnQuestionPick{
		{
			Column:  "some",
			Correct: fmt.Sprintf("The pressure line for time when at least one task was stalled on %s", resource),
			Distractors: []string{
				fmt.Sprintf("The pressure line for time when all non-idle tasks were stalled on %s", resource),
				fmt.Sprintf("The percentage of %s capacity currently in use", resource),
				"The total number of stalled tasks since boot",
			},
		},
		{
			Column:  "full",
			Correct: fmt.Sprintf("The pressure line for time when all non-idle tasks were stalled on %s at the same time", resource),
			Distractors: []string{
				fmt.Sprintf("The pressure line for time when at least one task was stalled on %s", resource),
				fmt.Sprintf("The percentage of %s capacity currently free", resource),
				"The number of stalled tasks in the current second",
			},
		},
		{
			Column:  "avg10",
			Correct: "The recent 10-second average stall time, reported as a percentage of wall-clock time",
			Distractors: []string{
				"The cumulative stall count from the last 10 tasks",
				"The 10-minute moving average of resource utilization",
				"The total stall time in milliseconds since boot",
			},
		},
		{
			Column:  "avg60",
			Correct: "The recent 60-second average stall time, reported as a percentage of wall-clock time",
			Distractors: []string{
				"The 60-minute moving average of resource utilization",
				"The cumulative number of stalls in the last 60 seconds",
				"The largest single stall duration in milliseconds",
			},
		},
		{
			Column:  "avg300",
			Correct: "The recent 300-second average stall time, reported as a percentage of wall-clock time",
			Distractors: []string{
				"The 300-millisecond latency percentile for stalled tasks",
				"The total number of stalled tasks sampled",
				"The percentage of resource capacity currently idle",
			},
		},
		{
			Column:  "total",
			Correct: "Total stall time in microseconds for that PSI line",
			Distractors: []string{
				"Total bytes read or written by stalled tasks",
				"Total resource capacity available to the host",
				"The current number of stalled tasks",
			},
		},
	}
	var available []columnQuestionPick
	for _, pick := range picks {
		if outputHasPSIName(output, pick.Column) {
			available = append(available, pick)
		}
	}
	return available
}

func outputHasPSIName(output, name string) bool {
	for _, field := range strings.Fields(output) {
		if field == name || strings.HasPrefix(field, name+"=") {
			return true
		}
	}
	return false
}

func psRssColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !psRssOutputDetected(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, psRSSQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `ps -eo pid,rss,comm` output, what does the `%s` column represent?", column)
		},
	)
}

// psRssOutputDetected gates psRssQuestions/psRssColumnQuestions on the
// presence of an RSS column. `top`-style output uses "RES" instead, so this
// check ensures top output doesn't get routed to ps-shaped questions.
func psRssOutputDetected(output string) bool {
	if !strings.Contains(output, "RSS") && !strings.Contains(strings.ToLower(output), "rss") {
		return false
	}
	// Top output contains "%MEM" + a RES column header; reject so it falls
	// through to topMemColumnQuestions.
	if topOutputDetected(output) {
		return false
	}
	return true
}

// sarWHeaderRe matches the column row of `sar -W` (swap pages per second).
var sarWHeaderRe = regexp.MustCompile(`pswpin/s\s+pswpout/s`)

// sarWQuestions returns one random question — used by the extractor (free-form
// practice mode) where a single comprehension check per capture suffices.

// sarWColumnQuestions returns the full pool, used by the guide step.
func sarWColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarWHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, sarWQuestionPicks),
		sarWStemFor,
	)
}

func sarWStemFor(column string) string {
	return fmt.Sprintf("In `sar -W` output, what does the `%s` column represent?", column)
}

var sarWQuestionPicks = []columnQuestionPick{
	{
		Column:  "pswpin/s",
		Correct: "Pages moved from the swap area back to RAM per second during the sample interval",
		Distractors: []string{
			"Total pages moved back to RAM since boot",
			"Pages of page cache reclaimed per second",
			"Page faults serviced from swap per second",
		},
	},
	{
		Column:  "pswpout/s",
		Correct: "Pages moved from RAM to the swap area per second during the sample interval",
		Distractors: []string{
			"Total pages moved from RAM to backing storage since boot",
			"Pages of page cache evicted per second",
			"Pages written by the page cache writeback thread per second",
		},
	},
}

// topHeaderRe matches the column row of `top -b` output. We require PID,
// USER, VIRT, RES, and %MEM so that a bare uptime/ps capture (which can
// mention %MEM in a process-level table from `ps -o pcpu,pmem`) doesn't
// qualify. Note: \b doesn't help around `%` (not a word char), so we
// match the literal substrings directly.
var topHeaderRe = regexp.MustCompile(`(?m)^\s*PID\s+USER.*VIRT.*RES.*%MEM`)

func topOutputDetected(output string) bool {
	return topHeaderRe.MatchString(output)
}

// topMemQuestions returns one random column question — extractor variant.

// topMemColumnQuestions returns the full pool — guide-step variant.
func topMemColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !topOutputDetected(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, topMemQuestionPicks),
		topMemStemFor,
	)
}

func topMemStemFor(column string) string {
	return fmt.Sprintf("In `top` output sorted by memory, what does the `%s` column represent?", column)
}

var topMemQuestionPicks = []columnQuestionPick{
	{
		Column:  "VIRT",
		Correct: "Total address space the process has reserved, including code, data, shared libraries, and mmap'd files; much of it may not be backed by RAM",
		Distractors: []string{
			"Memory currently in RAM for the process",
			"Memory shared with other processes",
			"Swap space currently used by the process",
		},
	},
	{
		Column:  "RES",
		Correct: "The portion of the process's address space currently backed by RAM (top's name for RSS)",
		Distractors: []string{
			"Memory reserved by the process but never touched",
			"The process's private anonymous memory only",
			"Total memory the process has ever allocated",
		},
	},
	{
		Column:  "SHR",
		Correct: "RAM-backed mappings visible in more than one process, such as libc and mmaped binaries",
		Distractors: []string{
			"Total memory the process has allocated for IPC segments",
			"Swap space mapped by more than one process",
			"Memory visible to the kernel only",
		},
	},
	{
		Column:  "%MEM",
		Correct: "RES as a percentage of total physical memory on the host",
		Distractors: []string{
			"VIRT as a percentage of total swap space",
			"Percentage of memory the process is actively writing to",
			"Percentage of CPU time the process is spending on memory operations",
		},
	},
	{
		Column:  "%CPU",
		Correct: "Recent CPU usage as a percentage of one CPU's capacity (so values above 100% are possible on multi-threaded processes)",
		Distractors: []string{
			"Percentage of total CPU capacity across all logical CPUs",
			"Total CPU time since the process started, as a percent of uptime",
			"Percentage of the user's allotted CPU quota",
		},
	},
}

var psRSSQuestionPicks = []columnQuestionPick{
	{
		Column:  "PID",
		Correct: "The process ID assigned by the kernel",
		Distractors: []string{
			"The parent process ID",
			"The process's CPU core number",
			"The process group memory total",
		},
	},
	{
		Column:  "RSS",
		Correct: "Memory currently backed by RAM for the process, reported in kilobytes by this ps format",
		Distractors: []string{
			"Private memory only, not counting shared libraries",
			"Total virtual address space reserved by the process",
			"Swap space currently used by the process",
		},
	},
	{
		Column:  "COMMAND",
		Correct: "The command name from the `comm` field, not the full argument list",
		Distractors: []string{
			"The full command line including all arguments",
			"The controlling terminal for the process",
			"The cgroup name the process belongs to",
		},
	},
}

func topRSSProcess(output string) (string, float64, bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.EqualFold(fields[1], "RSS") {
			continue
		}
		rss, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		return fields[2], rss, true
	}
	return "", 0, false
}

// ----- Observations -----

var memoryObservations = []Observation{
	{
		Name:    "mem_used_pct",
		Title:   "Memory used",
		Section: "Utilization",
		Extract: extractMemUsedPct,
		// Deliberately no Verdict: on Linux 'used' includes reclaimable
		// page cache, so high used% is routine on healthy systems. The
		// Heuristic explains this if the learner tries to cite it.
		Heuristic: "On Linux 'used' includes reclaimable page cache, so a high used% is not pressure. The real utilization signal is available memory (and saturation = swap activity / PSI).",
	},
	{
		Name:      "mem_available_gib",
		Title:     "Memory available",
		Section:   "Utilization",
		Extract:   extractMemAvailableGiB,
		Verdict:   verdictMemAvailable,
		Heuristic: "available memory in the low-GiB range means real pressure (Linux already counts reclaimable cache toward available)",
	},
	{
		Name:      "cache_buffers_gib",
		Title:     "Cache + buffers",
		Section:   "Utilization",
		Extract:   extractCacheBuffersGiB,
		Heuristic: "cache + buffers is reclaimable memory the kernel hands back under pressure — context only, not a verdict on its own",
	},
	{
		Name:      "swap_used_pct",
		Title:     "Swap used",
		Section:   "Utilization",
		Extract:   extractSwapUsedPct,
		Verdict:   verdictSwapUsed,
		Heuristic: "steady swap use means memory pressure: the kernel is pushing anonymous pages out of RAM",
	},
	{
		Name:      "vmstat_si",
		Title:     "vmstat si (swap-in)",
		Section:   "Saturation",
		Extract:   extractVmstatColumn("si"),
		Verdict:   verdictSwapActivity,
		Heuristic: "vmstat si > 0 = pages being swapped IN from disk = the kernel needed RAM it had paged out (the tool drops vmstat's since-boot first row, so the verdict reflects interval samples only)",
	},
	{
		Name:      "vmstat_so",
		Title:     "vmstat so (swap-out)",
		Section:   "Saturation",
		Extract:   extractVmstatColumn("so"),
		Verdict:   verdictSwapActivity,
		Heuristic: "vmstat so > 0 = pages being swapped OUT to disk = active reclaim under memory pressure (the tool drops vmstat's since-boot first row)",
	},
	{
		Name:      "psi_mem_some_avg10",
		Title:     "PSI memory some (avg10)",
		Section:   "Saturation",
		Extract:   extractPSIMemory("some"),
		Verdict:   verdictPSISome,
		Heuristic: "PSI memory 'some' avg10 = percent of the last 10s window with at least one task stalled on memory; >10% steady = saturation",
	},
	{
		Name:      "psi_mem_full_avg10",
		Title:     "PSI memory full (avg10)",
		Section:   "Saturation",
		Extract:   extractPSIMemory("full"),
		Verdict:   verdictPSIFull,
		Heuristic: "PSI memory 'full' avg10 = percent of the window where ALL non-idle tasks stalled on memory; any steady non-zero = severe saturation",
	},
	{
		Name:      "dmesg_oom_count",
		Title:     "OOM events in dmesg",
		Section:   "Errors",
		Extract:   extractDmesgOOM,
		Verdict:   verdictDmesgOOM,
		Heuristic: "OOM-killer entries in the kernel log = memory has already been used up; the kernel killed processes to reclaim",
	},
}

// ----- Diagnosis verdicts -----

func verdictMemAvailable(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number < 0.5:
		return SignalHigh
	case v.Number < 2:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictSwapUsed(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number >= 50:
		return SignalHigh
	case v.Number >= 5:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictSwapActivity(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Max() > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictPSISome(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number > 10:
		return SignalHigh
	case v.Number > 0:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictPSIFull(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictDmesgOOM(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

// parseMeminfoKB pulls a single key:value (in kB) from /proc/meminfo output
// and returns the value in kibibytes. Returns false if the key isn't found
// or the value can't be parsed.
func parseMeminfoKB(output, key string) (float64, bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		n, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// freeStats holds memory and swap values parsed from `free` output,
// normalised to kibibytes. Cells from `free -h` carry the suffixes the
// kernel printed (`Gi`, `M`, ...) and lose precision in the conversion;
// Rounded is set in that case so callers can annotate the report.
type freeStats struct {
	MemTotalKB     float64
	MemFreeKB      float64
	MemAvailableKB float64
	BuffCacheKB    float64
	SwapTotalKB    float64
	SwapFreeKB     float64
	HasMem         bool
	HasSwap        bool
	HasAvailable   bool
	Rounded        bool
}

// parseFreeOutputKB extracts memory and swap stats from `free` output. It
// supports the unit flags -h (human suffixes), -m, -g, -k (default), and -b.
// The captured command line `cmd` is used to determine the unit for
// unsuffixed cells (a plain "15923" means MiB under `free -m` but KiB
// under default `free`). Suffixed cells (e.g. "1.5Gi", "512M") are parsed
// from the suffix directly. Returns ok=false if the output doesn't look
// like `free` output.
func parseFreeOutputKB(cmd, output string) (freeStats, bool) {
	var s freeStats

	defaultUnitKB := 1.0
	for _, tok := range strings.Fields(cmd) {
		switch tok {
		case "-b":
			defaultUnitKB = 1.0 / 1024.0
		case "-k":
			defaultUnitKB = 1.0
		case "-m":
			defaultUnitKB = 1024.0
		case "-g":
			defaultUnitKB = 1024.0 * 1024.0
		case "-h":
			s.Rounded = true
		}
	}

	lines := strings.Split(output, "\n")
	headerIdx := -1
	var headers []string
	for i, ln := range lines {
		fields := strings.Fields(ln)
		if hasField(fields, "total") && hasField(fields, "used") && hasField(fields, "free") {
			headerIdx = i
			headers = fields
			break
		}
	}
	if headerIdx == -1 {
		return s, false
	}
	colOf := map[string]int{}
	for i, h := range headers {
		colOf[strings.ToLower(h)] = i
	}

	getCell := func(fields []string, name string) (float64, bool) {
		i, ok := colOf[name]
		if !ok || i+1 >= len(fields) {
			return 0, false
		}
		return parseFreeCellKB(fields[i+1], defaultUnitKB)
	}

	for _, ln := range lines[headerIdx+1:] {
		fields := strings.Fields(ln)
		if len(fields) < 2 {
			continue
		}
		label := strings.ToLower(strings.TrimSuffix(fields[0], ":"))
		switch label {
		case "mem":
			if v, ok := getCell(fields, "total"); ok {
				s.MemTotalKB = v
				s.HasMem = true
			}
			if v, ok := getCell(fields, "free"); ok {
				s.MemFreeKB = v
			}
			if v, ok := getCell(fields, "available"); ok {
				s.MemAvailableKB = v
				s.HasAvailable = true
			}
			if v, ok := getCell(fields, "buff/cache"); ok {
				s.BuffCacheKB = v
			}
		case "swap":
			if v, ok := getCell(fields, "total"); ok {
				s.SwapTotalKB = v
				s.HasSwap = true
			}
			if v, ok := getCell(fields, "free"); ok {
				s.SwapFreeKB = v
			}
		}
	}

	if !s.HasMem {
		return s, false
	}
	return s, true
}

// parseFreeCellKB parses a single `free` cell ("15Gi", "3584", "0", "1.5G")
// and returns its value in kibibytes. Suffixes are case-insensitive and
// always base-2 (procps `free` uses IEC even when displaying the bare letter).
// Unsuffixed cells are scaled by defaultUnitKB, which the caller derives
// from the captured command flags.
func parseFreeCellKB(cell string, defaultUnitKB float64) (float64, bool) {
	if cell == "" {
		return 0, false
	}
	i := 0
	for i < len(cell) && (cell[i] == '.' || (cell[i] >= '0' && cell[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(cell[:i], 64)
	if err != nil {
		return 0, false
	}
	suffix := strings.ToLower(strings.TrimSpace(cell[i:]))
	if suffix == "" {
		return n * defaultUnitKB, true
	}
	var factorKB float64
	switch suffix {
	case "b":
		factorKB = 1.0 / 1024.0
	case "k", "ki":
		factorKB = 1.0
	case "m", "mi":
		factorKB = 1024.0
	case "g", "gi":
		factorKB = 1024.0 * 1024.0
	case "t", "ti":
		factorKB = 1024.0 * 1024.0 * 1024.0
	default:
		return 0, false
	}
	return n * factorKB, true
}

func hasField(fields []string, target string) bool {
	for _, f := range fields {
		if strings.ToLower(f) == target {
			return true
		}
	}
	return false
}

// findFreeOutput returns the most recent freeStats parsed from a `free`
// capture, in reverse chronological order. Returns ok=false if no `free`
// capture parses successfully.
func findFreeOutput(caps []CapturedCommand) (freeStats, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		c := caps[i]
		if baseCmd(c.Cmd) != "free" {
			continue
		}
		if fs, ok := parseFreeOutputKB(c.Cmd, c.Output); ok {
			return fs, true
		}
	}
	return freeStats{}, false
}

// findMeminfoOutput locates the most recent CapturedCommand whose output
// looks like /proc/meminfo (so other `cat` invocations don't poison parsing).
func findMeminfoOutput(caps []CapturedCommand) (string, bool) {
	for i := len(caps) - 1; i >= 0; i-- {
		if meminfoMarkerRe.MatchString(caps[i].Output) {
			return caps[i].Output, true
		}
	}
	return "", false
}

func extractMemUsedPct(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	if out, ok := findMeminfoOutput(caps); ok {
		total, tok := parseMeminfoKB(out, "MemTotal")
		avail, aok := parseMeminfoKB(out, "MemAvailable")
		if tok && aok && total > 0 {
			return memUsedPctValue(total, avail, false), true
		}
	}
	if fs, ok := findFreeOutput(caps); ok && fs.HasAvailable && fs.MemTotalKB > 0 {
		return memUsedPctValue(fs.MemTotalKB, fs.MemAvailableKB, fs.Rounded), true
	}
	return Value{}, false
}

func memUsedPctValue(totalKB, availKB float64, rounded bool) Value {
	used := (totalKB - availKB) / totalKB * 100
	note := fmt.Sprintf("%.1f / %.1f GiB used (not counting reclaimable cache)", (totalKB-availKB)/1024/1024, totalKB/1024/1024)
	if rounded {
		note += " — from rounded `free -h`"
	}
	return Value{Number: used, Unit: "%", Note: note}
}

func extractMemAvailableGiB(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	if out, ok := findMeminfoOutput(caps); ok {
		if avail, aok := parseMeminfoKB(out, "MemAvailable"); aok {
			return Value{Number: avail / 1024 / 1024, Unit: " GiB"}, true
		}
	}
	if fs, ok := findFreeOutput(caps); ok && fs.HasAvailable {
		v := Value{Number: fs.MemAvailableKB / 1024 / 1024, Unit: " GiB"}
		if fs.Rounded {
			v.Note = "from rounded `free -h`"
		}
		return v, true
	}
	return Value{}, false
}

func extractCacheBuffersGiB(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	if out, ok := findMeminfoOutput(caps); ok {
		cached, cok := parseMeminfoKB(out, "Cached")
		buffers, bok := parseMeminfoKB(out, "Buffers")
		if cok || bok {
			total := cached + buffers
			return Value{Number: total / 1024 / 1024, Unit: " GiB", Note: "reclaimable"}, true
		}
	}
	if fs, ok := findFreeOutput(caps); ok && fs.BuffCacheKB > 0 {
		note := "reclaimable"
		if fs.Rounded {
			note += " — from rounded `free -h`"
		}
		return Value{Number: fs.BuffCacheKB / 1024 / 1024, Unit: " GiB", Note: note}, true
	}
	return Value{}, false
}

func extractSwapUsedPct(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	if out, ok := findMeminfoOutput(caps); ok {
		total, tok := parseMeminfoKB(out, "SwapTotal")
		free, fok := parseMeminfoKB(out, "SwapFree")
		if tok && fok {
			return swapUsedPctValue(total, free, false), true
		}
	}
	if fs, ok := findFreeOutput(caps); ok && fs.HasSwap {
		return swapUsedPctValue(fs.SwapTotalKB, fs.SwapFreeKB, fs.Rounded), true
	}
	return Value{}, false
}

func swapUsedPctValue(totalKB, freeKB float64, rounded bool) Value {
	if totalKB == 0 {
		return Value{Text: "no swap configured"}
	}
	used := (totalKB - freeKB) / totalKB * 100
	note := fmt.Sprintf("%.1f / %.1f GiB", (totalKB-freeKB)/1024/1024, totalKB/1024/1024)
	if rounded {
		note += " — from rounded `free -h`"
	}
	return Value{Number: used, Unit: "%", Note: note}
}

var psiMemLineRe = regexp.MustCompile(`^(some|full)\s+avg10=([0-9.]+)`)

// extractPSIMemory returns the avg10 value for the requested PSI line ("some"
// or "full") from any captured output that looks like /proc/pressure/memory.
func extractPSIMemory(which string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		for i := len(caps) - 1; i >= 0; i-- {
			out := caps[i].Output
			if !psiMemoryHeaderRe.MatchString(out) {
				continue
			}
			for _, line := range strings.Split(out, "\n") {
				m := psiMemLineRe.FindStringSubmatch(line)
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

func extractDmesgOOM(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	seen := false
	matched := 0
	totalLines := 0
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
			if strings.Contains(low, "killed process") ||
				strings.Contains(low, "out of memory") ||
				strings.Contains(low, "oom-killer") {
				matched++
			}
		}
	}
	if !seen {
		return Value{}, false
	}
	return Value{
		Number: float64(matched),
		Text:   fmt.Sprintf("%d/%d lines mention OOM", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

// ----- Synthesis rules -----

func oomQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if !strings.Contains(low, "out of memory") &&
		!strings.Contains(low, "killed process") &&
		!strings.Contains(low, "oom-killer") {
		return nil
	}
	return []Question{
		{
			Stem:    "When the kernel logs an OOM kill, how does it choose the victim process?",
			Correct: "The process with the highest `oom_score`, biased by RSS and `oom_score_adj`",
			Distractors: []string{
				"The process that requested the allocation that triggered OOM",
				"The most recently started process",
				"The process with the lowest priority (`nice` value)",
			},
		},
		{
			Stem:    "An OOM kill log line includes a memcg path (e.g. `oom-kill: ... oom_memcg=/system.slice/foo.service`).\nWhat does this tell you?",
			Correct: "The OOM was scoped to a cgroup memory limit, not the host running out of memory",
			Distractors: []string{
				"The host ran out of memory and systemd was the trigger",
				"The cgroup is exempt from OOM kills but logged the event",
				"Only services managed by systemd can be OOM-killed",
			},
		},
	}
}

var memoryCommands = []CommandRef{
	{
		Cmd:     "free -h",
		Section: "Utilization",
		Summary: "Total/used/free/available memory and swap, human-readable.\nFastest first look. Read `available`, not `free`.\n`free -m` (MiB) is equivalent and easier to compare exactly;\nthe report ingests either form.",
	},
	{
		Cmd:     "cat /proc/meminfo",
		Section: "Utilization",
		Summary: "Canonical kernel memory accounting in kB.\n`MemAvailable` includes reclaimable cache; `MemFree` does not.",
	},
	{
		Cmd:     "ps -eo pid,rss,comm --sort=-rss | head",
		Section: "Utilization",
		Summary: "Top-RSS processes right now.\nNote: shared pages double-count across processes.",
	},
	{
		Cmd:     "smem -tk",
		Section: "Utilization",
		Summary: "Per-process memory with PSS (proportional set size).\nMore accurate per-process numbers than RSS. (smem package.)",
	},
	{
		Cmd:     "vmstat 1 N",
		Section: "Saturation",
		Summary: "Watch the `si` and `so` columns. Steady non-zero\nvalues mean the working set is bigger than physical memory.",
	},
	{
		Cmd:      "cat /proc/pressure/memory",
		Section:  "Saturation",
		Summary:  "PSI: time-share of tasks stalled on memory.\n`full` > 0 is the strongest saturation signal.\nLinux 4.20+ with PSI enabled (kernel.org/PSI).",
		Requires: []string{"psi"},
	},
	{
		Cmd:      "sar -B 1 N",
		Section:  "Saturation",
		Summary:  "Paging stats (pgpgin/pgpgout/fault/majflt) over time.\n(sysstat package.)",
		Requires: []string{"sar"},
	},
	{
		Cmd:      "sar -W 1 N",
		Section:  "Saturation",
		Summary:  "Swap-in/swap-out rate over time. (sysstat package.)",
		Requires: []string{"sar"},
	},
	{
		Cmd:     "dmesg -T | grep -iE 'killed process|out of memory|oom-killer'",
		Section: "Errors",
		Summary: "OOM kill events from the kernel log.\nLines include victim PID, RSS at kill time, and (if container) memcg path.\n" + dmesgPermissionNote,
	},
	{
		Cmd:                 "journalctl -k -b --no-pager | grep -iE 'killed process|out of memory|oom-killer'",
		Section:             "Errors",
		Summary:             "OOM events via journald.\nAlternative to dmesg on systemd systems.",
		Requires:            []string{"journalctl"},
		HideWhenUnavailable: true,
	},
}
