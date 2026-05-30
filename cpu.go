package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var cpuInvestigation = &Investigation{
	Name:  "cpu",
	Title: "CPU — Utilization, Saturation, Errors",
	Description: "Investigate CPU using Brendan Gregg's USE method.\n" +
		"Run commands at the prompt; the harness captures their output\n" +
		"and asks specific questions about what you saw.",
	StepsFn:      cpuSteps,
	Observations: cpuObservations,
	Commands:     cpuCommands,
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
					"means more runnable processes than CPUs — the run-queue is saturated.\n"+
					"The 5- and 15-minute values show whether that's a brief spike or steady.",
				si.NumCPU),
		},
	}

	if si.HasMpstat {
		steps = append(steps, GuideStep{
			Name:          "per-cpu",
			Intro:         "Step 2: mpstat breaks down utilization per logical CPU.\nThis tells \"all cores busy\" apart from \"one core pegged.\"",
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
		Teaching: "Recent MCE (machine-check exception) or thermal throttling messages mean\n" +
			"physical CPU problems; absence is the healthy case. On idle laptops you'll\n" +
			"usually see nothing here — that's fine.",
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
				"If `r` keeps going above %d (= NumCPU on this system), the run-queue is\n"+
					"saturated. High `wa` means CPUs are idle waiting on I/O — the bottleneck is\n"+
					"storage, not CPU. Non-zero `st` means the hypervisor is giving cycles to\n"+
					"another tenant; on cloud VMs, steady `st` is contention you can't fix\n"+
					"from inside the guest.",
				si.NumCPU),
		},
		{
			Cmd:         "cat /proc/pressure/cpu",
			QuestionsFn: procPressureCpuQuestions,
			Teaching: "PSI's `some avg10/avg60/avg300` values are the share of time at least one\n" +
				"task was stalled waiting for CPU over those windows. Steady values above\n" +
				"~10% mean run-queue contention even when loadavg looks moderate. For\n" +
				"system-wide CPU PSI, `some` is the useful pressure signal; a `full` row may\n" +
				"appear, but CPU `full` is undefined at system level and is reported as zero\n" +
				"for compatibility.",
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
		Correct: "Percent of CPU time spent running application code outside the kernel",
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
			"Percent of CPU time spent by lower-priority processes",
		},
	},
	{
		Column:  "id",
		Correct: "Percent of CPU time with no runnable work",
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
		Correct: "Percent of CPU time unavailable to this guest because the hypervisor was running other virtual machines",
		Distractors: []string{
			"Percent of CPU time spent waiting on local disk I/O",
			"Percent of CPU time spent servicing software interrupts",
			"Percent of CPU time reserved for system daemons",
		},
	},
}

var vmstatCPUQuestionPicks = filterColumnQuestionPicks(vmstatQuestionPicks, "r", "b", "us", "sy", "id", "wa", "st")

