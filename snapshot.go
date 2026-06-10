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
	Sections      []SnapshotSection
	NotCaptured   []Observation
	Sources       []string
	Values        map[string]Value
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
	var notCaptured []Observation
	for _, obs := range s.Investigation.Observations {
		v, ok := obs.Extract(s.System, s.Captured)
		if ok {
			grouped[obs.Section] = append(grouped[obs.Section], SnapshotItem{Title: obs.Title, Value: v})
			values[obs.Name] = v
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
		CapturedCount: len(s.Captured),
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
	fmt.Println(strings.Repeat("=", 60))
	if len(s.Sources) == 0 {
		if s.CapturedCount == 0 {
			fmt.Println("No commands captured yet.")
		} else {
			fmt.Println("No USE-relevant data captured yet.")
		}
		fmt.Println()
		return
	}
	fmt.Printf("Captured from: %s\n", strings.Join(s.Sources, "; "))
	fmt.Println()
	titleWidth := s.itemTitleWidth()
	for _, sec := range s.Sections {
		fmt.Println(sec.Title)
		for _, it := range sec.Items {
			fmt.Printf("  %-*s %s\n", titleWidth, it.Title, formatValue(it.Value))
		}
		fmt.Println()
	}
	if len(s.NotCaptured) > 0 {
		fmt.Println("Not captured (no data yet):")
		for _, o := range s.NotCaptured {
			fmt.Printf("  %s\n", o.Title)
		}
		fmt.Println()
	}
}

func (s Snapshot) itemTitleWidth() int {
	width := 0
	for _, sec := range s.Sections {
		for _, it := range sec.Items {
			if len(it.Title) > width {
				width = len(it.Title)
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
