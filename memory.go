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
			Name:        "baseline",
			Intro:       "Step 1: get a baseline of total, used, available memory and swap.\n/proc/meminfo is the canonical source the kernel exposes.",
			Suggested:   "cat /proc/meminfo",
			QuestionsFn: meminfoQuestions,
			Teaching: "Read `MemAvailable`, not `MemFree`. `MemFree` excludes reclaimable\n" +
				"page cache; `MemAvailable` is the kernel's own estimate of how much\n" +
				"memory is allocatable without going to swap. The two can differ by\n" +
				"many gigabytes on a busy server.",
		},
		{
			Name:        "swap-activity",
			Intro:       "Step 2: vmstat shows pages swapped in (`si`) and out (`so`).\nNon-zero values mean the system is paging — working set exceeds RAM.",
			Suggested:   "vmstat 1 5",
			QuestionsFn: vmstatMemoryQuestions,
			Teaching: "Sustained `si`/`so` > 0 means working set exceeds physical memory.\n" +
				"On its own this is suggestive; pair with PSI or latency observations\n" +
				"to confirm tasks are actually being slowed down.",
		},
	}
	if si.HasPSI {
		steps = append(steps, GuideStep{
			Name:        "pressure",
			Intro:       "Step 3: PSI reports the time-share of tasks stalled on memory.\nLinux 4.20+ with PSI enabled in the kernel.",
			Suggested:   "cat /proc/pressure/memory",
			QuestionsFn: psiMemoryQuestions,
			Teaching: "`some` is time at least one task was stalled on memory; `full` is\n" +
				"time *all* non-idle tasks were stalled. `full` > 0 is the strongest\n" +
				"saturation signal — it means the system was actually wedged, not just\n" +
				"under load.",
		})
	}
	steps = append(steps, GuideStep{
		Name:        "top-consumers",
		Intro:       "Step 4: who's actually using memory?",
		Suggested:   "ps -eo pid,rss,comm --sort=-rss | head -10",
		QuestionsFn: psRssQuestions,
		AcceptAny:   true,
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
	out, ok := findMeminfoOutput(caps)
	if !ok {
		return Value{}, false
	}
	total, tok := parseMeminfoKB(out, "MemTotal")
	avail, aok := parseMeminfoKB(out, "MemAvailable")
	if !tok || !aok || total <= 0 {
		return Value{}, false
	}
	used := (total - avail) / total * 100
	return Value{
		Number: used,
		Unit:   "%",
		Note:   fmt.Sprintf("%.1f / %.1f GiB used (excluding reclaimable cache)", (total-avail)/1024/1024, total/1024/1024),
	}, true
}

func extractMemAvailableGiB(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findMeminfoOutput(caps)
	if !ok {
		return Value{}, false
	}
	avail, aok := parseMeminfoKB(out, "MemAvailable")
	if !aok {
		return Value{}, false
	}
	return Value{Number: avail / 1024 / 1024, Unit: " GiB"}, true
}

func extractCacheBuffersGiB(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findMeminfoOutput(caps)
	if !ok {
		return Value{}, false
	}
	cached, cok := parseMeminfoKB(out, "Cached")
	buffers, bok := parseMeminfoKB(out, "Buffers")
	if !cok && !bok {
		return Value{}, false
	}
	total := cached + buffers
	return Value{Number: total / 1024 / 1024, Unit: " GiB", Note: "reclaimable"}, true
}

func extractSwapUsedPct(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	out, ok := findMeminfoOutput(caps)
	if !ok {
		return Value{}, false
	}
	total, tok := parseMeminfoKB(out, "SwapTotal")
	free, fok := parseMeminfoKB(out, "SwapFree")
	if !tok || !fok {
		return Value{}, false
	}
	if total == 0 {
		return Value{Text: "no swap configured"}, true
	}
	used := (total - free) / total * 100
	return Value{
		Number: used,
		Unit:   "%",
		Note:   fmt.Sprintf("%.1f / %.1f GiB", (total-free)/1024/1024, total/1024/1024),
	}, true
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
		Summary: "Total/used/free/available memory and swap, human-readable.\nFastest first look. Read `available`, not `free`.",
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
