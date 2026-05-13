package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var cpuInvestigation = &Investigation{
	Name:  "cpu",
	Title: "CPU — Utilization, Saturation, Errors",
	Description: "Investigate CPU using Brendan Gregg's USE method.\n" +
		"Run commands at the prompt; the harness captures their output\n" +
		"and asks targeted questions about what you observed.",
	StepsFn:        cpuSteps,
	Extractors:     cpuExtractors,
	Observations:   cpuObservations,
	SynthesisRules: cpuSynthesisRules,
	Commands:       cpuCommands,
}

// ----- Guide steps -----

func cpuSteps(si SystemInfo) []GuideStep {
	steps := []GuideStep{
		{
			Name:        "loadavg",
			Intro:       "Step 1: Load averages give a coarse picture of run-queue pressure\nover 1, 5, and 15-minute windows.",
			Suggested:   "uptime",
			QuestionsFn: uptimeQuestions,
			Teaching: fmt.Sprintf(
				"Rule of thumb: a 1-minute load average above %d (= number of logical CPUs on this machine)\n"+
					"indicates more runnable processes than CPUs — the run-queue is saturated.\n"+
					"The 5- and 15-minute values show whether that's a transient spike or sustained.",
				si.NumCPU),
		},
	}

	if si.HasMpstat {
		steps = append(steps, GuideStep{
			Name:        "per-cpu",
			Intro:       "Step 2: mpstat breaks down utilization per logical CPU.\nThis distinguishes \"all cores busy\" from \"one core pegged.\"",
			Suggested:   "mpstat -P ALL 1 3",
			QuestionsFn: mpstatQuestions,
			Teaching: "Look at %idle per CPU. Uniformly low means saturation; one CPU near 0%\n" +
				"with others near 100% means a single-thread bottleneck.",
		})
	}

	steps = append(steps, GuideStep{
		Name:        "runqueue",
		Intro:       "Step 3: vmstat shows the run-queue length (`r`) and CPU breakdown\n(us/sy/id/wa) over short intervals.",
		Suggested:   "vmstat 1 3",
		QuestionsFn: vmstatQuestions,
		Teaching: fmt.Sprintf(
			"If `r` consistently exceeds %d (= NumCPU on this system), the run-queue is\n"+
				"saturated. High `wa` means CPUs are idle waiting on I/O — the bottleneck is\n"+
				"storage, not CPU. Non-zero `st` means the hypervisor is giving cycles to\n"+
				"another tenant; on cloud VMs, sustained `st` is contention you can't fix\n"+
				"from inside the guest.",
			si.NumCPU),
	})

	steps = append(steps, GuideStep{
		Name:               "errors",
		Intro:              "Step 4: kernel errors (the 'E' in USE) surface in dmesg —\nMCE events, thermal throttling, hardware faults.\n" + dmesgPermissionNote,
		Suggested:          "dmesg --level=err,warn | tail -20",
		QuestionsFn:        dmesgQuestions,
		AcceptAny:          true,
		EmptyOutputMessage: "No CPU, thermal, or machine-check errors found.",
		Teaching: "Recent MCE (machine-check exception) or thermal throttling messages indicate\n" +
			"physical CPU problems; absence is the healthy case. On idle laptops you'll\n" +
			"typically see nothing here — that's fine.",
	})

	return steps
}

// ----- Comprehension extractors (per-command) -----

var cpuExtractors = []Extractor{
	{BaseCmd: "uptime", QuestionsFn: uptimeQuestions},
	{BaseCmd: "w", QuestionsFn: uptimeQuestions},
	{BaseCmd: "vmstat", QuestionsFn: vmstatQuestions},
	{BaseCmd: "mpstat", QuestionsFn: mpstatQuestions},
	{BaseCmd: "dmesg", QuestionsFn: dmesgQuestions},
}

var loadAvgRe = regexp.MustCompile(`load average:\s*([0-9.]+),\s*([0-9.]+),\s*([0-9.]+)`)

