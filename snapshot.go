package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Value struct {
	Number  float64
	Samples []float64
	Unit    string
	Text    string
	Note    string
}

func (v Value) Min() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	m := v.Samples[0]
	for _, x := range v.Samples[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func (v Value) Max() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	m := v.Samples[0]
	for _, x := range v.Samples[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func (v Value) Mean() float64 {
	if len(v.Samples) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, x := range v.Samples {
		sum += x
	}
	return sum / float64(len(v.Samples))
}

// Signal is the diagnostic reading an observation contributes to its USE
// dimension (its Section). For Utilization, Low/Moderate/High mean what they
// say. For Saturation and Errors, Low means "absent" and High means "present"
// (Moderate is unused there). SignalNone means the observation carries no
// diagnostic reading — its value is informational, or it cannot be classified.
type Signal int

const (
	SignalNone Signal = iota
	SignalLow
	SignalModerate
	SignalHigh
)

type Observation struct {
	Name    string
	Title   string
	Section string
	// Resource is the USE subsystem this observation belongs to ("CPU",
	// "Memory", "Disk", "Network"). It's set centrally in init() rather than
	// per-entry, so the per-resource observation literals stay clean. Used
	// by whole-system diagnose to group prompts by resource.
	Resource string
	Extract  func(SystemInfo, []CapturedCommand) (Value, bool)
	// Verdict classifies this observation's value for its USE dimension,
	// reading SystemInfo and the full Snapshot so a context-sensitive rule can
	// consult sibling observations (e.g. a run-queue that only reads as
	// saturation alongside low idle). A nil Verdict means the observation is
	// informational: it can be displayed and recalled, but not cited as
	// diagnosis evidence.
	Verdict func(SystemInfo, Value, Snapshot) Signal
	// Heuristic is the one-line rule of thumb shown in diagnose feedback,
	// e.g. "vmstat r above NumCPU = threads waiting for CPU = saturation".
	Heuristic string
}

type Snapshot struct {
	Sections    []SnapshotSection
	NotCaptured []Observation
	Sources     []string
	Values      map[string]Value
	// Origins records which command each value came from, as an evidence
	// source key (see evidenceSourceKey). Diagnose uses it to tell several
	// readings from one command apart from independent signals. A missing
	// entry means the source is unknown and the value counts on its own.
	Origins       map[string]string
	CapturedCount int
}

type SnapshotSection struct {
	Title string
	Items []SnapshotItem
}

type SnapshotItem struct {
	Title string
	Value Value
}

func (s *Session) Snapshot() Snapshot {
	grouped := map[string][]SnapshotItem{}
	values := map[string]Value{}
	origins := map[string]string{}
	var notCaptured []Observation
	for _, obs := range s.Investigation.Observations {
		v, ok := obs.Extract(s.System, s.Captured)
		if ok {
			grouped[obs.Section] = append(grouped[obs.Section], SnapshotItem{Title: obs.Title, Value: v})
			values[obs.Name] = v
			if origin := valueOrigin(obs, s.System, s.Captured); origin != "" {
				origins[obs.Name] = origin
			}
		} else {
			notCaptured = append(notCaptured, obs)
		}
	}
	var sections []SnapshotSection
	for _, name := range []string{"Utilization", "Saturation", "Errors"} {
		if items, ok := grouped[name]; ok {
			sections = append(sections, SnapshotSection{Title: name, Items: items})
		}
	}
	srcs := relevantSources(s.Investigation, s.System, s.Captured)
	return Snapshot{
		Sections:      sections,
		NotCaptured:   notCaptured,
		Sources:       srcs,
		Values:        values,
		Origins:       origins,
		CapturedCount: len(s.Captured),
	}
}

// valueOrigin finds the most recent single command that produces obs's value
// and returns its evidence source key, or "" if no single command does.
func valueOrigin(obs Observation, si SystemInfo, caps []CapturedCommand) string {
	if obs.Extract == nil {
		return ""
	}
	for i := len(caps) - 1; i >= 0; i-- {
		if _, ok := obs.Extract(si, caps[i:i+1]); ok {
			return evidenceSourceKey(caps[i].Cmd)
		}
	}
	return ""
}

// evidenceSourceKey names the measurement a command takes, so readings from
// the same measurement count as one source. It is the command name, except
// that sar reports and cat'd files are told apart, since `sar -n DEV` and
// `sar -n EDEV`, or /proc/meminfo and /proc/pressure/memory, are different
// measurements. Different flags to the same tool (iostat -x vs -xz) are not,
// and dmesg and journalctl both read the one kernel log.
func evidenceSourceKey(cmd string) string {
	switch base := commandBase(cmd); base {
	case "dmesg", "journalctl":
		return "kernel log"
	case "sar", "cat":
		return commandFamilyKey(cmd)
	default:
		return base
	}
}

func relevantSources(inv *Investigation, si SystemInfo, caps []CapturedCommand) []string {
	srcSet := map[string]struct{}{}
	for _, c := range caps {
		if commandRelevantToInvestigation(inv, si, c) {
			srcSet[c.Cmd] = struct{}{}
		}
	}
	srcs := make([]string, 0, len(srcSet))
	for c := range srcSet {
		srcs = append(srcs, c)
	}
	sort.Strings(srcs)
	return srcs
}

func commandRelevantToInvestigation(inv *Investigation, si SystemInfo, c CapturedCommand) bool {
	return commandMatchesInvestigationReference(inv, c) || commandContributesObservation(inv, si, c)
}

func commandMatchesInvestigationReference(inv *Investigation, c CapturedCommand) bool {
	if inv == nil {
		return false
	}
	key := commandFamilyKey(c.Cmd)
	if key == "" {
		return false
	}
	for _, ref := range inv.Commands {
		if commandFamilyKey(ref.Cmd) == key {
			return true
		}
	}
	return false
}

func commandContributesObservation(inv *Investigation, si SystemInfo, c CapturedCommand) bool {
	if inv == nil {
		return false
	}
	for _, obs := range inv.Observations {
		if obs.Extract == nil {
			continue
		}
		if _, ok := obs.Extract(si, []CapturedCommand{c}); ok {
			return true
		}
	}
	return false
}

func (s Snapshot) Print() {
	fmt.Println()
	s.PrintBody()
}

// PrintBody prints the snapshot without a leading blank line, for callers
// that put a heading directly above it.
func (s Snapshot) PrintBody() {
	tutorln(faint(strings.Repeat("=", 60)))
	if len(s.Sources) == 0 {
		if s.CapturedCount == 0 {
			tutorln("No commands captured yet.")
		} else {
			tutorln("No USE-relevant data captured yet.")
		}
		fmt.Println()
		return
	}
	printProse("Captured from: "+strings.Join(s.Sources, "; "), faint)
	fmt.Println()
	titleWidth := s.itemTitleWidth()
	for _, sec := range s.Sections {
		tutorln(bold(sec.Title))
		for _, it := range sec.Items {
			tutorf("  %s %s\n", padRight(it.Title, titleWidth), formatValue(it.Value))
		}
		fmt.Println()
	}
	if len(s.NotCaptured) > 0 {
		tutorln(faint("Not captured (no data yet):"))
		for _, o := range s.NotCaptured {
			tutorln(faint("  " + o.Title))
		}
		fmt.Println()
	}
}

func (s Snapshot) itemTitleWidth() int {
	width := 0
	for _, sec := range s.Sections {
		for _, it := range sec.Items {
			if w := visibleWidth(it.Title); w > width {
				width = w
			}
		}
	}
	if width < 30 {
		return 30
	}
	return width
}

func formatValue(v Value) string {
	var s string
	switch {
	case v.Text != "":
		s = v.Text
	case len(v.Samples) > 0:
		parts := make([]string, len(v.Samples))
		for i, x := range v.Samples {
			parts[i] = formatNumber(x, v.Unit)
		}
		s = strings.Join(parts, ", ")
		s += fmt.Sprintf("  (max %s, mean %s)",
			formatNumber(v.Max(), v.Unit),
			formatNumber(v.Mean(), v.Unit))
	default:
		s = formatNumber(v.Number, v.Unit)
	}
	if v.Note != "" {
		s += "  (" + v.Note + ")"
	}
	return s
}

func formatNumber(n float64, unit string) string {
	if math.IsNaN(n) {
		return "—"
	}
	abs := n
	if abs < 0 {
		abs = -abs
	}
	var formatted string
	switch {
	case abs >= 100 || abs == 0:
		formatted = fmt.Sprintf("%.0f", n)
	case abs >= 10:
		formatted = fmt.Sprintf("%.1f", n)
	default:
		formatted = fmt.Sprintf("%.2f", n)
	}
	return formatted + unit
}
