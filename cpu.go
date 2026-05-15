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
	loadavgVariants := cpuLoadavgVariants()
	loadavgPick := pickStepVariant(si, loadavgVariants)

	steps := []GuideStep{
		{
			Name:        "loadavg",
			Intro:       "Step 1: Load averages and recent run-queue counters give a coarse\npicture of CPU pressure over 1-, 5-, and 15-minute windows.",
			Suggested:   loadavgPick.Cmd,
			QuestionsFn: combineVariantQuestions(loadavgVariants),
			Teaching: fmt.Sprintf(
				"Rule of thumb: a 1-minute load average above %d (= number of logical CPUs on this machine)\n"+
					"indicates more runnable processes than CPUs — the run-queue is saturated.\n"+
					"The 5- and 15-minute values show whether that's a transient spike or sustained.",
				si.NumCPU),
		},
	}

	if si.HasMpstat {
		steps = append(steps, GuideStep{
			Name:          "per-cpu",
			Intro:         "Step 2: mpstat breaks down utilization per logical CPU.\nThis distinguishes \"all cores busy\" from \"one core pegged.\"",
			Suggested:     "mpstat -P ALL 1 3",
			QuestionsFn:   mpstatColumnQuestions,
			QuestionCount: 3,
			Teaching: "Look at %idle per CPU. Uniformly low means saturation; one CPU near 0%\n" +
				"with others near 100% means a single-thread bottleneck.",
		})
	}

	runqueueVariants := cpuRunqueueVariants(si)
	runqueuePick := pickStepVariant(si, runqueueVariants)

	steps = append(steps, GuideStep{
		Name: "runqueue",
		Intro: "Step 3: look at saturation signals — run-queue length, CPU stall pressure,\n" +
			"or the idle-vs-wait breakdown — over a few short samples.",
		Suggested:     runqueuePick.Cmd,
		QuestionsFn:   combineVariantQuestions(runqueueVariants),
		QuestionCount: 3,
		Teaching:      runqueuePick.Teaching,
	})

	steps = append(steps, GuideStep{
		Name:               "errors",
		Intro:              "Step 4: kernel errors (the 'E' in USE) surface in dmesg —\nMCE events, thermal throttling, hardware faults.\nThis uses dmesg's severity filter instead of a keyword grep so unusual CPU and hardware warnings are not hidden.\n" + dmesgPermissionNote,
		Suggested:          "dmesg --level=err,warn | tail -20",
		Alternatives:       journalctlAlternative(si, "journalctl -k -b -p warning --no-pager -n 30"),
		QuestionsFn:        dmesgQuestions,
		AcceptAny:          true,
		EmptyOutputMessage: "No CPU, thermal, or machine-check errors found.",
		Teaching: "Recent MCE (machine-check exception) or thermal throttling messages indicate\n" +
			"physical CPU problems; absence is the healthy case. On idle laptops you'll\n" +
			"typically see nothing here — that's fine.",
	})

	return steps
}

// cpuLoadavgVariants is the pool of commands the loadavg step can suggest.
// wQuestions is listed before uptimeQuestions because `w` output contains
// a `load average:` line that uptimeQuestions would also match — without
// the ordering the learner who ran `w` would get a generic loadavg
// question instead of one about the session table.
func cpuLoadavgVariants() []stepVariant {
	return []stepVariant{
		{Cmd: "w", QuestionsFn: wQuestions},
		{Cmd: "cat /proc/loadavg", QuestionsFn: procLoadavgQuestions},
		{Cmd: "uptime", QuestionsFn: uptimeQuestions},
	}
}