func uptimeQuestions(si SystemInfo, c CapturedCommand) []Question {
	m := loadAvgRe.FindStringSubmatch(c.Output)
	if m == nil {
		return nil
	}
	// Pick which of the three positions to ask about so the question doesn't
	// always test the same one. Phrased by position word ("first" / "middle" /
	// "last") rather than by value so the question stays answerable even when
	// all three load averages are identical (common right after boot, when
	// all three are 0.00).
	type loadavgPick struct {
		position string
		correct  string
	}
	pick := pickRandom([]loadavgPick{
		{"first", "The 1-minute load average"},
		{"middle", "The 5-minute load average"},
		{"last", "The 15-minute load average"},
	})
	distractorPool := []string{
		"The 1-minute load average",
		"The 5-minute load average",
		"The 15-minute load average",
		"The current number of running processes",
	}
	return []Question{{
		Stem: fmt.Sprintf(
			"You ran a command whose output included:\n  load average: %s, %s, %s\nWhat does the *%s* of these three numbers refer to?",
			m[1], m[2], m[3], pick.position),
		Correct:     pick.correct,
		Distractors: pickUniqueDistractors(pick.correct, distractorPool, 3),
	}}
}

var vmstatHeaderRe = regexp.MustCompile(`(?m)^\s*r\s+b\s+swpd`)

func vmstatQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !vmstatHeaderRe.MatchString(c.Output) {
		return nil
	}
	pick := pickRandom(vmstatCPUQuestionPicks)
	return []Question{{
		Stem:        fmt.Sprintf("In the `vmstat` output, what does the `%s` column represent?", pick.Column),
		Correct:     pick.Correct,
		Distractors: pick.Distractors,
	}}
}

type columnQuestionPick struct {
	Column      string
	Correct     string
	Distractors []string
}

var vmstatCPUQuestionPicks = []columnQuestionPick{
	{
		Column:  "r",
		Correct: "The number of runnable processes (in the run-queue, including those running)",
		Distractors: []string{
			"The number of processes blocked waiting on I/O",
			"Free memory in kilobytes",
			"The rate of system calls per second",
		},
	},
	{
		Column:  "b",
		Correct: "The number of processes blocked in uninterruptible sleep, usually waiting on I/O",
		Distractors: []string{
			"The number of runnable processes in the run-queue",
			"The number of bytes read from block devices per second",
			"The number of CPU cores currently busy",
		},
	},
	{
		Column:  "us",
		Correct: "Percent of CPU time spent running user-space code",
		Distractors: []string{
			"Percent of CPU time spent running kernel code",
			"Percent of CPU time spent idle",
			"Percent of CPU time stolen by the hypervisor",
		},
	},
	{
		Column:  "sy",
		Correct: "Percent of CPU time spent running kernel code",
		Distractors: []string{
			"Percent of CPU time spent running user-space code",
			"Percent of CPU time spent idle while waiting on I/O",
			"Percent of CPU time spent by niced processes",
		},
	},
	{
		Column:  "id",
		Correct: "Percent of CPU time spent idle",
		Distractors: []string{
			"Percent of CPU time spent waiting on I/O",
			"Percent of CPU time spent in the kernel",
			"Percent of CPU time stolen by the hypervisor",
		},
	},
	{
		Column:  "wa",
		Correct: "Percent of CPU time spent idle while waiting on outstanding I/O",
		Distractors: []string{
			"Percent of CPU time spent in the kernel",
			"Number of threads waiting on a wakeup",
			"Average wait time per I/O operation in milliseconds",
		},
	},
	{
		Column:  "st",
		Correct: "Percent of CPU time stolen by the hypervisor for other virtual machines",
		Distractors: []string{
			"Percent of CPU time spent waiting on local disk I/O",
			"Percent of CPU time spent servicing software interrupts",
			"Percent of CPU time reserved for system daemons",
		},
	},
}

var mpstatHeaderRe = regexp.MustCompile(`%idle`)

func mpstatQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !mpstatHeaderRe.MatchString(c.Output) {
		return nil
	}
	picks := availableMpstatQuestionPicks(c.Output)
	if len(picks) == 0 {
		return nil
	}
	pick := pickRandom(picks)
	return []Question{{
		Stem:        fmt.Sprintf("In the `mpstat` output, what does the `%s` column represent?", pick.Column),
		Correct:     pick.Correct,
		Distractors: pick.Distractors,
	}}
}

