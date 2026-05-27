package main

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Investigation struct {
	Name         string
	Title        string
	Description  string
	StepsFn      func(SystemInfo) []GuideStep
	Observations []Observation
	Commands     []CommandRef
	// DiagnoseNotes carries per-dimension caveats shown to the learner at the
	// `diagnose` verdict prompt — e.g. for Network Utilization, explaining
	// that the tool cannot infer utilization without per-interface link speed.
	// A nil map or missing entry prints nothing.
	DiagnoseNotes map[string]string
}

type GuideStep struct {
	Name         string
	Intro        string
	Suggested    string
	Alternatives []string
	QuestionsFn  func(SystemInfo, CapturedCommand) []Question
	// QuestionCount defaults to 1. Use higher values for steps whose
	// QuestionsFn returns a pool of interchangeable guide checks.
	QuestionCount      int
	AcceptAny          bool
	EmptyOutputMessage string
	Teaching           string
	// Filter, if set, rewrites the captured command output before it is
	// shown to the learner and stored for questions. Used to drop noise
	// (e.g. loopback and container veth interfaces) so the step focuses on
	// USE-relevant data. The command itself is left untouched, so question
	// stems still reference the clean canonical command.
	Filter func(CapturedCommand) string
}

type Question struct {
	Stem        string
	Correct     string
	Distractors []string
}

type QuestionResult struct {
	Correct bool
	Quit    bool
	Skipped bool
}

type questionCommandRunner func(string) CapturedCommand

// stepVariant is one of several interchangeable commands a single guide
// step can use. At session start we pick one for the `Suggested` prompt;
// the step's QuestionsFn dispatches across all variants so the learner can
// also run a different one and still get a tailored comprehension check.
type stepVariant struct {
	Cmd         string
	QuestionsFn func(SystemInfo, CapturedCommand) []Question
	Teaching    string
	Available   func(SystemInfo) bool
}

// pickStepVariant returns a variant available on this system, chosen at
// random. Falls back to the first variant if none are marked available
// (every step must always have some command to suggest).
func pickStepVariant(si SystemInfo, variants []stepVariant) stepVariant {
	var avail []stepVariant
	for _, v := range variants {
		if v.Available == nil || v.Available(si) {
			avail = append(avail, v)
		}
	}
	if len(avail) == 0 {
		return variants[0]
	}
	return pickRandom(avail)
}

// combineVariantQuestions returns a QuestionsFn that walks variants in order
// and returns the first non-empty result. Each variant's QuestionsFn is
// expected to recognise only its own output format; order variants
// most-specific first when formats can overlap.
func combineVariantQuestions(variants []stepVariant) func(SystemInfo, CapturedCommand) []Question {
	return func(si SystemInfo, c CapturedCommand) []Question {
		for _, v := range variants {
			if qs := v.QuestionsFn(si, c); len(qs) > 0 {
				return qs
			}
		}
		return nil
	}
}

type CommandRef struct {
	Cmd                 string
	Section             string
	Summary             string
	Requires            []string
	HideWhenUnavailable bool
}

const dmesgPermissionNote = "Direct dmesg access reads the kernel buffer; on systems with kernel.dmesg_restrict=1, use sudo."

func journalctlAlternative(si SystemInfo, cmd string) []string {
	if !si.HasJournalctl {
		return nil
	}
	return []string{cmd}
}

// Resource labels used by whole-system diagnose to group prompts. These are
// the strings stored in Observation.Resource by init().
const (
	ResourceCPU     = "CPU"
	ResourceMemory  = "Memory"
	ResourceDisk    = "Disk"
	ResourceNetwork = "Network"
)

// resourceOrder is the canonical USE walk order for `practice system`.
var resourceOrder = []string{ResourceCPU, ResourceMemory, ResourceDisk, ResourceNetwork}

var investigations = map[string]*Investigation{
	"cpu":     cpuInvestigation,
	"memory":  memoryInvestigation,
	"disk":    diskInvestigation,
	"network": networkInvestigation,
}

var appRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// tagObservations stamps a Resource label onto every entry in obs, mutating
// the backing array. Observation slices in the per-resource Investigation
// structs share that backing array, so the tag propagates through.
func tagObservations(obs []Observation, resource string) {
	for i := range obs {
		obs[i].Resource = resource
	}
}

