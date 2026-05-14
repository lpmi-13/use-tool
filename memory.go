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
		"and asks targeted questions about what you observed.",
	StepsFn:        memorySteps,
	Extractors:     memoryExtractors,
	Observations:   memoryObservations,
	SynthesisRules: memorySynthesisRules,
	Commands:       memoryCommands,
}

// ----- Guide steps -----

func memorySteps(si SystemInfo) []GuideStep {
	steps := []GuideStep{
		{
			Name:          "baseline",
			Intro:         "Step 1: get a baseline of total, used, available memory and swap.\n/proc/meminfo is the canonical source the kernel exposes.",
			Suggested:     "cat /proc/meminfo",
			QuestionsFn:   meminfoColumnQuestions,
			QuestionCount: 3,
			Teaching: "Read `MemAvailable`, not `MemFree`. `MemFree` excludes reclaimable\n" +
				"page cache; `MemAvailable` is the kernel's own estimate of how much\n" +
				"memory is allocatable without going to swap. The two can differ by\n" +
				"many gigabytes on a busy server.",
		},
		{
			Name:          "swap-activity",
			Intro:         "Step 2: vmstat shows pages swapped in (`si`) and out (`so`).\nNon-zero values mean the system is paging — working set exceeds RAM.",
			Suggested:     "vmstat 1 5",
			QuestionsFn:   vmstatColumnQuestions,
			QuestionCount: 3,
			Teaching: "Sustained `si`/`so` > 0 means working set exceeds physical memory.\n" +
				"On its own this is suggestive; pair with PSI or latency observations\n" +
				"to confirm tasks are actually being slowed down.",
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
		Suggested:     "ps -eo pid,rss,comm --sort=-rss | head -10",
		QuestionsFn:   psRssColumnQuestions,
		QuestionCount: 3,
		AcceptAny:     true,
		Teaching: "RSS is resident memory in pages. Note: shared pages (libc, mmaped\n" +
			"binaries) are double-counted across processes, so summing RSS can\n" +
			"exceed real memory used. For accurate per-process accounting, look\n" +
			"at PSS via `smem` or /proc/<pid>/smaps_rollup.",
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

// ----- Comprehension extractors (per-command) -----

var memoryExtractors = []Extractor{
	{BaseCmd: "cat", QuestionsFn: catMemoryQuestions},
	{BaseCmd: "free", QuestionsFn: freeQuestions},
	{BaseCmd: "vmstat", QuestionsFn: vmstatMemoryQuestions},
	{BaseCmd: "ps", QuestionsFn: psRssQuestions},
	{BaseCmd: "dmesg", QuestionsFn: oomQuestions},
	{BaseCmd: "journalctl", QuestionsFn: oomQuestions},
}

// catMemoryQuestions dispatches based on what the captured output looks like
// (since `cat` is a multipurpose command).
func catMemoryQuestions(si SystemInfo, c CapturedCommand) []Question {
	var qs []Question
	qs = append(qs, meminfoQuestions(si, c)...)
	qs = append(qs, psiMemoryQuestions(si, c)...)
	return qs
}

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
					"About what memory-used percentage does that imply when reclaimable cache is treated as available?",
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
		Correct: "Completely unused RAM, excluding reclaimable page cache and buffers",
		Distractors: []string{
			"Memory available for new allocations without swapping",
			"Memory used by filesystem cache",
			"Swap space currently unused",
		},
	},
	{
		Column:  "MemAvailable",
		Correct: "The kernel's estimate of memory available for new allocations without swapping",
		Distractors: []string{
			"Completely unused RAM only",
			"Free swap space plus free RAM",
			"Memory currently mapped by running processes",
		},
	},
	{
		Column:  "Buffers",
		Correct: "Memory used for block-device buffers and filesystem metadata",
		Distractors: []string{
			"Memory used for executable code pages",
			"Memory used by TCP socket buffers only",
			"Pages swapped out to disk",
		},
	},
	{
		Column:  "Cached",
		Correct: "File-backed page cache that is mostly reclaimable under memory pressure",
		Distractors: []string{
			"Private anonymous memory owned by processes",
			"Swap space used as a cache for disk blocks",
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

func freeQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "Mem:") || !strings.Contains(c.Output, "available") {
		return nil
	}
	return []Question{{
		Stem:    "In `free` output, the `available` column differs from `free`. Which best describes `available`?",
		Correct: "An estimate of how much memory is usable for new allocations without swapping",
		Distractors: []string{
			"Memory currently mapped but not yet faulted in",
			"Free memory minus kernel reserved memory",
			"Free memory plus swap free",
		},
	}}
}

var vmstatMemHeaderRe = regexp.MustCompile(`(?m)^\s*r\s+b\s+swpd`)

func vmstatMemoryQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !vmstatMemHeaderRe.MatchString(c.Output) {
		return nil
	}
	// Randomize whether we ask about `si` or `so` so the learner has to
	// distinguish direction rather than memorising one column.
	type swapPick struct {
		col, correct, sibling string
	}
	pick := pickRandom([]swapPick{
		{"si", "Pages swapped in from swap to memory per second", "Pages swapped out from memory to swap per second"},
		{"so", "Pages swapped out from memory to swap per second", "Pages swapped in from swap to memory per second"},
	})
	qs := []Question{
		{
			Stem:    fmt.Sprintf("In `vmstat` output, what does the `%s` column under `swap` represent?", pick.col),
			Correct: pick.correct,
			Distractors: []string{
				pick.sibling, // the OTHER direction is the strongest distractor
				"Number of context switches per second",
				"Free swap space in kilobytes",
			},
		},
	}

	siV, siOK := extractVmstatColumn("si")(si, []CapturedCommand{c})
	soV, soOK := extractVmstatColumn("so")(si, []CapturedCommand{c})
	if siOK && soOK {
		swapMax := maxFloat(siV.Max(), soV.Max())
		correct := fmt.Sprintf("%.0f", swapMax)
		pool := []string{
			fmt.Sprintf("%.0f", swapMax+10),
			fmt.Sprintf("%.0f", swapMax+50),
			fmt.Sprintf("%.0f", swapMax*5+1),
			"0", "10", "100", "1000",
		}
		qs = append(qs, makeRecallQuestion(
			fmt.Sprintf(
				"This `vmstat` run reported `si` samples %s and `so` samples %s.\n"+
					"What was the highest observed swap-activity sample across those two columns?",
				formatSamples(siV.Samples), formatSamples(soV.Samples)),
			correct, pool)...)

		var correctInterpretation string
		var distractors []string
		if swapMax == 0 {
			correctInterpretation = "No host-level swap-in or swap-out activity was observed in these samples; check PSI if the system still feels slow"
			distractors = []string{
				"Memory pressure is impossible because swap activity is zero",
				"The system is definitely swapping because `swpd` was present in the header",
				"The `si`/`so` columns show free swap capacity, not activity",
			}
		} else {
			correctInterpretation = "At least one sample showed paging activity, so the working set may be exceeding RAM during the sample window"
			distractors = []string{
				"Non-zero `si`/`so` is harmless because it only counts page cache reclamation",
				"Swap activity rules out memory pressure and points to disk errors only",
				"The `si`/`so` columns show free swap capacity, not activity",
			}
		}
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"This `vmstat` run had max `si` %.0f and max `so` %.0f.\n"+
					"Which interpretation best fits these samples?",
				siV.Max(), soV.Max()),
			Correct:     correctInterpretation,
			Distractors: distractors,
		})
		if swapMax == 0 {
			qs = append(qs, Question{
				Stem: fmt.Sprintf(
					"This `vmstat` run showed max `si` %.0f and max `so` %.0f.\n"+
						"If the system still feels slow, what should you check next?",
					siV.Max(), soV.Max()),
				Correct: "PSI (/proc/pressure/memory) — pressure can exist at the cgroup level without host-level swap activity",
				Distractors: []string{
					"The `swpd` column to confirm swap is configured",
					"`free -h` again, since vmstat samples are often stale",
					"Disable swap and re-test — swap is the only source of memory pressure",
				},
			})
		}
	}
	return qs
}

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
		{"some", "The percentage of time during which at least one task was stalled on memory", "The percentage of time during which all non-idle tasks were simultaneously stalled on memory"},
		{"full", "The percentage of time during which all non-idle tasks were simultaneously stalled on memory", "The percentage of time during which at least one task was stalled on memory"},
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
			Stem:    "Which is the strongest signal that memory saturation is harming throughput?",
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
			Correct: fmt.Sprintf("`full avg10=%.2f` — it means all non-idle tasks were simultaneously stalled during that time-share", fullV.Number),
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
				"The cumulative number of stalled tasks since boot",
			},
		},
		{
			Column:  "full",
			Correct: fmt.Sprintf("The pressure line for time when all non-idle tasks were simultaneously stalled on %s", resource),
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
			Correct: "Cumulative stall time in microseconds for that PSI line",
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

func psRssQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "RSS") && !strings.Contains(strings.ToLower(c.Output), "rss") {
		return nil
	}
	qs := []Question{{
		Stem:    "Summing RSS across all processes can exceed total physical memory used. Why?",
		Correct: "Shared pages (libc, mmaped binaries) are counted once in each process's RSS",
		Distractors: []string{
			"RSS includes swapped-out pages",
			"RSS is reported in pages, not bytes, so the unit is misleading",
			"The kernel double-counts pages that are also in the page cache",
		},
	}}
	if proc, rss, ok := topRSSProcess(c.Output); ok {
		qs = append(qs, Question{
			Stem: fmt.Sprintf(
				"In this `ps` output, the largest RSS entry is `%s` at %s.\n"+
					"What is the most important caveat when interpreting that number?",
				proc, formatKiBGiB(rss)),
			Correct: "RSS includes shared pages, so this is not the process's fully private memory footprint",
			Distractors: []string{
				"RSS includes memory that has already been swapped out",
				"RSS is a cumulative lifetime allocation counter",
				"RSS is the amount of swap reserved for the process",
			},
		})
	}
	return qs
}

func psRssColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "RSS") && !strings.Contains(strings.ToLower(c.Output), "rss") {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, psRSSQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In `ps -eo pid,rss,comm` output, what does the `%s` column represent?", column)
		},
	)
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
		Correct: "Resident set size: memory currently resident in RAM for the process, reported in kilobytes by this ps format",
		Distractors: []string{
			"Private memory only, excluding shared libraries",
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

// ----- Observations -----

var memoryObservations = []Observation{
	{
		Name:    "mem_used_pct",
		Title:   "Memory used",
		Section: "Utilization",
		Extract: extractMemUsedPct,
		Recall:  memUsedPctRecall,
	},
	{
		Name:    "mem_available_gib",
		Title:   "Memory available",
		Section: "Utilization",
		Extract: extractMemAvailableGiB,
	},
	{
		Name:    "cache_buffers_gib",
		Title:   "Cache + buffers",
		Section: "Utilization",
		Extract: extractCacheBuffersGiB,
	},
	{
		Name:    "swap_used_pct",
		Title:   "Swap used",
		Section: "Utilization",
		Extract: extractSwapUsedPct,
	},
	{
		Name:    "vmstat_si",
		Title:   "vmstat si (swap-in)",
		Section: "Saturation",
		Extract: extractVmstatColumn("si"),
		Recall:  swapSamplesRecall("si"),
	},
	{
		Name:    "vmstat_so",
		Title:   "vmstat so (swap-out)",
		Section: "Saturation",
		Extract: extractVmstatColumn("so"),
	},
	{
		Name:    "psi_mem_some_avg10",
		Title:   "PSI memory some (avg10)",
		Section: "Saturation",
		Extract: extractPSIMemory("some"),
	},
	{
		Name:    "psi_mem_full_avg10",
		Title:   "PSI memory full (avg10)",
		Section: "Saturation",
		Extract: extractPSIMemory("full"),
	},
	{
		Name:    "dmesg_oom_count",
		Title:   "OOM events in dmesg",
		Section: "Errors",
		Extract: extractDmesgOOM,
	},
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
	note := fmt.Sprintf("%.1f / %.1f GiB used (excluding reclaimable cache)", (totalKB-availKB)/1024/1024, totalKB/1024/1024)
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
		Text: fmt.Sprintf("%d/%d lines mention OOM", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

func memUsedPctRecall(v Value) []Question {
	correct := fmt.Sprintf("%.0f%%", v.Number)
	pool := []string{
		fmt.Sprintf("%.0f%%", clamp(v.Number+25, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(v.Number-20, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(v.Number+10, 0, 100)),
		"0%", "25%", "50%", "75%", "99%",
	}
	return makeRecallQuestion(
		"What memory-used percentage did you observe (treating reclaimable cache as available)?",
		correct, pool)
}

func swapSamplesRecall(col string) func(Value) []Question {
	return func(v Value) []Question {
		if len(v.Samples) == 0 {
			return nil
		}
		max := v.Samples[0]
		for _, x := range v.Samples {
			if x > max {
				max = x
			}
		}
		correct := fmt.Sprintf("%.0f", max)
		pool := []string{
			fmt.Sprintf("%.0f", max+10),
			fmt.Sprintf("%.0f", max+50),
			fmt.Sprintf("%.0f", max*5+1),
			"0", "10", "100", "1000",
		}
		return makeRecallQuestion(
			fmt.Sprintf("What was the highest `%s` (swap) sample you observed in vmstat?", col),
			correct, pool)
	}
}

// ----- Synthesis rules -----

var memorySynthesisRules = []SynthesisRule{
	swapPSIConsistency,
}

var swapPSIConsistency = SynthesisRule{
	Requires: []string{"vmstat_si", "vmstat_so", "psi_mem_full_avg10"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		siV := vs["vmstat_si"]
		soV := vs["vmstat_so"]
		psiFull := vs["psi_mem_full_avg10"].Number

		swapMax := 0.0
		for _, x := range siV.Samples {
			if x > swapMax {
				swapMax = x
			}
		}
		for _, x := range soV.Samples {
			if x > swapMax {
				swapMax = x
			}
		}

		var correct string
		switch {
		case swapMax == 0 && psiFull < 0.5:
			correct = "Consistent — no swap activity matches near-zero PSI full; the system is not under memory pressure."
		case swapMax > 0 && psiFull > 1:
			correct = "Consistent — paging activity matches measurable PSI full stalls; tasks are being slowed by memory."
		case swapMax == 0 && psiFull > 5:
			correct = "Inconsistent at the host level — PSI shows stalls but no host swap activity, which often means cgroup-level memory pressure (a container hit its limit)."
		case swapMax > 5 && psiFull < 0.5:
			correct = "Unusual — visible paging but very low PSI full; could be fast swap (NVMe) absorbing the cost, or a sampling artifact."
		default:
			return Question{}, false
		}

		pool := []string{
			"Consistent — no swap activity matches near-zero PSI full; the system is not under memory pressure.",
			"Consistent — paging activity matches measurable PSI full stalls; tasks are being slowed by memory.",
			"Inconsistent at the host level — PSI shows stalls but no host swap activity, which often means cgroup-level memory pressure (a container hit its limit).",
			"Unusual — visible paging but very low PSI full; could be fast swap (NVMe) absorbing the cost, or a sampling artifact.",
			"Cannot be compared — PSI and vmstat measure entirely unrelated subsystems.",
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
				"Highest swap-activity sample (max of `si`/`so`) was %.0f.\n"+
					"PSI memory `full avg10` was %.2f%%.\n"+
					"Which best describes the relationship between these two readings?",
				swapMax, psiFull),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// ----- Command reference -----

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
		Summary: "Per-process memory with PSS (proportional set size).\nMore accurate attribution than RSS. (smem package.)",
	},
	{
		Cmd:     "vmstat 1 N",
		Section: "Saturation",
		Summary: "Watch the `si` and `so` columns. Sustained non-zero\nvalues mean the working set exceeds physical memory.",
	},
	{
		Cmd:     "cat /proc/pressure/memory",
		Section: "Saturation",
		Summary: "PSI: time-share of tasks stalled on memory.\n`full` > 0 is the strongest saturation signal.\nLinux 4.20+ with PSI enabled (kernel.org/PSI).",
	},
	{
		Cmd:     "sar -B 1 N",
		Section: "Saturation",
		Summary: "Paging stats (pgpgin/pgpgout/fault/majflt) over time.\n(sysstat package.)",
	},
	{
		Cmd:     "sar -W 1 N",
		Section: "Saturation",
		Summary: "Swap-in/swap-out rate over time. (sysstat package.)",
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