// cpuRunqueueVariants is the pool for the saturation step. vmstat is the
// classic; PSI and sar -u look at the same dimension from different angles
// when they're available. Each variant carries its own Teaching because
// the columns and concepts being explained are different.
func cpuRunqueueVariants(si SystemInfo) []stepVariant {
	return []stepVariant{
		{
			Cmd:         "vmstat 1 3",
			QuestionsFn: vmstatColumnQuestions,
			Teaching: fmt.Sprintf(
				"If `r` consistently exceeds %d (= NumCPU on this system), the run-queue is\n"+
					"saturated. High `wa` means CPUs are idle waiting on I/O — the bottleneck is\n"+
					"storage, not CPU. Non-zero `st` means the hypervisor is giving cycles to\n"+
					"another tenant; on cloud VMs, sustained `st` is contention you can't fix\n"+
					"from inside the guest.",
				si.NumCPU),
		},
		{
			Cmd:         "cat /proc/pressure/cpu",
			QuestionsFn: procPressureCpuQuestions,
			Teaching: "PSI's `some avg10/avg60/avg300` values are the share of time at least one\n" +
				"task was stalled waiting for CPU over those windows. Sustained values above\n" +
				"~10% indicate run-queue contention even when loadavg looks moderate. There's\n" +
				"no `full` row for CPU because a fully-stalled run-queue would mean no task is\n" +
				"running — indistinguishable from idle.",
			Available: func(si SystemInfo) bool { return si.HasPSI },
		},
		{
			Cmd:         "sar -u 1 3",
			QuestionsFn: sarUColumnQuestions,
			Teaching: "`sar -u` reports the same CPU breakdown as vmstat's CPU columns\n" +
				"(%user/%system/%iowait/%steal/%idle) but is part of sysstat, which also\n" +
				"archives historical samples. That makes `sar` the right tool for\n" +
				"after-the-fact diagnosis — `sar -f` reads the archived data file for a\n" +
				"previous day or hour.",
			Available: func(si SystemInfo) bool { return si.HasSar },
		},
	}
}

// ----- Comprehension extractors (per-command) -----

var cpuExtractors = []Extractor{
	{BaseCmd: "uptime", QuestionsFn: uptimeQuestions},
	{BaseCmd: "w", QuestionsFn: wQuestions},
	{BaseCmd: "cat", QuestionsFn: catCpuProcQuestions},
	{BaseCmd: "vmstat", QuestionsFn: vmstatQuestions},
	{BaseCmd: "mpstat", QuestionsFn: mpstatQuestions},
	{BaseCmd: "sar", QuestionsFn: sarUQuestions},
	{BaseCmd: "dmesg", QuestionsFn: dmesgQuestions},
	{BaseCmd: "journalctl", QuestionsFn: dmesgQuestions},
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
	return randomColumnQuestions(
		availableColumnQuestionPicks(c.Output, vmstatCPUQuestionPicks),
		1,
		func(column string) string {
			return fmt.Sprintf("In the `vmstat` output, what does the `%s` column represent?", column)
		},
	)
}

func vmstatColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !vmstatHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, vmstatQuestionPicks),
		func(column string) string {
			return fmt.Sprintf("In the `vmstat` output, what does the `%s` column represent?", column)
		},
	)
}

type columnQuestionPick struct {
	Column      string
	Correct     string
	Distractors []string
}

func columnQuestionsFromPicks(picks []columnQuestionPick, stemFor func(string) string) []Question {
	if len(picks) == 0 {
		return nil
	}
	qs := make([]Question, 0, len(picks))
	for _, pick := range picks {
		qs = append(qs, Question{
			Stem:        stemFor(pick.Column),
			Correct:     pick.Correct,
			Distractors: pick.Distractors,
		})
	}
	return qs
}

func randomColumnQuestions(picks []columnQuestionPick, count int, stemFor func(string) string) []Question {
	if len(picks) == 0 {
		return nil
	}
	if count <= 0 || count > len(picks) {
		count = len(picks)
	}
	shuffled := append([]columnQuestionPick(nil), picks...)
	appRand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	return columnQuestionsFromPicks(shuffled[:count], stemFor)
}