var mpstatHeaderRe = regexp.MustCompile(`%idle`)

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
		Column:  "%usr",
		Correct: "Percent of CPU time spent running application code outside the kernel",
		Distractors: []string{
			"Percent of CPU time spent in the kernel",
			"Percent of CPU time spent idle",
			"Percent of CPU time spent waiting on outstanding disk I/O",
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
		Correct: "Percent of CPU time with no runnable work while a disk I/O request was outstanding",
		Distractors: []string{
			"Percent of CPU time spent processing I/O interrupts",
			"Percent of time disks were saturated",
			"Latency in milliseconds before each I/O completes",
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
		Correct: "Percent of CPU time spent servicing deferred kernel interrupt handlers",
		Distractors: []string{
			"Percent of CPU time spent servicing hardware interrupts",
			"Percent of CPU time spent running lower-priority processes",
			"Percent of CPU time stolen by the hypervisor",
		},
	},
	{
		Column:  "%steal",
		Correct: "Percent of CPU time unavailable to this guest because the hypervisor was running other virtual machines",
		Distractors: []string{
			"Percent of CPU time spent in software interrupts",
			"Percent of CPU time spent waiting for local disk I/O",
			"Percent of CPU time reserved for kernel threads",
		},
	},
	{
		Column:  "%idle",
		Correct: "Percent of CPU time with no runnable work and no outstanding disk I/O request",
		Distractors: []string{
			"Percent of CPU time with no runnable work because disk I/O was outstanding",
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
			Correct: "Total CPU time of all processes attached to that TTY since the user logged in",
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
				"Combined CPU usage across all the user's TTYs",
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
var psiFullRe = regexp.MustCompile(`(?m)^full\s+avg10=([0-9.]+)\s+avg60=([0-9.]+)\s+avg300=([0-9.]+)\s+total=(\d+)`)
var psiCPULineRe = regexp.MustCompile(`^(some|full)\s+avg10=([0-9.]+)\s+avg60=([0-9.]+)\s+avg300=([0-9.]+)\s+total=(\d+)`)

// procPressureCpuQuestions handles `cat /proc/pressure/cpu` (PSI). All
// three questions are returned so the guide step (QuestionCount=3) can
// ask one of each per session.
func procPressureCpuQuestions(si SystemInfo, c CapturedCommand) []Question {
	m := psiSomeRe.FindStringSubmatch(c.Output)
	if m == nil {
		return nil
	}
	avg10, avg60, avg300, total := m[1], m[2], m[3], m[4]
	fullStem := "Why might `/proc/pressure/cpu` omit a `full` row on some kernels, or show it as zero on newer kernels?"
	if psiFullRe.MatchString(c.Output) {
		fullStem = "Your `/proc/pressure/cpu` output includes a `full` row. How should you interpret system-wide CPU `full` PSI?"
	}
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
			Stem:    fullStem,
			Correct: "At the system-wide CPU level, `full` is undefined and reported as zero for compatibility; use `some` to judge CPU pressure",
			Distractors: []string{
				"The `full` row is the primary CPU saturation signal and should replace `some` when present",
				"`full` is the percentage of logical CPUs running user-space work at 100%",
				"`full` is hidden unless the system has been under load for at least 60 seconds",
			},
		},
		{
			Stem: fmt.Sprintf(
				"Your PSI output ended with `total=%s`. What does that counter represent?",
				total),
			Correct: "Total microseconds since boot when at least one task was stalled on CPU",
			Distractors: []string{
				"Number of distinct tasks that have ever stalled on CPU since boot",
				"Number of CPU-steal events recorded by the hypervisor",
				"Total milliseconds the run-queue has been non-empty",
			},
		},
	}
}

func extractPSICPU(which string) func(SystemInfo, []CapturedCommand) (Value, bool) {
	return func(si SystemInfo, caps []CapturedCommand) (Value, bool) {
		for i := len(caps) - 1; i >= 0; i-- {
			c := caps[i]
			if baseCmd(c.Cmd) != "cat" || !strings.Contains(c.Cmd, "/proc/pressure/cpu") {
				continue
			}
			for _, line := range strings.Split(c.Output, "\n") {
				m := psiCPULineRe.FindStringSubmatch(line)
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

// sarUHeaderRe is the column-header line printed by `sar -u`.
var sarUHeaderRe = regexp.MustCompile(`%user\s+%nice\s+%system\s+%iowait\s+%steal\s+%idle`)

// sarUQuestions returns one random column question. Used by the extractor
// (free-form practice mode) where the user may run sar -u alongside other
// commands and only needs one comprehension check.

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
		Correct: "Percent of CPU time spent running regular-priority application code outside the kernel",
		Distractors: []string{
			"Percent of CPU time spent in kernel code on behalf of applications",
			"Percent of CPU time available to non-root accounts",
			"Percent of total CPUs occupied by application processes",
		},
	},
	{
		Column:  "%nice",
		Correct: "Percent of CPU time spent running lower-priority user-space processes",
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
		Correct: "Percent of CPU time with no runnable work while a disk I/O request was outstanding",
		Distractors: []string{
			"Percent of CPU time spent actively handling I/O interrupts",
			"Latency in seconds before each I/O operation completes",
			"Percent of disk capacity currently in use",
		},
	},
	{
		Column:  "%steal",
		Correct: "Percent of CPU time unavailable to this guest because the hypervisor was running other virtual machines",
		Distractors: []string{
			"Percent of CPU time taken by higher-priority local processes",
			"Percent of CPU time spent servicing software interrupts",
			"Percent of CPU time the guest could not access due to a NUMA penalty",
		},
	},
	{
		Column:  "%idle",
		Correct: "Percent of CPU time with no runnable work and no outstanding disk I/O request",
		Distractors: []string{
			"Percent of CPU cores currently powered down",
			"Percent of CPU time with no runnable work because disk I/O was outstanding",
			"Percent of total CPU capacity that's currently free",
		},
	},
}

// catCpuProcQuestions dispatches `cat` extractor matches to whichever
// /proc file's question handler recognises the captured output. Returns
// nil if neither matches, which is the right answer for an unrelated
// `cat` invocation.

// ----- Observations (cross-command, feed snapshot + recall + synthesis) -----

var cpuObservations = []Observation{
	{
		Name:      "loadavg_1min",
		Title:     "1-min load average",
		Section:   "Utilization",
		Extract:   extractLoadavgN(0),
		Verdict:   verdictLoad1min,
		Heuristic: "load average ÷ NumCPU near 1 = fully committed; above 1 = more runnable demand than CPUs",
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
		Name:      "mpstat_idle_mean",
		Title:     "Mean %idle (mpstat)",
		Section:   "Utilization",
		Extract:   extractMpstatIdleMean,
		Verdict:   verdictIdleMean,
		Heuristic: "utilization ≈ 100 − %idle: low idle = high utilization",
	},
	{
		Name:    "mpstat_idle_range",
		Title:   "Per-CPU %idle range",
		Section: "Utilization",
		Extract: extractMpstatIdleRange,
	},
	{
		Name:      "vmstat_r",
		Title:     "vmstat r (run-queue)",
		Section:   "Saturation",
		Extract:   extractVmstatColumn("r"),
		Verdict:   verdictRunQueue,
		Heuristic: "vmstat r above NumCPU = runnable threads waiting for a CPU = saturation (the tool drops vmstat's since-boot first row, so the verdict reflects interval samples only)",
	},
	{
		Name:      "vmstat_wa",
		Title:     "vmstat wa (cpu I/O wait)",
		Section:   "Saturation",
		Extract:   extractVmstatColumn("wa"),
		Verdict:   verdictIOWait,
		Heuristic: "high vmstat wa = CPU is waiting on I/O, not actively saturated — if disk PSI / aqu-sz are also high, the saturation belongs to the disk, not the CPU (run iostat -xz to confirm)",
	},
	{
		Name:      "vmstat_st",
		Title:     "vmstat st (hypervisor steal)",
		Section:   "Saturation",
		Extract:   extractVmstatColumn("st"),
		Verdict:   verdictSteal,
		Heuristic: "vmstat st above 0 = CPU cycles stolen by the hypervisor = contention for the physical CPU (the tool drops vmstat's since-boot first row)",
	},
	{
		Name:      "psi_cpu_some_avg10",
		Title:     "CPU PSI some (avg10)",
		Section:   "Saturation",
		Extract:   extractPSICPU("some"),
		Verdict:   verdictPSISome,
		Heuristic: "CPU PSI 'some' avg10 = percent of the last 10s window with at least one task stalled waiting for CPU; >10% steady = run-queue contention",
	},
	{
		Name:      "dmesg_cpu_keywords",
		Title:     "dmesg CPU/thermal/MCE",
		Section:   "Errors",
		Extract:   extractDmesgCpuKeywords,
		Verdict:   verdictDmesgErrors,
		Heuristic: "MCE / thermal / throttle messages in the kernel log = CPU hardware errors",
	},
}

// ----- Diagnosis verdicts -----
// Each maps an observed value to the Signal it contributes to its USE
// dimension. Saturation/Errors collapse to Low (absent) or High (present);
// Utilization uses all three levels. These encode the cheatsheet heuristics in
// machine-readable form so `diagnose` can grade a learner's claim against what
// they actually observed, without ever asserting an absolute system state.

func verdictLoad1min(si SystemInfo, v Value, _ Snapshot) Signal {
	if si.NumCPU <= 0 {
		return SignalNone
	}
	ratio := v.Number / float64(si.NumCPU)
	switch {
	case ratio >= 1.0:
		return SignalHigh
	case ratio >= 0.7:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictIdleMean(_ SystemInfo, v Value, _ Snapshot) Signal {
	switch {
	case v.Number < 20:
		return SignalHigh
	case v.Number < 70:
		return SignalModerate
	default:
		return SignalLow
	}
}

func verdictRunQueue(si SystemInfo, v Value, _ Snapshot) Signal {
	max := v.Max()
	if math.IsNaN(max) {
		return SignalNone
	}
	if si.NumCPU > 0 && max > float64(si.NumCPU) {
		return SignalHigh
	}
	return SignalLow
}

func verdictSteal(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Max() > 0 {
		return SignalHigh
	}
	return SignalLow
}

func verdictDmesgErrors(_ SystemInfo, v Value, _ Snapshot) Signal {
	if v.Number > 0 {
		return SignalHigh
	}
	return SignalLow
}

// verdictIOWait is the canonical cross-resource verdict: high iowait on the
// CPU side often means the CPU is *waiting* on disk, not that the CPU itself
// is saturated. The verdict consults the snapshot for disk saturation signals
// — if disk is the bottleneck, this observation reads Low for CPU saturation
// (with the static Heuristic explaining the secondary-cause inference). If
// iowait is elevated but disk shows no saturation (or no disk data was
// captured), it reads Moderate, prompting the learner to investigate disk
// next.
func verdictIOWait(_ SystemInfo, v Value, snap Snapshot) Signal {
	wa := v.Max()
	if math.IsNaN(wa) || wa < 10 {
		return SignalLow
	}
	if diskShowsSaturation(snap) {
		return SignalLow
	}
	return SignalModerate
}

func diskShowsSaturation(snap Snapshot) bool {
	if v, ok := snap.Values["iostat_max_aqu_sz"]; ok && v.Number > 1 {
		return true
	}
	if v, ok := snap.Values["psi_io_some_avg10"]; ok && v.Number > 10 {
		return true
	}
	if v, ok := snap.Values["psi_io_full_avg10"]; ok && v.Number > 0 {
		return true
	}
	return false
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
			var perCmd []float64
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
				perCmd = append(perCmd, n)
			}
			// vmstat's first interval row is "since boot" stats, not an
			// interval sample. Drop it per captured command so verdicts and
			// recall reflect only genuine interval data. A bare `vmstat`
			// (one row, since-boot) therefore yields no captured samples,
			// which correctly tells the learner to use `vmstat 1 N`.
			if len(perCmd) > 0 {
				perCmd = perCmd[1:]
			}
			samples = append(samples, perCmd...)
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
		Number: float64(matched),
		Text:   fmt.Sprintf("%d/%d lines mention CPU/thermal/MCE keywords", matched, totalLines),
	}, true
}

// ----- Recall question generators -----

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

func dmesgQuestions(si SystemInfo, c CapturedCommand) []Question {
	low := strings.ToLower(c.Output)
	tool := kernelLogQuestionTool(c.Cmd)
	if strings.Contains(low, "machine check") || strings.Contains(low, "mce:") {
		return []Question{{
			Stem:    fmt.Sprintf("Your `%s` output mentions a machine-check (MCE) event. What does this usually mean?", tool),
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
				"Workloads are moved to other physical sockets",
				"The kernel kills the highest-CPU processes",
			},
		}}
	}
	return nil
}

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
		Summary:  "Per-CPU breakdown (%usr, %sys, %iowait, %idle, ...)\nover N one-second intervals. Tells all-cores-busy\napart from one-core-pegged. (sysstat package.)",
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
		Summary:  "Per-process CPU usage over N intervals.\nFinds which process is using time. (sysstat package.)\nNote: shows host PID namespace. Processes inside Docker/containerd\ncontainers appear as their runtime (`runc`, `containerd-shim`) or\nare absent; switch to `docker stats` to see which service.",
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
		Summary: "Recent kernel errors and warnings.\nLook for MCE, thermal throttling, hardware faults.\nUses severity filtering instead of keyword grep so unusual CPU/hardware warnings stay visible.\n" + dmesgPermissionNote,
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