type mpstatQuestionPick struct {
	Column      string
	Correct     string
	Distractors []string
}

var mpstatQuestionPicks = []mpstatQuestionPick{
	{
		Column:  "CPU",
		Correct: "The logical CPU identifier for the row; `all` is the aggregate across CPUs",
		Distractors: []string{
			"The percentage of total CPU capacity used by the row",
			"The process ID currently running on that CPU",
			"The physical socket number only, excluding logical CPUs",
		},
	},
	{
		Column:  "%usr",
		Correct: "Percent of CPU time spent running user-space code",
		Distractors: []string{
			"Percent of CPU time spent in the kernel",
			"Percent of CPU time spent idle",
			"Percent of CPU time spent waiting on outstanding disk I/O",
		},
	},
	{
		Column:  "%nice",
		Correct: "Percent of CPU time spent running niced user-space processes",
		Distractors: []string{
			"Percent of CPU time spent running normal-priority user-space code",
			"Percent of CPU time spent handling software interrupts",
			"Percent of CPU time taken by the hypervisor",
		},
	},
	{
		Column:  "%sys",
		Correct: "Percent of CPU time spent running kernel code",
		Distractors: []string{
			"Percent of CPU time spent running user-space code",
			"Percent of CPU time spent idle",
			"Percent of CPU time spent handling hardware interrupts",
		},
	},
	{
		Column:  "%iowait",
		Correct: "Percent of time the CPU was idle while there was an outstanding disk I/O request",
		Distractors: []string{
			"Percent of CPU time spent processing I/O interrupts",
			"Percent of time disks were saturated",
			"Wait time in milliseconds before each I/O completes",
		},
	},
	{
		Column:  "%irq",
		Correct: "Percent of CPU time spent servicing hardware interrupts",
		Distractors: []string{
			"Percent of CPU time spent servicing software interrupts",
			"Percent of CPU time spent in user-space code",
			"Percent of CPU time spent idle while waiting for I/O",
		},
	},
	{
		Column:  "%soft",
		Correct: "Percent of CPU time spent servicing software interrupts",
		Distractors: []string{
			"Percent of CPU time spent servicing hardware interrupts",
			"Percent of CPU time spent running niced processes",
			"Percent of CPU time stolen by the hypervisor",
		},
	},
	{
		Column:  "%steal",
		Correct: "Percent of time stolen by the hypervisor for other virtual machines",
		Distractors: []string{
			"Percent of CPU time spent in software interrupts",
			"Percent of CPU time spent waiting for local disk I/O",
			"Percent of CPU time reserved for kernel threads",
		},
	},
	{
		Column:  "%guest",
		Correct: "Percent of CPU time spent running a guest virtual CPU",
		Distractors: []string{
			"Percent of CPU time stolen by the hypervisor",
			"Percent of CPU time spent running niced host processes",
			"Percent of CPU time spent idle",
		},
	},
	{
		Column:  "%gnice",
		Correct: "Percent of CPU time spent running a niced guest virtual CPU",
		Distractors: []string{
			"Percent of CPU time spent running normal niced host processes",
			"Percent of CPU time stolen by the hypervisor",
			"Percent of CPU time spent servicing software interrupts",
		},
	},
	{
		Column:  "%idle",
		Correct: "Percent of CPU time spent idle with no outstanding disk I/O wait",
		Distractors: []string{
			"Percent of CPU time spent idle while waiting for disk I/O",
			"Percent of CPU time available after subtracting user-space time only",
			"Percent of disk capacity currently unused",
		},
	},
}

func availableMpstatQuestionPicks(output string) []mpstatQuestionPick {
	var picks []mpstatQuestionPick
	for _, pick := range mpstatQuestionPicks {
		if outputHasColumn(output, pick.Column) {
			picks = append(picks, pick)
		}
	}
	return picks
}

func outputHasColumn(output, column string) bool {
	for _, line := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(line) {
			if field == column {
				return true
			}
		}
	}
	return false
}

func dmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	if strings.Contains(low, "machine check") || strings.Contains(low, "mce:") {
		return []Question{{
			Stem:    "Your `dmesg` output mentions a machine-check (MCE) event. What does this typically indicate?",
			Correct: "A hardware-level CPU or memory error reported by the processor",
			Distractors: []string{
				"A scheduling decision made by the kernel",
				"A user-space program crashing",
				"A successful firmware update",
			},
		}}
	}
	if strings.Contains(low, "thermal") || strings.Contains(low, "throttl") {
		return []Question{{
			Stem:    "Your `dmesg` output mentions thermal throttling. What is the immediate effect on the CPU?",
			Correct: "The CPU clocks down to reduce heat, lowering effective performance",
			Distractors: []string{
				"The CPU is taken offline until it cools down",
				"Workloads are migrated to other physical sockets",
				"The kernel kills the highest-CPU processes",
			},
		}}
	}
	return nil
}

// ----- Observations (cross-command, feed snapshot + recall + synthesis) -----

var cpuObservations = []Observation{
	{
		Name:    "loadavg_1min",
		Title:   "1-min load average",
		Section: "Utilization",
		Extract: extractLoadavgN(0),
		Recall:  loadavgRecall("1-minute"),
	},
	{
		Name:    "loadavg_5min",
		Title:   "5-min load average",
		Section: "Utilization",
		Extract: extractLoadavgN(1),
	},
	{
		Name:    "loadavg_15min",
		Title:   "15-min load average",
		Section: "Utilization",
		Extract: extractLoadavgN(2),
	},
	{
		Name:    "mpstat_idle_mean",
		Title:   "Mean %idle (mpstat)",
		Section: "Utilization",
		Extract: extractMpstatIdleMean,
	},
	{
		Name:    "mpstat_idle_range",
		Title:   "Per-CPU %idle range",
		Section: "Utilization",
		Extract: extractMpstatIdleRange,
		Recall:  mpstatIdleRangeRecall,
	},
	{
		Name:    "vmstat_r",
		Title:   "vmstat r (run-queue)",
		Section: "Saturation",
		Extract: extractVmstatColumn("r"),
		Recall:  vmstatRRecall,
	},
	{
		Name:    "vmstat_wa",
		Title:   "vmstat wa (cpu I/O wait)",
		Section: "Saturation",
		Extract: extractVmstatColumn("wa"),
	},
	{
		Name:    "vmstat_st",
		Title:   "vmstat st (hypervisor steal)",
		Section: "Saturation",
		Extract: extractVmstatColumn("st"),
		Recall:  vmstatStealRecall,
	},
	{
		Name:    "dmesg_cpu_keywords",
		Title:   "dmesg CPU/thermal/MCE",
		Section: "Errors",
		Extract: extractDmesgCpuKeywords,
	},
}

func extractLoadavgN(pos int) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		for _, c := range caps {
			m := loadAvgRe.FindStringSubmatch(c.Output)
			if m == nil {
				continue
			}
			n, err := strconv.ParseFloat(m[pos+1], 64)
			if err != nil {
				continue
			}
			v := Value{Number: n}
			if pos == 0 {
				v.Note = fmt.Sprintf("NumCPU=%d, ratio %.2f", si.NumCPU, n/float64(si.NumCPU))
			}
			return v, true
		}
		return Value{}, false
	}
}

func extractMpstatIdleSamples(caps []CapturedCommand) []float64 {
	var samples []float64
	for _, c := range caps {
		if !strings.Contains(c.Output, "%idle") {
			continue
		}
		for _, line := range strings.Split(c.Output, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Average") {
				continue
			}
			if strings.Contains(line, " all ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 12 {
				continue
			}
			if _, err := strconv.Atoi(fields[1]); err != nil {
				continue
			}
			idle, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err != nil {
				continue
			}
			samples = append(samples, idle)
		}
	}
	return samples
}

func extractMpstatIdleMean(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	samples := extractMpstatIdleSamples(caps)
	if len(samples) == 0 {
		return Value{}, false
	}
	sum := 0.0
	for _, x := range samples {
		sum += x
	}
	mean := sum / float64(len(samples))
	return Value{
		Number: mean,
		Unit:   "%",
		Note:   fmt.Sprintf("%d per-CPU samples", len(samples)),
	}, true
}