func availableColumnQuestionPicks(output string, picks []columnQuestionPick) []columnQuestionPick {
	var out []columnQuestionPick
	for _, pick := range picks {
		if outputHasColumn(output, pick.Column) {
			out = append(out, pick)
		}
	}
	return out
}

func filterColumnQuestionPicks(picks []columnQuestionPick, columns ...string) []columnQuestionPick {
	wanted := make(map[string]bool, len(columns))
	for _, column := range columns {
		wanted[column] = true
	}
	var out []columnQuestionPick
	for _, pick := range picks {
		if wanted[pick.Column] {
			out = append(out, pick)
		}
	}
	return out
}

var vmstatQuestionPicks = []columnQuestionPick{
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
		Column:  "swpd",
		Correct: "Amount of virtual memory currently swapped out, in kilobytes",
		Distractors: []string{
			"Amount of physical memory currently free",
			"Pages swapped in from disk per second",
			"Percent of CPU time spent waiting on swap I/O",
		},
	},
	{
		Column:  "free",
		Correct: "Amount of idle memory, in kilobytes",
		Distractors: []string{
			"Estimated memory available without swapping",
			"Amount of memory used for filesystem cache",
			"Amount of swap space currently unused",
		},
	},
	{
		Column:  "buff",
		Correct: "Amount of memory used as buffers, in kilobytes",
		Distractors: []string{
			"Amount of memory used as page cache",
			"Number of block-device writes per second",
			"Percent of CPU time spent buffering I/O",
		},
	},
	{
		Column:  "cache",
		Correct: "Amount of memory used as page cache, in kilobytes",
		Distractors: []string{
			"Amount of memory used as buffers",
			"Amount of virtual memory swapped out",
			"Number of cache misses per second",
		},
	},
	{
		Column:  "si",
		Correct: "Pages swapped in from swap to memory per second",
		Distractors: []string{
			"Pages swapped out from memory to swap per second",
			"Software interrupts per second",
			"Memory used by the slab allocator",
		},
	},
	{
		Column:  "so",
		Correct: "Pages swapped out from memory to swap per second",
		Distractors: []string{
			"Pages swapped in from swap to memory per second",
			"System calls per second",
			"Swap space currently available",
		},
	},
	{
		Column:  "bi",
		Correct: "Blocks received from block devices per second",
		Distractors: []string{
			"Blocks sent to block devices per second",
			"Processes blocked on I/O",
			"Bytes read from network interfaces per second",
		},
	},
	{
		Column:  "bo",
		Correct: "Blocks sent to block devices per second",
		Distractors: []string{
			"Blocks received from block devices per second",
			"Bytes written to network interfaces per second",
			"Processes blocked on output",
		},
	},
	{
		Column:  "in",
		Correct: "Interrupts per second, including the clock interrupt",
		Distractors: []string{
			"Input blocks read per second",
			"Context switches per second",
			"Inbound network packets per second",
		},
	},
	{
		Column:  "cs",
		Correct: "Context switches per second",
		Distractors: []string{
			"CPU system time percentage",
			"Cache size in kilobytes",
			"Completed system calls per second",
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

var vmstatCPUQuestionPicks = filterColumnQuestionPicks(vmstatQuestionPicks, "r", "b", "us", "sy", "id", "wa", "st")

var mpstatHeaderRe = regexp.MustCompile(`%idle`)

func mpstatQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !mpstatHeaderRe.MatchString(c.Output) {
		return nil
	}
	return randomColumnQuestions(
		availableMpstatQuestionPicks(c.Output),
		1,
		func(column string) string {
			return fmt.Sprintf("In the `mpstat` output, what does the `%s` column represent?", column)
		},
	)
}

func mpstatColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !mpstatHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableMpstatQuestionPicks(c.Output),
		func(column string) string {
			return fmt.Sprintf("In the `mpstat` output, what does the `%s` column represent?", column)
		},
	)
}