func init() {
	tagObservations(cpuObservations, ResourceCPU)
	tagObservations(memoryObservations, ResourceMemory)
	tagObservations(diskObservations, ResourceDisk)
	tagObservations(networkObservations, ResourceNetwork)

	// systemInvestigation aggregates the four resources for whole-system
	// practice + diagnose. It is practice-only: there is no guided walkthrough
	// across all four resources, so StepsFn returns nothing.
	sysObs := make([]Observation, 0,
		len(cpuObservations)+len(memoryObservations)+len(diskObservations)+len(networkObservations))
	sysObs = append(sysObs, cpuObservations...)
	sysObs = append(sysObs, memoryObservations...)
	sysObs = append(sysObs, diskObservations...)
	sysObs = append(sysObs, networkObservations...)

	var sysCmds []CommandRef
	sysCmds = append(sysCmds, cpuInvestigation.Commands...)
	sysCmds = append(sysCmds, memoryInvestigation.Commands...)
	sysCmds = append(sysCmds, diskInvestigation.Commands...)
	sysCmds = append(sysCmds, networkInvestigation.Commands...)

	investigations["system"] = &Investigation{
		Name:  "system",
		Title: "System — whole-system USE diagnosis",
		Description: "Practice across the full system: capture any commands across CPU,\n" +
			"memory, disk, and network, then run `diagnose` to check each resource\n" +
			"by USE (Utilization, Saturation, Errors).",
		StepsFn:      func(SystemInfo) []GuideStep { return nil },
		Observations: sysObs,
		Commands:     sysCmds,
		// DiagnoseNotes is intentionally nil at the system level: notes are
		// per-(resource, dim) and must be looked up from the per-resource
		// investigation at prompt time (see investigationForResource).
	}
}

// investigationForResource maps a Resource label back to its per-resource
// Investigation. Used by `diagnose` in system mode so that a per-dimension
// note (e.g. Network Utilization's inferrability caveat) fires only at the
// prompt for the resource it belongs to.
func investigationForResource(resource string) *Investigation {
	switch resource {
	case ResourceCPU:
		return cpuInvestigation
	case ResourceMemory:
		return memoryInvestigation
	case ResourceDisk:
		return diskInvestigation
	case ResourceNetwork:
		return networkInvestigation
	}
	return nil
}

func resourceNames() []string {
	out := make([]string, 0, len(investigations))
	for k := range investigations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func getInvestigation(name string) (*Investigation, error) {
	inv, ok := investigations[name]
	if !ok {
		return nil, fmt.Errorf("unknown resource %q (available: %s)", name, strings.Join(resourceNames(), ", "))
	}
	return inv, nil
}

func askQuestion(q Question) QuestionResult {
	return askQuestionWithCommandRunner(q, nil)
}

func askQuestionWithCommandRunner(q Question, run questionCommandRunner) QuestionResult {
	options := append([]string{q.Correct}, q.Distractors...)
	appRand.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })
	fmt.Println()
	fmt.Println("--- Check ---")
	fmt.Println(q.Stem)
	for i, o := range options {
		fmt.Printf("  %d. %s\n", i+1, o)
	}
	for {
		fmt.Print("Choice: ")
		line, ok := readLine()
		if !ok {
			return QuestionResult{Quit: true}
		}
		if isExitCommand(line) {
			fmt.Println("Exiting.")
			return QuestionResult{Quit: true}
		}
		if strings.TrimSpace(line) == "skip" {
			fmt.Println("(Skipped.)")
			return QuestionResult{Skipped: true}
		}
		if cmd, ok := stripCopiedShellPrompt(line); ok {
			if cmd == "" {
				fmt.Printf("Type a command after `$`, or pick a number 1-%d.\n", len(options))
				continue
			}
			if run == nil {
				fmt.Printf("Pick a number 1-%d.\n", len(options))
				continue
			}
			c := run(cmd)
			if c.Output != "" && !strings.HasSuffix(c.Output, "\n") {
				fmt.Println()
			}
			if c.Failed {
				fmt.Printf("(Command failed; try another `$ <command>`, or pick a number 1-%d.)\n", len(options))
				continue
			}
			fmt.Printf("(Ran `%s`; now pick a number 1-%d.)\n", cmd, len(options))
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Printf("Pick a number 1-%d.\n", len(options))
			continue
		}
		chosen := options[n-1]
		fmt.Println()
		fmt.Println("--- Feedback ---")
		fmt.Printf("Your answer: %s\n", chosen)
		if chosen == q.Correct {
			fmt.Println("Result: correct")
			return QuestionResult{Correct: true}
		}
		fmt.Println("Result: not quite")
		fmt.Printf("Correct answer: %s\n", q.Correct)
		return QuestionResult{}
	}
}

func isExitCommand(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "exit", "quit":
		return true
	default:
		return false
	}
}

// baseCmd returns the first whitespace-separated token of a command line,
// skipping a leading `sudo`. Returns "" for empty input. Used to match
// captured commands against an extractor without false positives like
// `vim dmesg.txt` matching dmesg.
func baseCmd(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if base == "sudo" && len(fields) > 1 {
		base = fields[1]
	}
	return base
}