func extractMpstatIdleRange(si SystemInfo, caps []CapturedCommand) (Value, bool) {
	samples := extractMpstatIdleSamples(caps)
	if len(samples) == 0 {
		return Value{}, false
	}
	min, max := samples[0], samples[0]
	for _, x := range samples {
		if x < min {
			min = x
		}
		if x > max {
			max = x
		}
	}
	return Value{
		Text:    fmt.Sprintf("%.1f – %.1f%%", min, max),
		Note:    fmt.Sprintf("spread %.1f", max-min),
		Samples: samples,
	}, true
}

var vmstatColumns = []string{"r", "b", "swpd", "free", "buff", "cache", "si", "so", "bi", "bo", "in", "cs", "us", "sy", "id", "wa", "st"}

func extractVmstatColumn(col string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	colIdx := -1
	for i, c := range vmstatColumns {
		if c == col {
			colIdx = i
			break
		}
	}
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		if colIdx == -1 {
			return Value{}, false
		}
		var samples []float64
		for _, c := range caps {
			lines := strings.Split(c.Output, "\n")
			seen := false
			for _, line := range lines {
				if !seen {
					if strings.Contains(line, " r ") && strings.Contains(line, " b ") && strings.Contains(line, "swpd") {
						seen = true
					}
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < len(vmstatColumns) {
					continue
				}
				n, err := strconv.ParseFloat(fields[colIdx], 64)
				if err != nil {
					continue
				}
				samples = append(samples, n)
			}
		}
		if len(samples) == 0 {
			return Value{}, false
		}
		v := Value{Samples: samples}
		if col == "r" {
			v.Note = fmt.Sprintf("NumCPU=%d", si.NumCPU)
		}
		if col == "wa" || col == "st" || col == "us" || col == "sy" || col == "id" {
			v.Unit = "%"
		}
		return v, true
	}
}

