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
		Name:        "errors",
		Intro:       "Step 5: kernel memory errors mean OOM kills.\nEven if the system is fine *now*, recent OOM events explain flapping services.",
		Suggested:   "dmesg -T 2>/dev/null | grep -iE 'killed process|out of memory|oom-killer' | tail",
		QuestionsFn: oomQuestions,
		AcceptAny:   true,
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
	return []Question{
		{
			Stem:    "In /proc/meminfo, what does `MemAvailable` represent that `MemFree` does not?",
			Correct: "An estimate of memory allocatable without swapping, including reclaimable cache",
			Distractors: []string{
				"Memory mapped by user-space processes only",
				"The size of the largest contiguous free block",
				"Memory minus what the kernel has reserved for buffers",
			},
		},
		{
			Stem:    "If `Cached` is 6 GiB and the system feels memory-constrained, what's the right next step?",
			Correct: "Check `MemAvailable` — most page cache is reclaimable, so high `Cached` is not itself a problem",
			Distractors: []string{
				"Run `echo 3 > /proc/sys/vm/drop_caches` to free it",
				"Conclude the system is out of memory and needs more RAM",
				"Restart the largest process to release its cached pages",
			},
		},
	}
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
	return []Question{
		{
			Stem:    "In `vmstat` output, what does the `si` column under `swap` represent?",
			Correct: "Pages swapped in from swap to memory per second",
			Distractors: []string{
				"Pages swapped out from memory to swap per second",
				"Number of context switches per second",
				"Free swap space in kilobytes",
			},
		},
		{
			Stem:    "If `vmstat` shows `si`/`so` consistently at zero but the system feels slow, what should you check next?",
			Correct: "PSI (/proc/pressure/memory) — pressure can exist at the cgroup level without host-level swap activity",
			Distractors: []string{
				"The `swpd` column to confirm swap is configured",
				"`free -h` again, since vmstat samples are often stale",
				"Disable swap and re-test — swap is the only source of memory pressure",
			},
		},
	}
}

var psiMemoryHeaderRe = regexp.MustCompile(`(?m)^some avg10=`)

func psiMemoryQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !psiMemoryHeaderRe.MatchString(c.Output) {
		return nil
	}
	return []Question{
		{
			Stem:    "In /proc/pressure/memory, what does the `full` line measure?",
			Correct: "The percentage of time during which all non-idle tasks were simultaneously stalled on memory",
			Distractors: []string{
				"The percentage of physical memory currently in use",
				"The total time the system has spent in any memory pressure since boot",
				"The percentage of memory that is fully allocated and non-reclaimable",
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
}

func psRssQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !strings.Contains(c.Output, "RSS") && !strings.Contains(strings.ToLower(c.Output), "rss") {
		return nil
	}
	return []Question{{
		Stem:    "Summing RSS across all processes can exceed total physical memory used. Why?",
		Correct: "Shared pages (libc, mmaped binaries) are counted once in each process's RSS",
		Distractors: []string{
			"RSS includes swapped-out pages",
			"RSS is reported in pages, not bytes, so the unit is misleading",
			"The kernel double-counts pages that are also in the page cache",
		},
	}}
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
		if !strings.Contains(c.Cmd, "dmesg") && !strings.Contains(c.Cmd, "journalctl") {
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
	return []Question{{
		Stem:    "What memory-used percentage did you observe (treating reclaimable cache as available)?",
		Correct: correct,
		Distractors: []string{
			fmt.Sprintf("%.0f%%", clamp(v.Number+25, 0, 100)),
			fmt.Sprintf("%.0f%%", clamp(v.Number-20, 0, 100)),
			"99%",
		},
	}}
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
		return []Question{{
			Stem:    fmt.Sprintf("What was the highest `%s` (swap) sample you observed in vmstat?", col),
			Correct: fmt.Sprintf("%.0f", max),
			Distractors: []string{
				fmt.Sprintf("%.0f", max+10),
				fmt.Sprintf("%.0f", max*5+1),
				"0",
			},
		}}
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
		Cmd:     "dmesg -T 2>/dev/null | grep -iE 'killed process|out of memory|oom-killer'",
		Section: "Errors",
		Summary: "OOM kill events from the kernel log.\nLines include victim PID, RSS at kill time, and (if container) memcg path.",
	},
	{
		Cmd:     "journalctl -k -p err --since '1 day ago' | grep -iE 'oom|out of memory'",
		Section: "Errors",
		Summary: "OOM events via journald.\nAlternative to dmesg on systemd systems.",
	},
}