// personalizeQuestions rewrites the leading ``In `<base>...` `` reference in
// each question stem so it shows the exact command the learner ran, rather
// than the canonical form baked into the generator (e.g. `iostat -x`). We
// only touch the first backticked reference and only when it begins with the
// base command of actualCmd, so column names like `r/s` or `%util` are left
// alone.
func personalizeQuestions(qs []Question, actualCmd string) []Question {
	base := baseCmd(actualCmd)
	if base == "" || len(qs) == 0 {
		return qs
	}
	re := regexp.MustCompile("`" + regexp.QuoteMeta(base) + `(\b[^` + "`" + `]*)?` + "`")
	replacement := "`" + actualCmd + "`"
	for i := range qs {
		qs[i].Stem = re.ReplaceAllString(qs[i].Stem, replacement)
	}
	return qs
}

// pickUniqueDistractors selects up to `want` strings from `pool`, skipping
// any that equal `correct` or that have already been picked. Order is
// preserved. Used by recall-question generators to ensure distractor lists
// don't accidentally duplicate the correct answer (which can happen when
// arithmetic distractor formulae degenerate at extreme values like 0 or
// 100, e.g. clamp(0-15, 0, 100) == 0 == correct).
func pickUniqueDistractors(correct string, pool []string, want int) []string {
	seen := map[string]bool{correct: true}
	out := make([]string, 0, want)
	for _, c := range pool {
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= want {
			break
		}
	}
	return out
}

// makeRecallQuestion builds a single-element []Question slice from a stem,
// correct answer, and a candidate pool of distractors. Returns nil if fewer
// than 2 distinct distractors can be produced from the pool — a question
// with 0 or 1 distractor isn't a quiz. Recall functions should pass a pool
// that includes both arithmetic variants of the correct answer (for
// plausibility) and a few constants (for fallback when arithmetic
// degenerates).
func makeRecallQuestion(stem, correct string, pool []string) []Question {
	distractors := pickUniqueDistractors(correct, pool, 3)
	if len(distractors) < 2 {
		return nil
	}
	return []Question{{
		Stem:        stem,
		Correct:     correct,
		Distractors: distractors,
	}}
}

// pickRandom returns one element from xs, chosen uniformly at random. Used by
// comprehension-question generators that have several equally valid teaching
// prompts (e.g. asking about the 1-minute, 5-minute, or 15-minute load
// average) and shouldn't always pick the same one.
func pickRandom[T any](xs []T) T {
	return xs[appRand.Intn(len(xs))]
}

func printCommands(inv *Investigation, si SystemInfo) {
	fmt.Printf("\n%s — command reference\n", inv.Title)
	fmt.Println(strings.Repeat("=", 60))
	bySection := map[string][]CommandRef{}
	for _, c := range inv.Commands {
		bySection[c.Section] = append(bySection[c.Section], c)
	}
	for _, sec := range []string{"Utilization", "Saturation", "Errors"} {
		cmds, ok := bySection[sec]
		if !ok {
			continue
		}
		fmt.Printf("\n%s\n%s\n", sec, strings.Repeat("-", len(sec)))
		for _, c := range cmds {
			status := commandStatus(c, si)
			if status != "" && c.HideWhenUnavailable {
				continue
			}
			if status == "" {
				fmt.Printf("\n  %s\n", c.Cmd)
			} else {
				fmt.Printf("\n  %s  [%s]\n", c.Cmd, status)
			}
			for _, line := range strings.Split(c.Summary, "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
	fmt.Println()
}

func commandStatus(c CommandRef, si SystemInfo) string {
	var missing []string
	for _, req := range c.Requires {
		if requirementAvailable(req, si) {
			continue
		}
		missing = append(missing, requirementHint(req))
	}
	if len(missing) == 0 {
		return ""
	}
	return "unavailable: " + strings.Join(missing, ", ")
}

func requirementAvailable(req string, si SystemInfo) bool {
	switch req {
	case "mpstat":
		return si.HasMpstat
	case "pidstat":
		return si.HasPidstat
	case "sar":
		return si.HasSar
	case "journalctl":
		return si.HasJournalctl
	case "psi":
		return si.HasPSI
	default:
		return haveCmd(req)
	}
}

func requirementHint(req string) string {
	switch req {
	case "mpstat", "pidstat", "sar":
		return req + " not found; install sysstat"
	case "journalctl":
		return "journalctl not found"
	case "psi":
		return "/proc/pressure/cpu not available"
	default:
		return req + " not found"
	}
}