func extractDmesgCpuKeywords(si SystemInfo, caps []CapturedCommand) (Value, bool) {
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
			for _, kw := range []string{"mce", "machine check", "thermal", "throttl"} {
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
		Text: fmt.Sprintf("%d/%d lines mention CPU/thermal/MCE keywords", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

func loadavgRecall(label string) func(Value) []Question {
	return func(v Value) []Question {
		correct := fmt.Sprintf("%.2f", v.Number)
		pool := []string{
			fmt.Sprintf("%.2f", v.Number+0.5),
			fmt.Sprintf("%.2f", v.Number+1.5),
			fmt.Sprintf("%.2f", v.Number*2+1),
			fmt.Sprintf("%.2f", v.Number*0.5+0.25),
			"0.00", "1.00", "2.50", "5.00",
		}
		return makeRecallQuestion(
			fmt.Sprintf("What %s load average did you observe?", label),
			correct, pool)
	}
}

func mpstatIdleRangeRecall(v Value) []Question {
	if len(v.Samples) == 0 {
		return nil
	}
	max := v.Samples[0]
	for _, x := range v.Samples {
		if x > max {
			max = x
		}
	}
	correct := fmt.Sprintf("%.1f%%", max)
	pool := []string{
		fmt.Sprintf("%.1f%%", clamp(max-15, 0, 100)),
		fmt.Sprintf("%.1f%%", clamp(max-30, 0, 100)),
		fmt.Sprintf("%.1f%%", clamp(max-50, 0, 100)),
		"0.0%", "25.0%", "50.0%", "75.0%", "99.0%",
	}
	return makeRecallQuestion(
		"Across all per-CPU samples in your `mpstat` output, what was the highest %idle value?",
		correct, pool)
}

func vmstatRRecall(v Value) []Question {
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
		fmt.Sprintf("%.0f", max+2),
		fmt.Sprintf("%.0f", max+5),
		fmt.Sprintf("%.0f", max*3+1),
		"0", "1", "5", "100",
	}
	return makeRecallQuestion(
		"What was the highest run-queue length (`r`) you observed in your `vmstat` samples?",
		correct, pool)
}

func vmstatStealRecall(v Value) []Question {
	if len(v.Samples) == 0 {
		return nil
	}
	max := v.Samples[0]
	for _, x := range v.Samples {
		if x > max {
			max = x
		}
	}
	correct := fmt.Sprintf("%.0f%%", max)
	pool := []string{
		fmt.Sprintf("%.0f%%", clamp(max+15, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(max+5, 0, 100)),
		fmt.Sprintf("%.0f%%", clamp(max+30, 0, 100)),
		"0%", "5%", "25%", "50%", "100%",
	}
	return makeRecallQuestion(
		"What was the highest `st` (steal-time) sample you observed in vmstat?",
		correct, pool)
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// ----- Synthesis rules -----

var cpuSynthesisRules = []SynthesisRule{
	loadavgIdleConsistency,
	stealVsIowaitDistinction,
}

var loadavgIdleConsistency = SynthesisRule{
	Requires: []string{"loadavg_1min", "mpstat_idle_mean"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		loadavg := vs["loadavg_1min"].Number
		idle := vs["mpstat_idle_mean"].Number
		ratio := loadavg / float64(si.NumCPU)

		var correct string
		switch {
		case ratio < 0.5 && idle > 60:
			correct = "Consistent — a small loadavg-to-NumCPU ratio matches a high mean %idle; the system is lightly loaded."
		case ratio > 1.0 && idle < 25:
			correct = "Consistent — a loadavg ratio above 1 matches low %idle; the system is saturated."
		case ratio < 0.3 && idle < 30:
			correct = "Inconsistent — low load average should not coexist with low %idle; one of the readings may be stale (loadavg lags by minutes)."
		case ratio > 1.0 && idle > 60:
			correct = "Inconsistent — high load average alongside high %idle is unusual; one reading may be stale."
		default:
			return Question{}, false
		}

		pool := []string{
			"Consistent — a small loadavg-to-NumCPU ratio matches a high mean %idle; the system is lightly loaded.",
			"Consistent — a loadavg ratio above 1 matches low %idle; the system is saturated.",
			"Inconsistent — low load average should not coexist with low %idle; one of the readings may be stale (loadavg lags by minutes).",
			"Inconsistent — high load average alongside high %idle is unusual; one reading may be stale.",
			"Cannot be compared — these metrics measure entirely unrelated things.",
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
				"Your 1-min load average was %.2f (NumCPU=%d, ratio %.2f).\n"+
					"Mean %%idle observed was %.1f%%.\n"+
					"Which best describes the relationship between these two numbers?",
				loadavg, si.NumCPU, ratio, idle),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// stealVsIowaitDistinction teaches the cloud-VM diagnostic point: high `wa`
// is often misread as "disk problem" but on a virtualised host it can also
// reflect time stolen by the hypervisor. Comparing `st` and `wa` directly
// disambiguates the two.
var stealVsIowaitDistinction = SynthesisRule{
	Requires: []string{"vmstat_wa", "vmstat_st"},
	Generate: func(si SystemInfo, vs map[string]Value) (Question, bool) {
		waMax := 0.0
		for _, x := range vs["vmstat_wa"].Samples {
			if x > waMax {
				waMax = x
			}
		}
		stMax := 0.0
		for _, x := range vs["vmstat_st"].Samples {
			if x > stMax {
				stMax = x
			}
		}

		var correct string
		switch {
		case stMax >= 5 && waMax < 5:
			correct = "Steal exceeds I/O wait — the bottleneck is hypervisor contention (a noisy neighbour on the same host), not local disk. This isn't fixable from inside the VM."
		case stMax < 1 && waMax >= 10:
			correct = "Steal is negligible; the cpu-stall signal is genuine I/O wait. Investigate disk performance."
		case stMax >= 5 && waMax >= 10:
			correct = "Both are elevated — hypervisor contention and I/O wait coexist. The disk may itself be a contended resource at the hypervisor layer."
		case stMax < 1 && waMax < 5:
			correct = "Both are low — neither hypervisor contention nor I/O wait is significant. Look elsewhere for the bottleneck."
		default:
			return Question{}, false
		}

		pool := []string{
			"Steal exceeds I/O wait — the bottleneck is hypervisor contention (a noisy neighbour on the same host), not local disk. This isn't fixable from inside the VM.",
			"Steal is negligible; the cpu-stall signal is genuine I/O wait. Investigate disk performance.",
			"Both are elevated — hypervisor contention and I/O wait coexist. The disk may itself be a contended resource at the hypervisor layer.",
			"Both are low — neither hypervisor contention nor I/O wait is significant. Look elsewhere for the bottleneck.",
			"Cannot be compared — `wa` and `st` measure entirely unrelated subsystems.",
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
				"Highest `wa` (cpu I/O wait) sample was %.1f%%; highest `st` (hypervisor steal) sample was %.1f%%.\n"+
					"Which best describes what these two numbers tell you?",
				waMax, stMax),
			Correct:     correct,
			Distractors: distractors,
		}, true
	},
}

// ----- Command reference (semi-verbose help) -----

var cpuCommands = []CommandRef{
	{
		Cmd:     "uptime",
		Section: "Utilization",
		Summary: "1, 5, and 15-minute load averages, plus uptime.\nFastest first look at run-queue pressure.",
	},
	{
		Cmd:     "w",
		Section: "Utilization",
		Summary: "Load averages plus per-user session info.\nSimilar info to uptime, with logged-in users.",
	},
	{
		Cmd:      "mpstat -P ALL 1 N",
		Section:  "Utilization",
		Summary:  "Per-CPU breakdown (%usr, %sys, %iowait, %idle, ...)\nover N one-second intervals. Distinguishes\nall-cores-busy from one-core-pegged. (sysstat package.)",
		Requires: []string{"mpstat"},
	},
	{
		Cmd:     "top -bn1",
		Section: "Utilization",
		Summary: "One-shot batch snapshot of processes sorted by CPU.\nBatch mode (-b -n1) avoids needing a TTY.",
	},
	{
		Cmd:      "pidstat 1 N",
		Section:  "Utilization",
		Summary:  "Per-process CPU usage over N intervals.\nFinds which process is consuming time. (sysstat package.)",
		Requires: []string{"pidstat"},
	},
	{
		Cmd:     "ps -eo pid,pcpu,comm --sort=-pcpu | head",
		Section: "Utilization",
		Summary: "Top CPU-consuming processes right now.\nNo external dependencies; works everywhere.",
	},
	{
		Cmd:     "vmstat 1 N",
		Section: "Saturation",
		Summary: "System-wide stats per second: run-queue (r),\nblocked (b), swap (si/so), I/O (bi/bo),\nand CPU breakdown (us/sy/id/wa/st).",
	},
	{
		Cmd:     "cat /proc/loadavg",
		Section: "Saturation",
		Summary: "Same load averages as uptime, plus current\nrunnable/total task counts and last PID.",
	},
	{
		Cmd:      "cat /proc/pressure/cpu",
		Section:  "Saturation",
		Summary:  "PSI: time-share of tasks stalled on CPU.\nLinux 4.20+ with PSI enabled (kernel.org/PSI).",
		Requires: []string{"psi"},
	},
	{
		Cmd:      "sar -u 1 N",
		Section:  "Saturation",
		Summary:  "Historical CPU utilization, N samples.\nCan also read archived data with -f. (sysstat package.)",
		Requires: []string{"sar"},
	},
	{
		Cmd:     "dmesg --level=err,warn | tail -30",
		Section: "Errors",
		Summary: "Recent kernel errors and warnings.\nLook for MCE, thermal throttling, hardware faults.\n" + dmesgPermissionNote,
	},
	{
		Cmd:     "journalctl -k -p err -n 30",
		Section: "Errors",
		Summary: "Recent kernel-level errors via journald.\nAlternative to dmesg on systemd systems.",
	},
	{
		Cmd:     "grep . /sys/devices/system/cpu/*/thermal_throttle/* 2>/dev/null",
		Section: "Errors",
		Summary: "Per-CPU thermal-throttle event counters.\nNon-zero values mean thermal events have occurred.",
	},
}