var mpstatQuestionPicks = []columnQuestionPick{
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

func availableMpstatQuestionPicks(output string) []columnQuestionPick {
	return availableColumnQuestionPicks(output, mpstatQuestionPicks)
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
	tool := kernelLogQuestionTool(c.Cmd)
	if strings.Contains(low, "machine check") || strings.Contains(low, "mce:") {
		return []Question{{
			Stem:    fmt.Sprintf("Your `%s` output mentions a machine-check (MCE) event. What does this typically indicate?", tool),
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
			Stem:    fmt.Sprintf("Your `%s` output mentions thermal throttling. What is the immediate effect on the CPU?", tool),
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

func kernelLogQuestionTool(cmd string) string {
	if baseCmd(cmd) == "journalctl" {
		return "journalctl"
	}
	return "dmesg"
}

// wHeaderRe matches `w`'s session-table header. We require both TTY and
// JCPU so that arbitrary text containing the word "USER" doesn't qualify.
var wHeaderRe = regexp.MustCompile(`(?m)^\s*USER\b.*\bTTY\b.*\bJCPU\b.*\bPCPU\b`)

// wQuestions handles `w` output. `w` includes the same `load average:`
// line as `uptime` plus a session table with TTY, IDLE, JCPU, and PCPU
// columns; the pool covers both the loadavg-position question (shared with
// uptime) and two w-specific column questions, and the guide picks one at
// random.
func wQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !wHeaderRe.MatchString(c.Output) {
		return nil
	}
	qs := []Question{
		{
			Stem:    "In `w`'s session table, what does the `JCPU` column measure?",
			Correct: "Cumulative CPU time of all processes attached to that TTY since the user logged in",
			Distractors: []string{
				"CPU time used by the process shown in the `WHAT` column",
				"Just-in-time CPU usage averaged over the last minute",
				"The number of CPU cores currently bound to that session",
			},
		},
		{
			Stem:    "In `w`'s session table, what does the `PCPU` column measure?",
			Correct: "CPU time used by the process named in the `WHAT` column",
			Distractors: []string{
				"Aggregate CPU usage across all the user's TTYs",
				"Percentage of CPU capacity allocated to that user's process group",
				"The pinned CPU index for that process",
			},
		},
	}
	qs = append(qs, uptimeQuestions(si, c)...)
	return qs
}

// procLoadavgLineRe matches a single line in /proc/loadavg format:
//
//	0.42 0.31 0.28 1/234 5678
//
// Three load averages, then `running/total` scheduling entities, then the
// PID of the most recently created task.
var procLoadavgLineRe = regexp.MustCompile(`(?m)^\s*([0-9]+\.[0-9]+)\s+([0-9]+\.[0-9]+)\s+([0-9]+\.[0-9]+)\s+(\d+)/(\d+)\s+(\d+)\s*$`)

// procLoadavgQuestions handles `cat /proc/loadavg`. Deliberately skips a
// loadavg-position question (covered by uptimeQuestions) and asks about
// the two fields the kernel emits *only* via /proc/loadavg.
func procLoadavgQuestions(si SystemInfo, c CapturedCommand) []Question {
	m := procLoadavgLineRe.FindStringSubmatch(c.Output)
	if m == nil {
		return nil
	}
	running, total, lastPid := m[4], m[5], m[6]
	return []Question{
		{
			Stem: fmt.Sprintf(
				"Your `/proc/loadavg` line included the field `%s/%s`.\n"+
					"What do those two numbers represent?",
				running, total),
			Correct: "Currently runnable kernel scheduling entities, over the total number of scheduling entities (threads)",
			Distractors: []string{
				"The 1-minute load average expressed as a fraction of NumCPU",
				"Running processes, over the configured kernel.pid_max",
				"Open file descriptors, over the soft RLIMIT_NOFILE",
			},
		},
		{
			Stem: fmt.Sprintf(
				"Your `/proc/loadavg` line ended with the field `%s`.\n"+
					"What is this last numeric field?",
				lastPid),
			Correct: "The PID of the most recently created process or thread on the system",
			Distractors: []string{
				"Total context switches since boot",
				"The number of CPU samples that contributed to the load averages",
				"Total tasks blocked waiting on uninterruptible I/O",
			},
		},
	}
}

// psiSomeRe matches a PSI `some` row in /proc/pressure/cpu, e.g.
//
//	some avg10=0.12 avg60=0.05 avg300=0.01 total=12345678
var psiSomeRe = regexp.MustCompile(`some\s+avg10=([0-9.]+)\s+avg60=([0-9.]+)\s+avg300=([0-9.]+)\s+total=(\d+)`)

// procPressureCpuQuestions handles `cat /proc/pressure/cpu` (PSI). All
// three questions are returned so the guide step (QuestionCount=3) can
// ask one of each per session.
func procPressureCpuQuestions(si SystemInfo, c CapturedCommand) []Question {
	m := psiSomeRe.FindStringSubmatch(c.Output)
	if m == nil {
		return nil
	}
	avg10, avg60, avg300, total := m[1], m[2], m[3], m[4]
	return []Question{
		{
			Stem: fmt.Sprintf(
				"Your PSI output included `some avg10=%s avg60=%s avg300=%s ...`.\n"+
					"What does the `avg10` value mean?",
				avg10, avg60, avg300),
			Correct: "Percent of time, over the last 10 seconds, that at least one task was stalled waiting for CPU",
			Distractors: []string{
				"The 10-second moving average of load (runnable tasks)",
				"Average context switches per second over a 10-second window",
				"Number of CPUs idle on average over the last 10 seconds",
			},
		},
		{
			Stem:    "Why does `/proc/pressure/cpu` show only a `some` row, with no `full` row, unlike memory and I/O PSI?",
			Correct: "A CPU stall means at least one task is waiting; if every task were stalled there would be nothing running, which is indistinguishable from idle — `full` isn't meaningful for CPU",
			Distractors: []string{
				"The `full` row is hidden unless the system has been under load for 60 seconds or more",
				"Kernel limitation — the `full` row is planned for a future release",
				"It's only shown to processes with CAP_SYS_ADMIN, so it's hidden for normal users",
			},
		},
		{
			Stem: fmt.Sprintf(
				"Your PSI output ended with `total=%s`. What does that counter represent?",
				total),
			Correct: "Cumulative microseconds since boot during which at least one task was stalled on CPU",
			Distractors: []string{
				"Number of distinct tasks that have ever stalled on CPU since boot",
				"Number of CPU-steal events recorded by the hypervisor",
				"Cumulative milliseconds the run-queue has been non-empty",
			},
		},
	}
}

// sarUHeaderRe is the column-header line printed by `sar -u`.
var sarUHeaderRe = regexp.MustCompile(`%user\s+%nice\s+%system\s+%iowait\s+%steal\s+%idle`)

// sarUQuestions returns one random column question. Used by the extractor
// (free-form practice mode) where the user may run sar -u alongside other
// commands and only needs one comprehension check.
func sarUQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarUHeaderRe.MatchString(c.Output) {
		return nil
	}
	return randomColumnQuestions(
		availableColumnQuestionPicks(c.Output, sarUQuestionPicks),
		1,
		sarUStemFor,
	)
}

// sarUColumnQuestions returns the full pool of column questions. Used by
// the guide step (QuestionCount=3) so the shuffler picks several different
// columns per session.
func sarUColumnQuestions(si SystemInfo, c CapturedCommand) []Question {
	if !sarUHeaderRe.MatchString(c.Output) {
		return nil
	}
	return columnQuestionsFromPicks(
		availableColumnQuestionPicks(c.Output, sarUQuestionPicks),
		sarUStemFor,
	)
}

func sarUStemFor(column string) string {
	return fmt.Sprintf("In the `sar -u` output, what does the `%s` column represent?", column)
}

var sarUQuestionPicks = []columnQuestionPick{
	{
		Column:  "%user",
		Correct: "Percent of CPU time spent running user-space code, excluding niced processes",
		Distractors: []string{
			"Percent of CPU time spent in kernel code on behalf of user processes",
			"Percent of CPU time available to non-root users",
			"Percent of total CPUs occupied by user processes",
		},
	},
	{
		Column:  "%nice",
		Correct: "Percent of CPU time spent running user-space processes with a positive nice value",
		Distractors: []string{
			"Percent of CPU time spent on cgroup-throttled processes",
			"Percent of CPU time spent on systemd-managed services only",
			"Percent of CPU time spent in interruptible sleep",
		},
	},
	{
		Column:  "%system",
		Correct: "Percent of CPU time spent running kernel code",
		Distractors: []string{
			"Percent of CPU time spent on systemd-managed services only",
			"Percent of CPU time spent running user-space code",
			"Percent of CPU time spent on the system bus driver",
		},
	},
	{
		Column:  "%iowait",
		Correct: "Percent of time the CPU was idle while there was an outstanding disk I/O request",
		Distractors: []string{
			"Percent of CPU time spent actively handling I/O interrupts",
			"Wait time in seconds before each I/O operation completes",
			"Percent of disk capacity currently in use",
		},
	},
	{
		Column:  "%steal",
		Correct: "Percent of CPU time stolen by the hypervisor for other virtual machines",
		Distractors: []string{
			"Percent of CPU time stolen by higher-priority local processes",
			"Percent of CPU time spent servicing software interrupts",
			"Percent of CPU time the guest could not access due to a NUMA penalty",
		},
	},
	{
		Column:  "%idle",
		Correct: "Percent of CPU time spent idle with no outstanding disk I/O wait",
		Distractors: []string{
			"Percent of CPU cores currently powered down",
			"Percent of CPU time spent idle while waiting for disk I/O",
			"Percent of total CPU capacity that's currently free",
		},
	},
}

// catCpuProcQuestions dispatches `cat` extractor matches to whichever
// /proc file's question handler recognises the captured output. Returns
// nil if neither matches, which is the right answer for an unrelated
// `cat` invocation.
func catCpuProcQuestions(si SystemInfo, c CapturedCommand) []Question {
	if qs := procLoadavgQuestions(si, c); len(qs) > 0 {
		return qs
	}
	if qs := procPressureCpuQuestions(si, c); len(qs) > 0 {
		return qs
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
		Cmd:     "top -bcn1 w512",
		Section: "Utilization",
		Summary: "One-shot batch snapshot of processes sorted by CPU.\nBatch mode (-b -n1) avoids needing a TTY; -c shows the full\ncommand line and `w 512` widens output so the COMMAND column\nisn't truncated (e.g. `stress-+`).",
	},
	{
		Cmd:      "pidstat 1 N",
		Section:  "Utilization",
		Summary:  "Per-process CPU usage over N intervals.\nFinds which process is consuming time. (sysstat package.)\nNote: shows host PID namespace. Processes inside Docker/containerd\ncontainers appear as their runtime (`runc`, `containerd-shim`) or\nare absent; pivot to `docker stats` for service-level attribution.",
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
		Summary: "Recent kernel errors and warnings.\nLook for MCE, thermal throttling, hardware faults.\nUses severity filtering rather than keyword grep so unusual CPU/hardware warnings remain visible.\n" + dmesgPermissionNote,
	},
	{
		Cmd:                 "journalctl -k -b -p warning --no-pager -n 30",
		Section:             "Errors",
		Summary:             "Recent kernel-level warnings and errors via journald.\nAlternative to dmesg on systemd systems.",
		Requires:            []string{"journalctl"},
		HideWhenUnavailable: true,
	},
	{
		Cmd:     "grep . /sys/devices/system/cpu/*/thermal_throttle/* 2>/dev/null",
		Section: "Errors",
		Summary: "Per-CPU thermal-throttle event counters.\nNon-zero values mean thermal events have occurred.",
	},
}
